package feed_test

import (
	"compress/gzip"
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
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(d, h, m int) time.Time  { return time.Date(2026, 10, d, h, m, 0, 0, cph) }
func tp(t time.Time) *time.Time { return &t }

func schedule(t *testing.T, files map[string]string) *gtfs.Schedule {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, files), gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const refB1 = "2|#ZI#100#DA#81026#"
const refA1 = "2|#ZI#200#DA#81026#"

func boardB1(delay time.Duration) rejseplanen.Departure {
	d := rejseplanen.Departure{StopID: "8600626", Line: "B", Scheduled: at(8, 8, 12), JourneyRef: refB1}
	if delay >= 0 {
		rtT := d.Scheduled.Add(delay)
		d.Realtime = &rtT
	}
	return d
}

// journeyA1 describes trip A1 as journeyDetail would, with a delay growing
// from 1 to 4 minutes.
func journeyA1() rejseplanen.Journey {
	stop := func(id string, idx int, arr, dep *time.Time, delay time.Duration) rejseplanen.JourneyStop {
		s := rejseplanen.JourneyStop{StopID: id, Index: idx, ScheduledArrival: arr, ScheduledDeparture: dep}
		if arr != nil {
			s.RealtimeArrival = tp(arr.Add(delay))
		}
		if dep != nil {
			s.RealtimeDeparture = tp(dep.Add(delay))
		}
		return s
	}
	return rejseplanen.Journey{Ref: refA1, Line: "A", Position: &rejseplanen.Position{Lat: 55.67, Lon: 12.56}, Stops: []rejseplanen.JourneyStop{
		stop("8600601", 0, nil, tp(at(8, 7, 50)), time.Minute),
		stop("8600626", 1, tp(at(8, 8, 11)), tp(at(8, 8, 12)), 2*time.Minute),
		stop("8600646", 2, tp(at(8, 8, 15)), tp(at(8, 8, 15)), 3*time.Minute),
		stop("8600669", 3, tp(at(8, 8, 50)), nil, 4*time.Minute),
	}}
}

func entity(t *testing.T, msg *rt.FeedMessage, id string) *rt.FeedEntity {
	t.Helper()
	for _, e := range msg.Entity {
		if e.GetId() == id {
			return e
		}
	}
	t.Fatalf("no entity %s in %v", id, msg.Entity)
	return nil
}

func TestBoardRowBecomesTripUpdate(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	store.ObserveDeparture(boardB1(3*time.Minute), at(8, 8, 0))
	snap := store.Render(s, at(8, 8, 1))

	h := snap.TripUpdates.GetHeader()
	if h.GetGtfsRealtimeVersion() != "2.0" || h.GetIncrementality() != rt.FeedHeader_FULL_DATASET || h.GetTimestamp() != uint64(at(8, 8, 1).Unix()) {
		t.Fatalf("header = %v", h)
	}
	tu := entity(t, snap.TripUpdates, "100@81026").GetTripUpdate()
	trip := tu.GetTrip()
	if trip.GetTripId() != "B1" || trip.GetRouteId() != "rB" || trip.GetStartDate() != "20261008" || trip.GetScheduleRelationship() != rt.TripDescriptor_SCHEDULED {
		t.Fatalf("trip = %v", trip)
	}
	if tu.GetTimestamp() != uint64(at(8, 8, 0).Unix()) || len(tu.StopTimeUpdate) != 1 {
		t.Fatalf("trip update = %v", tu)
	}
	stu := tu.StopTimeUpdate[0]
	if stu.GetStopSequence() != 1 || stu.GetStopId() != "8600626-1" || stu.ScheduleRelationship == nil ||
		stu.GetScheduleRelationship() != rt.TripUpdate_StopTimeUpdate_SCHEDULED {
		t.Fatalf("stop update = %v", stu)
	}
	if stu.GetDeparture().GetDelay() != 180 || stu.GetDeparture().GetTime() != at(8, 8, 15).Unix() || stu.Arrival != nil {
		t.Fatalf("departure = %v", stu.GetDeparture())
	}
	if snap.Stats.Outcomes["matched"] != 1 || snap.Stats.TripUpdates != 1 || snap.Stats.VehiclePositions != 0 {
		t.Fatalf("stats = %+v", snap.Stats)
	}
}

func TestBoardRowWithoutPredictionIsTrackedButNotPublished(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	store.ObserveDeparture(boardB1(-1), at(8, 8, 0))
	snap := store.Render(s, at(8, 8, 1))
	if len(snap.TripUpdates.Entity) != 0 || snap.Stats.Outcomes["matched"] != 1 {
		t.Fatalf("snap = %+v", snap.Stats)
	}
	tr := store.Tracked()
	if len(tr) != 1 || tr[0].Ref != refB1 || tr[0].HasJourney || tr[0].Cancelled || !tr[0].Departure.Equal(at(8, 8, 12)) {
		t.Fatalf("tracked = %+v", tr)
	}
}

func TestCancelledBoardRowSkipsThatStop(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	d := boardB1(-1)
	d.Cancelled = true
	store.ObserveDeparture(d, at(8, 8, 0))
	tu := entity(t, store.Render(s, at(8, 8, 1)).TripUpdates, "100@81026").GetTripUpdate()
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_SCHEDULED {
		t.Fatal("one cancelled departure must not cancel the whole trip")
	}
	stu := tu.StopTimeUpdate[0]
	if stu.GetScheduleRelationship() != rt.TripUpdate_StopTimeUpdate_SKIPPED || stu.Departure != nil || stu.Arrival != nil {
		t.Fatalf("stop = %v", stu)
	}
	if tr := store.Tracked(); !tr[0].Cancelled {
		t.Fatal("tracked journey should be flagged cancelled")
	}
}

func TestJourneyDetailBecomesFullTripUpdate(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	store.ObserveJourney(journeyA1(), at(8, 8, 0))
	snap := store.Render(s, at(8, 8, 1))
	tu := entity(t, snap.TripUpdates, "200@81026").GetTripUpdate()
	if tu.GetTrip().GetTripId() != "A1" || len(tu.StopTimeUpdate) != 4 {
		t.Fatalf("trip update = %v", tu)
	}
	for i, stu := range tu.StopTimeUpdate {
		if stu.GetStopSequence() != uint32(i+1) {
			t.Fatalf("sequence %d = %d", i, stu.GetStopSequence())
		}
	}
	first, last := tu.StopTimeUpdate[0], tu.StopTimeUpdate[3]
	if first.Arrival != nil || first.GetDeparture().GetDelay() != 60 {
		t.Fatalf("first = %v", first)
	}
	if last.Departure != nil || last.GetArrival().GetDelay() != 240 || last.GetArrival().GetTime() != at(8, 8, 54).Unix() {
		t.Fatalf("last = %v", last)
	}
	if tu.StopTimeUpdate[2].GetStopId() != "8600646" {
		t.Fatalf("stop id = %s", tu.StopTimeUpdate[2].GetStopId())
	}
	vp := entity(t, snap.VehiclePositions, "200@81026").GetVehicle()
	if vp.GetTrip().GetTripId() != "A1" || vp.GetPosition().GetLatitude() != float32(55.67) || vp.GetTimestamp() != uint64(at(8, 8, 0).Unix()) {
		t.Fatalf("vehicle = %v", vp)
	}
}

func TestStopTimesThatDoNotIncreaseAreLeftOut(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	j := journeyA1()
	// Nørreport predicted at 8:13, before København H's departure at 8:14.
	j.Stops[2].RealtimeArrival, j.Stops[2].RealtimeDeparture = tp(at(8, 8, 13)), tp(at(8, 8, 13))
	store.ObserveJourney(j, at(8, 8, 0))
	tu := entity(t, store.Render(s, at(8, 8, 1)).TripUpdates, "200@81026").GetTripUpdate()
	var stops []string
	for _, stu := range tu.StopTimeUpdate {
		stops = append(stops, stu.GetStopId())
	}
	if strings.Join(stops, ",") != "8600601,8600626-1,8600669" {
		t.Fatalf("stops = %v", stops)
	}
}

func TestJourneyCancellations(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)

	// Partial: the last stop's arrival is cancelled (train terminates early).
	partial := journeyA1()
	partial.Stops[3].ArrivalCancelled = true
	store := feed.NewStore()
	store.ObserveJourney(partial, at(8, 8, 0))
	tu := entity(t, store.Render(s, at(8, 8, 1)).TripUpdates, "200@81026").GetTripUpdate()
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_SCHEDULED ||
		tu.StopTimeUpdate[3].GetScheduleRelationship() != rt.TripUpdate_StopTimeUpdate_SKIPPED {
		t.Fatalf("partial = %v", tu)
	}

	// Whole journey flagged cancelled.
	whole := journeyA1()
	whole.Cancelled = true
	store = feed.NewStore()
	store.ObserveJourney(whole, at(8, 8, 0))
	snap := store.Render(s, at(8, 8, 1))
	tu = entity(t, snap.TripUpdates, "200@81026").GetTripUpdate()
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_CANCELED || len(tu.StopTimeUpdate) != 0 {
		t.Fatalf("cancelled = %v", tu)
	}
	if len(snap.VehiclePositions.Entity) != 0 || snap.Stats.Cancelled != 1 {
		t.Fatalf("cancelled trip has no vehicle; stats = %+v", snap.Stats)
	}

	// Every stop cancelled counts as a cancelled trip.
	all := journeyA1()
	all.Position = nil
	for i := range all.Stops {
		all.Stops[i].ArrivalCancelled, all.Stops[i].DepartureCancelled = true, true
	}
	store = feed.NewStore()
	store.ObserveJourney(all, at(8, 8, 0))
	tu = entity(t, store.Render(s, at(8, 8, 1)).TripUpdates, "200@81026").GetTripUpdate()
	if tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_CANCELED {
		t.Fatalf("all stops cancelled = %v", tu)
	}
}

