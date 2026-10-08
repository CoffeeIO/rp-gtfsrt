package feed_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, cph) }

func schedule(t *testing.T) *gtfs.Schedule {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture),
		gtfs.Filter{RouteShortNames: map[string]bool{"A": true, "B": true}}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func match(t *testing.T, s *gtfs.Schedule, stop, line string, when time.Time, trip string) gtfs.Match {
	t.Helper()
	for _, m := range s.Candidates(stop, line, when) {
		if m.Trip.ID == trip {
			return m
		}
	}
	t.Fatalf("no candidate %s at %s %v", trip, stop, when)
	return gtfs.Match{}
}

func delayed(stop string, sched time.Time, delay time.Duration) rejseplanen.Departure {
	rtTime := sched.Add(delay)
	return rejseplanen.Departure{StopID: stop, Scheduled: sched, Realtime: &rtTime}
}

func TestObserveBuildsTripUpdate(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	now := at(8, 8, 10)
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), delayed("8600626", at(8, 8, 12), 3*time.Minute), now)

	msg := store.Message(now)
	if msg.GetHeader().GetGtfsRealtimeVersion() != "2.0" || msg.GetHeader().GetIncrementality() != rt.FeedHeader_FULL_DATASET {
		t.Fatalf("header = %v", msg.GetHeader())
	}
	if msg.GetHeader().GetTimestamp() != uint64(now.Unix()) {
		t.Fatal("header timestamp")
	}
	if len(msg.Entity) != 1 {
		t.Fatalf("entities = %d", len(msg.Entity))
	}
	e := msg.Entity[0]
	if e.GetId() != "B1|20261008" {
		t.Fatalf("id = %s", e.GetId())
	}
	tu := e.GetTripUpdate()
	if tu.GetTrip().GetTripId() != "B1" || tu.GetTrip().GetRouteId() != "rB" || tu.GetTrip().GetStartDate() != "20261008" {
		t.Fatalf("trip = %v", tu.GetTrip())
	}
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_SCHEDULED {
		t.Fatal("should be SCHEDULED")
	}
	if len(tu.StopTimeUpdate) != 1 {
		t.Fatalf("stop updates = %d", len(tu.StopTimeUpdate))
	}
	stu := tu.StopTimeUpdate[0]
	if stu.ScheduleRelationship == nil || stu.GetScheduleRelationship() != rt.TripUpdate_StopTimeUpdate_SCHEDULED {
		t.Fatal("stop_time_update schedule_relationship must be set explicitly (validator W009)")
	}
	if stu.GetStopSequence() != 1 || stu.GetStopId() != "8600626-1" {
		t.Fatalf("stop update = %v", stu)
	}
	if stu.GetDeparture().GetDelay() != 180 || stu.GetDeparture().GetTime() != at(8, 8, 15).Unix() {
		t.Fatalf("departure = %v", stu.GetDeparture())
	}
	if tu.GetTimestamp() != uint64(now.Unix()) {
		t.Fatal("trip update timestamp")
	}
}

func TestEarlyDepartureNegativeDelay(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), delayed("8600626", at(8, 8, 12), -time.Minute), at(8, 8, 0))
	if d := store.Message(at(8, 8, 0)).Entity[0].GetTripUpdate().StopTimeUpdate[0].GetDeparture().GetDelay(); d != -60 {
		t.Fatalf("delay = %d", d)
	}
}

func TestObserveMergesStopsInSequenceOrder(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	// Nørreport (seq 3) observed before København H (seq 2).
	store.Observe(s, match(t, s, "8600646", "A", at(8, 8, 15), "A1"), delayed("8600646", at(8, 8, 15).Add(30*time.Second), 2*time.Minute), at(8, 8, 0))
	store.Observe(s, match(t, s, "8600626", "A", at(8, 8, 12), "A1"), delayed("8600626", at(8, 8, 12), time.Minute), at(8, 8, 1))
	// Re-observing the same stop replaces it.
	store.Observe(s, match(t, s, "8600626", "A", at(8, 8, 12), "A1"), delayed("8600626", at(8, 8, 12), 90*time.Second), at(8, 8, 2))

	stus := store.Message(at(8, 8, 2)).Entity[0].GetTripUpdate().StopTimeUpdate
	if len(stus) != 2 {
		t.Fatalf("stop updates = %d", len(stus))
	}
	if stus[0].GetStopSequence() != 2 || stus[0].GetDeparture().GetDelay() != 90 {
		t.Fatalf("first = %v", stus[0])
	}
	if stus[1].GetStopSequence() != 3 || stus[1].GetDeparture().GetDelay() != 120 {
		t.Fatalf("second = %v", stus[1])
	}
}

