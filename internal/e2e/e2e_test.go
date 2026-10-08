// Package e2e runs the whole pipeline: real HTTP client against a fake
// Rejseplanen API, real GTFS loading, matching, and the served protobuf feed.
package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/poller"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// The fake API uses 9-digit ids like the live one; the alias maps them to GTFS.
const board = `{"Departure":[
 {"name":"A","type":"S","stopExtId":"000008626","time":"08:12:00","date":"2026-10-08","rtTime":"08:16:00","rtDate":"2026-10-08","direction":"Hillerød St.","ProductAtStop":{"line":"A"}},
 {"name":"A","type":"S","stopExtId":"000008626","time":"08:12:00","date":"2026-10-08","rtTime":"08:12:00","direction":"Hundige St.","ProductAtStop":{"line":"A"}},
 {"name":"B","type":"S","stopExtId":"000008626","time":"08:12:00","date":"2026-10-08","direction":"Høje Taastrup St.","cancelled":true,"ProductAtStop":{"line":"B"}},
 {"name":"A","type":"S","stopExtId":"000008626","time":"00:30:00","date":"2026-10-09","rtTime":"00:34:00","rtDate":"2026-10-09","direction":"Hillerød St.","ProductAtStop":{"line":"A"}}
]}`

func TestPipeline(t *testing.T) {
	cph, _ := time.LoadLocation("Europe/Copenhagen")
	now := time.Date(2026, 10, 8, 8, 5, 0, 0, cph)

	var apiCalls int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls++
		if r.URL.Query().Get("accessId") != "test-key" || r.URL.Query().Get("id") != "000008626" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Write([]byte(board))
	}))
	defer api.Close()

	sched, err := gtfs.LoadZip(gtfstest.WriteFile(t, gtfstest.Fixture),
		gtfs.Filter{RouteShortNames: map[string]bool{"A": true, "B": true}}, cph)
	if err != nil {
		t.Fatal(err)
	}
	store := feed.NewStore()
	p := &poller.Poller{
		Source:      &rejseplanen.Client{BaseURL: api.URL, AccessID: "test-key", Products: 16, Location: cph},
		Schedule:    func() *gtfs.Schedule { return sched },
		StopAliases: map[string]string{"000008626": "8600626"},
		Store:       store,
		Budget:      &budget.Budget{Limit: 10, Location: cph},
		Hubs:        []string{"000008626"},
		Now:         func() time.Time { return now },
	}
	p.RunOnce(context.Background())
	if apiCalls != 1 {
		t.Fatalf("api calls = %d", apiCalls)
	}
	if st := p.Stats(); st.Outcomes["matched"] != 4 || st.Errors != 0 {
		t.Fatalf("stats = %+v", st)
	}

	srv := httptest.NewServer(feed.Handler(store, func() time.Time { return now }, nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/gtfs-rt/trip-updates")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var msg rt.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}

	got := map[string]*rt.TripUpdate{}
	for _, e := range msg.Entity {
		got[e.GetTripUpdate().GetTrip().GetTripId()] = e.GetTripUpdate()
	}
	if len(got) != 4 {
		t.Fatalf("entities = %v", got)
	}
	if d := got["A1"].StopTimeUpdate[0].GetDeparture().GetDelay(); d != 240 {
		t.Errorf("A1 delay = %d, want 240", d)
	}
	if d := got["A2"].StopTimeUpdate[0].GetDeparture().GetDelay(); d != 0 {
		t.Errorf("A2 delay = %d, want 0", d)
	}
	if got["B1"].GetTrip().GetScheduleRelationship() != rt.TripDescriptor_CANCELED {
		t.Errorf("B1 should be cancelled")
	}
	a3 := got["A3"]
	if a3.GetTrip().GetStartDate() != "20261008" {
		t.Errorf("night trip start_date = %s, want previous service day", a3.GetTrip().GetStartDate())
	}
	if d := a3.StopTimeUpdate[0].GetDeparture().GetDelay(); d != 240 {
		t.Errorf("A3 delay = %d", d)
	}

	// Budget exhausted after 10 calls: further rounds make no API calls.
	for i := 0; i < 15; i++ {
		p.RunOnce(context.Background())
	}
	if apiCalls != 10 {
		t.Fatalf("api calls = %d, want capped at 10", apiCalls)
	}
}