func TestNewerBoardRowRefinesJourney(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	// Older board row is ignored once journey detail arrives...
	old := rejseplanen.Departure{StopID: "8600646", Line: "A", Scheduled: at(8, 8, 15), JourneyRef: refA1, Realtime: tp(at(8, 8, 25))}
	store.ObserveDeparture(old, at(8, 7, 55))
	store.ObserveJourney(journeyA1(), at(8, 8, 0))
	stus := entity(t, store.Render(s, at(8, 8, 1)).TripUpdates, "200@81026").GetTripUpdate().StopTimeUpdate
	if stus[2].GetDeparture().GetDelay() != 180 {
		t.Fatalf("older board row should not override: %v", stus[2])
	}
	// ...but a newer one replaces that stop's departure.
	store.ObserveDeparture(rejseplanen.Departure{StopID: "8600646", Line: "A", Scheduled: at(8, 8, 15), JourneyRef: refA1, Realtime: tp(at(8, 8, 20))}, at(8, 8, 2))
	stus = entity(t, store.Render(s, at(8, 8, 3)).TripUpdates, "200@81026").GetTripUpdate().StopTimeUpdate
	if stus[2].GetDeparture().GetDelay() != 300 || stus[2].GetArrival().GetDelay() != 180 {
		t.Fatalf("newer board row should refine departure only: %v", stus[2])
	}
}