func TestCancelledTrip(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	d := rejseplanen.Departure{StopID: "8600626", Scheduled: at(8, 8, 12), Cancelled: true}
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), d, at(8, 8, 0))
	tu := store.Message(at(8, 8, 0)).Entity[0].GetTripUpdate()
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_CANCELED || len(tu.StopTimeUpdate) != 0 {
		t.Fatalf("cancelled trip = %v", tu)
	}
}

func TestReinstatedTripCarriesFreshStop(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	m := match(t, s, "8600626", "B", at(8, 8, 12), "B1")
	store.Observe(s, m, rejseplanen.Departure{Scheduled: at(8, 8, 12), Cancelled: true}, at(8, 8, 0))
	// A later non-cancelled observation reinstates the trip with its stop.
	store.Observe(s, m, rejseplanen.Departure{Scheduled: at(8, 8, 12), Cancelled: false}, at(8, 8, 1))
	msg := store.Message(at(8, 8, 1))
	if len(msg.Entity) != 1 || len(msg.Entity[0].GetTripUpdate().StopTimeUpdate) != 1 {
		t.Fatalf("reinstated trip should carry its fresh stop: %v", msg.Entity)
	}
}

func TestPrune(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	// B1 ends 08:40; with 5 min delay and 30 min grace it expires after 09:15.
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), delayed("8600626", at(8, 8, 12), 5*time.Minute), at(8, 8, 0))
	if n := store.Prune(at(8, 9, 15)); n != 0 || store.Len() != 1 {
		t.Fatalf("pruned too early: %d", n)
	}
	if n := store.Prune(at(8, 9, 16)); n != 1 || store.Len() != 0 {
		t.Fatalf("not pruned: %d", n)
	}
}

func TestMessageDeterministicOrder(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), delayed("8600626", at(8, 8, 12), 0), at(8, 8, 0))
	store.Observe(s, match(t, s, "8600626", "A", at(8, 8, 12), "A2"), delayed("8600626", at(8, 8, 12), 0), at(8, 8, 0))
	store.Observe(s, match(t, s, "8600626", "A", at(8, 8, 12), "A1"), delayed("8600626", at(8, 8, 12), 0), at(8, 8, 0))
	var ids []string
	for _, e := range store.Message(at(8, 8, 0)).Entity {
		ids = append(ids, e.GetId())
	}
	if strings.Join(ids, ",") != "A1|20261008,A2|20261008,B1|20261008" {
		t.Fatalf("order = %v", ids)
	}
}

func TestEmptyFeed(t *testing.T) {
	msg := feed.NewStore().Message(at(8, 8, 0))
	if len(msg.Entity) != 0 || msg.GetHeader() == nil {
		t.Fatalf("msg = %v", msg)
	}
}

func TestHandlerProtobuf(t *testing.T) {
	s := schedule(t)
	store := feed.NewStore()
	store.Observe(s, match(t, s, "8600626", "B", at(8, 8, 12), "B1"), delayed("8600626", at(8, 8, 12), time.Minute), at(8, 8, 0))
	srv := httptest.NewServer(feed.Handler(store, func() time.Time { return at(8, 8, 0) }, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/gtfs-rt/trip-updates")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("content type = %s", resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	var msg rt.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.Entity) != 1 || msg.Entity[0].GetTripUpdate().GetTrip().GetTripId() != "B1" {
		t.Fatalf("decoded = %v", &msg)
	}
}

func TestHandlerJSON(t *testing.T) {
	srv := httptest.NewServer(feed.Handler(feed.NewStore(), func() time.Time { return at(8, 8, 0) }, nil))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/gtfs-rt/trip-updates?format=json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	header, _ := body["header"].(map[string]any)
	if header["gtfsRealtimeVersion"] != "2.0" {
		t.Fatalf("json = %v", body)
	}
}

func TestHandlerHealthAndMethods(t *testing.T) {
	status := func() map[string]any { return map[string]any{"budget_used": 7} }
	srv := httptest.NewServer(feed.Handler(feed.NewStore(), time.Now, status))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if body["ok"] != true || body["entities"] != float64(0) || body["budget_used"] != float64(7) {
		t.Fatalf("healthz = %v", body)
	}

	resp, _ = http.Post(srv.URL+"/gtfs-rt/trip-updates", "text/plain", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	resp, _ = http.Get(srv.URL + "/nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown path status = %d", resp.StatusCode)
	}
}
