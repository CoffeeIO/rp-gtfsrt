package simulator_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	"github.com/CoffeeIO/rp-gtfsrt/internal/simulator"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(h, m int) time.Time { return time.Date(2026, 10, 8, h, m, 0, 0, cph) }

func setup(t *testing.T, sim *simulator.Simulator, now time.Time) *rejseplanen.Client {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture), gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	sim.Schedule = s
	sim.Now = func() time.Time { return now }
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	return &rejseplanen.Client{BaseURL: srv.URL + "/api", AccessID: "test", Duration: 30, MaxJourneys: -1, Location: cph}
}

func TestDepartureBoardOnTime(t *testing.T) {
	c := setup(t, &simulator.Simulator{}, at(8, 0))
	deps, err := c.DepartureBoard(context.Background(), "8600626")
	if err != nil {
		t.Fatal(err)
	}
	// A1, A2, B1 and BUS depart København H at 08:12 (within 30 min).
	if len(deps) != 4 {
		t.Fatalf("deps = %d: %+v", len(deps), deps)
	}
	for _, d := range deps {
		if d.StopID != "8600626" || !d.Scheduled.Equal(at(8, 12)) || d.JourneyRef == "" {
			t.Fatalf("row = %+v", d)
		}
		if d.Realtime == nil || d.Delay() != 0 {
			t.Fatalf("started journeys report on time: %+v", d)
		}
	}
}

func TestDepartureBoardWindowAndPrognosis(t *testing.T) {
	c := setup(t, &simulator.Simulator{}, at(7, 30))
	deps, err := c.DepartureBoard(context.Background(), "8600626")
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 0 {
		t.Fatalf("nothing departs 07:30–08:00: %+v", deps)
	}
	c.Duration = 60
	deps, _ = c.DepartureBoard(context.Background(), "8600626")
	for _, d := range deps {
		// A2 starts at 07:30 so it has a prognosis; B1 starts at 08:12, more
		// than 30 minutes away, so it has none yet.
		if d.Line == "B" && d.Realtime != nil {
			t.Fatalf("B1 should not be prognosed yet: %+v", d)
		}
	}
	c.MaxJourneys = 1
	if deps, _ := c.DepartureBoard(context.Background(), "8600626"); len(deps) != 1 {
		t.Fatalf("maxJourneys = 1 gave %d rows", len(deps))
	}
}

func TestDelaysAreDeterministicAndRamp(t *testing.T) {
	c := setup(t, &simulator.Simulator{DelayRate: 1, Seed: 7}, at(8, 0))
	deps, _ := c.DepartureBoard(context.Background(), "8600626")
	var ref string
	for _, d := range deps {
		if d.Delay() <= 0 {
			t.Fatalf("every journey is late with DelayRate 1: %+v", d)
		}
		if d.Line == "A" && d.Direction == "Hillerød" {
			ref = d.JourneyRef
		}
	}
	j, err := c.JourneyDetail(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	first := j.Stops[0].RealtimeDeparture.Sub(*j.Stops[0].ScheduledDeparture)
	last := j.Stops[len(j.Stops)-1].RealtimeArrival.Sub(*j.Stops[len(j.Stops)-1].ScheduledArrival)
	if first <= 0 || last < first {
		t.Fatalf("delay should ramp up: first %v last %v", first, last)
	}
	again, _ := c.JourneyDetail(context.Background(), ref)
	if again.Stops[1].RealtimeArrival.Sub(*again.Stops[1].ScheduledArrival) != j.Stops[1].RealtimeArrival.Sub(*j.Stops[1].ScheduledArrival) {
		t.Fatal("same seed must give the same delays")
	}
}

func TestJourneyDetailShape(t *testing.T) {
	c := setup(t, &simulator.Simulator{}, at(8, 13))
	deps, _ := c.DepartureBoard(context.Background(), "8600646") // Nørreport: A1 08:15
	if len(deps) != 1 {
		t.Fatalf("deps = %+v", deps)
	}
	j, err := c.JourneyDetail(context.Background(), deps[0].JourneyRef)
	if err != nil {
		t.Fatal(err)
	}
	if j.Line != "A" || j.Direction != "Hillerød" || len(j.Stops) != 4 || j.Key() == "" {
		t.Fatalf("journey = %+v", j)
	}
	if j.Stops[0].ScheduledArrival != nil || j.Stops[3].ScheduledDeparture != nil {
		t.Fatal("first stop has no arrival, last no departure")
	}
	if j.Stops[1].StopID != "8600626" {
		t.Fatalf("platform stops report their station id, got %s", j.Stops[1].StopID)
	}
	if j.Position == nil {
		t.Fatal("a running journey has a position")
	}
}

func TestCancelledJourneys(t *testing.T) {
	c := setup(t, &simulator.Simulator{CancelRate: 1}, at(8, 0))
	deps, _ := c.DepartureBoard(context.Background(), "8600626")
	if len(deps) == 0 || !deps[0].Cancelled || deps[0].Realtime != nil {
		t.Fatalf("deps = %+v", deps)
	}
	j, err := c.JourneyDetail(context.Background(), deps[0].JourneyRef)
	if err != nil {
		t.Fatal(err)
	}
	if !j.Cancelled || !j.Stops[1].Cancelled() || j.Position != nil {
		t.Fatalf("journey = %+v", j)
	}
}

func TestAlertsAndErrors(t *testing.T) {
	c := setup(t, &simulator.Simulator{}, at(8, 0))
	alerts, err := c.Alerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 || !alerts[0].Active || len(alerts[0].StopIDs) != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}
	if _, err := c.JourneyDetail(context.Background(), "2|#ZI#9999#DA#81026#"); err == nil {
		t.Fatal("unknown journey should fail")
	}
	if _, err := c.JourneyDetail(context.Background(), "garbage"); err == nil {
		t.Fatal("bad ref should fail")
	}
	c.AccessID = ""
	if _, err := c.DepartureBoard(context.Background(), "8600626"); err == nil {
		t.Fatal("missing accessId should fail")
	}
}

func TestUnknownEndpoint(t *testing.T) {
	s, _ := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture), gtfs.Filter{}, cph)
	srv := httptest.NewServer((&simulator.Simulator{Schedule: s}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/location.name?accessId=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