func TestUnmatchedAndExpiry(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	store.ObserveDeparture(rejseplanen.Departure{StopID: "999", Line: "X", Scheduled: at(8, 8, 0), JourneyRef: "2|#ZI#9#DA#81026#", Realtime: tp(at(8, 8, 1))}, at(8, 8, 0))
	store.ObserveDeparture(boardB1(5*time.Minute), at(8, 8, 0))
	snap := store.Render(s, at(8, 8, 1))
	if snap.Stats.Outcomes["no_candidate"] != 1 || snap.Stats.Outcomes["matched"] != 1 || snap.Stats.Journeys != 2 {
		t.Fatalf("stats = %+v", snap.Stats)
	}
	// B1 ends 08:40; +5 min delay +30 min grace → kept through 09:15.
	if store.Prune(at(8, 9, 15)) != 0 || store.Len() != 2 {
		t.Fatal("pruned too early")
	}
	if store.Prune(at(8, 9, 16)) != 1 {
		t.Fatal("B1 should expire after 09:15")
	}
	// Unmatched entries are kept for KeepUnmatched after their last update.
	if store.Prune(at(8, 8, 0).Add(feed.KeepUnmatched).Add(time.Second)) != 1 || store.Len() != 0 {
		t.Fatal("unmatched entry should expire")
	}
}

