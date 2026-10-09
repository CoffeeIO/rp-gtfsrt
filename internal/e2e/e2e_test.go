// Package e2e runs the whole pipeline: the real HTTP client against the
// simulated Rejseplanen API, the scheduler, the store, and the served
// protobuf feeds.
package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/coverage"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	"github.com/CoffeeIO/rp-gtfsrt/internal/scheduler"
	"github.com/CoffeeIO/rp-gtfsrt/internal/simulator"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func fetch(t *testing.T, url string) *rt.FeedMessage {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var msg rt.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	return &msg
}

func TestPipelineAgainstSimulator(t *testing.T) {
	// 07:45: boards show the 07:50 and 08:12 departures; X1 (09:00) is
	// outside the 60-minute window.
	now := time.Date(2026, 10, 8, 7, 45, 0, 0, cph)
	clock := func() time.Time { return now }
	sched, err := gtfs.LoadZip(gtfstest.WriteFile(t, gtfstest.Fixture), gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}

	sim := &simulator.Simulator{Schedule: sched, Now: clock, DelayRate: 1, Seed: 3}
	api := httptest.NewServer(sim.Handler())
	defer api.Close()

	// Discovery stations from the coverage planner, as an operator would.
	var stops []string
	for _, st := range coverage.Plan(sched, "20261008", 3) {
		stops = append(stops, st.ID)
	}
	store, alerts := feed.NewStore(), &feed.AlertStore{}
	reg := metrics.New()
	sch := &scheduler.Scheduler{
		API:   &rejseplanen.Client{BaseURL: api.URL + "/api", AccessID: "test", Duration: 60, MaxJourneys: -1, Location: cph},
		Pacer: &scheduler.RatePacer{}, Store: store, Alerts: alerts, Metrics: reg, Now: clock,
		Config: scheduler.Config{
			Stops: stops, BoardInterval: time.Hour, JourneyMode: scheduler.JourneysAll,
			JourneyInterval: time.Minute, JourneyLookahead: time.Hour, AlertsInterval: time.Hour,
		},
	}
	// Two dispatch rounds: boards and alerts, then the journeys they found.
	for round := 0; round < 2; round++ {
		sch.RefreshTracked()
		for {
			task, _ := sch.Next(now)
			if task == nil {
				break
			}
			if err := sch.Execute(context.Background(), task); err != nil {
				t.Fatalf("task failed: %v", err)
			}
		}
	}
	if st := sch.Stats(); st.Calls["board"] != len(stops) || st.Calls["journey"] == 0 || st.Calls["alerts"] != 1 {
		t.Fatalf("calls = %+v", st.Calls)
	}

	server := &feed.Server{Store: store, Alerts: alerts, Schedule: func() *gtfs.Schedule { return sched }, Now: clock, Metrics: reg}
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()

	tu := fetch(t, srv.URL+"/gtfs-rt/trip-updates")
	trips := map[string]*rt.TripUpdate{}
	for _, e := range tu.Entity {
		trips[e.GetTripUpdate().GetTrip().GetTripId()] = e.GetTripUpdate()
	}
	for _, id := range []string{"A1", "A2", "B1", "BUS"} {
		if trips[id] == nil {
			t.Errorf("missing trip update for %s (have %v)", id, keys(trips))
		}
	}
	a1 := trips["A1"]
	if a1 != nil {
		if len(a1.StopTimeUpdate) != 4 {
			t.Fatalf("A1 should have every stop from journeyDetail: %v", a1.StopTimeUpdate)
		}
		last := a1.StopTimeUpdate[3]
		if last.GetArrival().GetDelay() <= 0 || last.GetStopId() != "8600669" {
			t.Fatalf("A1 last stop = %v", last)
		}
	}
	if vp := fetch(t, srv.URL+"/gtfs-rt/vehicle-positions"); len(vp.Entity) == 0 {
		t.Fatal("running journeys should have vehicle positions")
	}
	if al := fetch(t, srv.URL+"/gtfs-rt/alerts"); len(al.Entity) != 2 {
		t.Fatalf("alerts = %d", len(al.Entity))
	}
}

func keys(m map[string]*rt.TripUpdate) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