func TestReloadRematchesWithNewTripIDs(t *testing.T) {
	s1 := schedule(t, gtfstest.Fixture)
	renamed := gtfstest.With(map[string]string{
		"trips.txt":      strings.ReplaceAll(gtfstest.Fixture["trips.txt"], "B1", "B1-v2"),
		"stop_times.txt": strings.ReplaceAll(gtfstest.Fixture["stop_times.txt"], "B1,", "B1-v2,"),
	})
	s2 := schedule(t, renamed)
	store := feed.NewStore()
	store.ObserveDeparture(boardB1(time.Minute), at(8, 8, 0))
	if id := entity(t, store.Render(s1, at(8, 8, 1)).TripUpdates, "100@81026").GetTripUpdate().GetTrip().GetTripId(); id != "B1" {
		t.Fatalf("before reload = %s", id)
	}
	if id := entity(t, store.Render(s2, at(8, 8, 2)).TripUpdates, "100@81026").GetTripUpdate().GetTrip().GetTripId(); id != "B1-v2" {
		t.Fatalf("after reload = %s", id)
	}
}

func TestDeterministicOrder(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := feed.NewStore()
	store.ObserveJourney(journeyA1(), at(8, 8, 0))
	store.ObserveDeparture(boardB1(0), at(8, 8, 0))
	var ids []string
	for _, e := range store.Render(s, at(8, 8, 1)).TripUpdates.Entity {
		ids = append(ids, e.GetId())
	}
	if strings.Join(ids, ",") != "100@81026,200@81026" {
		t.Fatalf("order = %v", ids)
	}
}

func alertsFixture() []rejseplanen.Alert {
	return []rejseplanen.Alert{
		{ID: "a1", Active: true, Header: "Sporarbejde", Text: "Busser i stedet for tog", StopIDs: []string{"8600626", "8600646"},
			Start: at(8, 6, 0), End: at(9, 6, 0)},
		{ID: "a2", Active: false, Header: "old", StopIDs: []string{"8600626"}},
		{ID: "a3", Active: true, Header: "expired", StopIDs: []string{"8600626"}, End: at(7, 0, 0)},
		{ID: "a4", Active: true, Header: "unknown stop", StopIDs: []string{"999"}},
		{ID: "a5", Active: true, Header: "platform only", StopIDs: []string{"8600626-1"}},
	}
}

func TestAlertsRender(t *testing.T) {
	s := schedule(t, gtfstest.Fixture)
	store := &feed.AlertStore{}
	store.Set(alertsFixture(), at(8, 8, 0))
	if !store.FetchedAt().Equal(at(8, 8, 0)) {
		t.Fatal("FetchedAt")
	}
	msg, stats := store.Render(s, at(8, 8, 1))
	if stats != (feed.AlertStats{Received: 5, Published: 2, Inactive: 2, NoEntities: 1}) {
		t.Fatalf("stats = %+v", stats)
	}
	a := entity(t, msg, "a1").GetAlert()
	var stops []string
	for _, ie := range a.InformedEntity {
		stops = append(stops, ie.GetStopId())
	}
	if strings.Join(stops, ",") != "8600626,8600646" {
		t.Fatalf("informed stops = %v (station id, not its platforms)", stops)
	}
	tr := a.GetHeaderText().GetTranslation()[0]
	if tr.GetText() != "Sporarbejde" || tr.GetLanguage() != "da" || a.GetDescriptionText().GetTranslation()[0].GetText() != "Busser i stedet for tog" {
		t.Fatalf("texts = %v", a)
	}
	if p := a.ActivePeriod[0]; p.GetStart() != uint64(at(8, 6, 0).Unix()) || p.GetEnd() != uint64(at(9, 6, 0).Unix()) {
		t.Fatalf("period = %v", p)
	}
	if ie := entity(t, msg, "a5").GetAlert().InformedEntity; len(ie) != 1 || ie[0].GetStopId() != "8600626-1" {
		t.Fatalf("platform = %v", ie)
	}
}

func newServer(t *testing.T, store *feed.Store, now *time.Time) (*feed.Server, *httptest.Server) {
	t.Helper()
	s := schedule(t, gtfstest.Fixture)
	alerts := &feed.AlertStore{}
	alerts.Set(alertsFixture(), *now)
	fs := &feed.Server{
		Store: store, Alerts: alerts, Schedule: func() *gtfs.Schedule { return s },
		Now: func() time.Time { return *now }, MaxAge: 5 * time.Second, Metrics: metrics.New(),
		Status: func() map[string]any { return map[string]any{"mode": "test"} },
	}
	srv := httptest.NewServer(fs.Handler())
	t.Cleanup(srv.Close)
	return fs, srv
}

func get(t *testing.T, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultTransport.RoundTrip(req) // no transparent gzip
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, r io.Reader) *rt.FeedMessage {
	t.Helper()
	body, _ := io.ReadAll(r)
	var msg rt.FeedMessage
	if err := proto.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	return &msg
}

func TestServerFeeds(t *testing.T) {
	store := feed.NewStore()
	store.ObserveJourney(journeyA1(), at(8, 8, 0))
	now := at(8, 8, 1)
	_, srv := newServer(t, store, &now)

	for path, want := range map[string]int{"trip-updates": 1, "vehicle-positions": 1, "alerts": 2} {
		resp := get(t, srv.URL+"/gtfs-rt/"+path, nil)
		if resp.Header.Get("Content-Type") != "application/x-protobuf" || resp.Header.Get("ETag") == "" {
			t.Fatalf("%s headers = %v", path, resp.Header)
		}
		if msg := decode(t, resp.Body); len(msg.Entity) != want {
			t.Fatalf("%s entities = %d, want %d", path, len(msg.Entity), want)
		}
		resp.Body.Close()
	}
}

func TestServerGzipETagAndJSON(t *testing.T) {
	store := feed.NewStore()
	store.ObserveDeparture(boardB1(time.Minute), at(8, 8, 0))
	now := at(8, 8, 1)
	_, srv := newServer(t, store, &now)

	resp := get(t, srv.URL+"/gtfs-rt/trip-updates", map[string]string{"Accept-Encoding": "gzip"})
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("expected gzip")
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if msg := decode(t, zr); len(msg.Entity) != 1 {
		t.Fatalf("gzip body entities = %d", len(msg.Entity))
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	resp = get(t, srv.URL+"/gtfs-rt/trip-updates", map[string]string{"If-None-Match": etag})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match status = %d", resp.StatusCode)
	}

	resp = get(t, srv.URL+"/gtfs-rt/trip-updates?format=json", nil)
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if header, _ := body["header"].(map[string]any); header["gtfsRealtimeVersion"] != "2.0" {
		t.Fatalf("json = %v", body)
	}
}

func TestServerCachesRendering(t *testing.T) {
	store := feed.NewStore()
	now := at(8, 8, 1)
	_, srv := newServer(t, store, &now)
	count := func() int {
		resp := get(t, srv.URL+"/gtfs-rt/trip-updates", nil)
		defer resp.Body.Close()
		return len(decode(t, resp.Body).Entity)
	}
	if count() != 0 {
		t.Fatal("empty store")
	}
	store.ObserveDeparture(boardB1(time.Minute), at(8, 8, 1))
	if count() != 0 {
		t.Fatal("within MaxAge the cached rendering is served")
	}
	now = now.Add(6 * time.Second)
	if count() != 1 {
		t.Fatal("after MaxAge the store is re-rendered")
	}
}

func TestServerHealthMetricsAndRoutes(t *testing.T) {
	store := feed.NewStore()
	store.ObserveDeparture(boardB1(time.Minute), at(8, 8, 0))
	now := at(8, 8, 1)
	_, srv := newServer(t, store, &now)

	resp := get(t, srv.URL+"/healthz", nil)
	var health map[string]any
	json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	journeys, _ := health["journeys"].(map[string]any)
	if health["ok"] != true || health["mode"] != "test" || journeys["trip_updates"] != float64(1) {
		t.Fatalf("healthz = %v", health)
	}

	get(t, srv.URL+"/gtfs-rt/alerts", nil).Body.Close()
	resp = get(t, srv.URL+"/metrics", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`rpgtfsrt_feed_entities{feed="trip-updates"} 1`,
		`rpgtfsrt_journeys_by_outcome{outcome="matched"} 1`,
		`rpgtfsrt_feed_requests_total{feed="alerts"} 1`,
		`rpgtfsrt_alerts_unresolved 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics missing %q", want)
		}
	}

	resp = get(t, srv.URL+"/", nil)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "/gtfs-rt/vehicle-positions") {
		t.Fatalf("index = %s", body)
	}
	for path, status := range map[string]int{"/nope": http.StatusNotFound} {
		resp := get(t, srv.URL+path, nil)
		resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("%s = %d", path, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/gtfs-rt/trip-updates", nil)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", resp.StatusCode)
	}
}
