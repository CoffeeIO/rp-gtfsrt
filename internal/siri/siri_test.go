package siri_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	"github.com/CoffeeIO/rp-gtfsrt/internal/siri"
)

func decodeAll(t *testing.T, doc string) []siri.Update {
	t.Helper()
	var out []siri.Update
	if err := siri.Decode(strings.NewReader(doc), func(u siri.Update) { out = append(out, u) }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDecodeLiveDelivery(t *testing.T) {
	doc, err := os.ReadFile("testdata/et_live_20261025.xml")
	if err != nil {
		t.Fatal(err)
	}
	us := decodeAll(t, string(doc))
	if len(us) != 2 {
		t.Fatalf("got %d journeys", len(us))
	}
	bus, train := us[0], us[1]
	if bus.Journey.Ref != "2026-10-25/23000013-8602266702000" || bus.Journey.Line != "230R" || bus.Journey.Key() != "ref:2026-10-25/23000013-8602266702000" {
		t.Fatalf("bus = %+v", bus.Journey)
	}
	if !bus.RecordedAt.Equal(time.Date(2026, 10, 25, 19, 24, 51, 0, time.UTC)) {
		t.Fatalf("recorded at %v", bus.RecordedAt)
	}
	s := bus.Journey.Stops[0]
	if s.StopID != "000000006560" || s.RealtimeDeparture.Sub(*s.ScheduledDeparture) != -3*time.Minute {
		t.Fatalf("bus stop = %+v", s)
	}
	s = train.Journey.Stops[0]
	if train.Journey.Line != "5800" || s.RealtimeArrival.Sub(*s.ScheduledArrival) != 8*time.Minute ||
		s.RealtimeDeparture.Sub(*s.ScheduledDeparture) != 7*time.Minute {
		t.Fatalf("train = %+v %+v", train.Journey, s)
	}
}

const envelope = `<Siri xmlns="http://www.siri.org.uk/siri" version="2.0"><ServiceDelivery><EstimatedTimetableDelivery><EstimatedJourneyVersionFrame>%s</EstimatedJourneyVersionFrame></EstimatedTimetableDelivery></ServiceDelivery></Siri>`

func doc(journeys ...string) string {
	return strings.Replace(envelope, "%s", strings.Join(journeys, ""), 1)
}

func call(stop, aimed, expected string, extra string) string {
	return `<EstimatedCall><StopPointRef>` + stop + `</StopPointRef>` +
		`<AimedDepartureTime>` + aimed + `</AimedDepartureTime>` +
		`<ExpectedDepartureTime>` + expected + `</ExpectedDepartureTime>` + extra + `</EstimatedCall>`
}

func journey(ref, recorded string, extra string, calls ...string) string {
	return `<EstimatedVehicleJourney><RecordedAtTime>` + recorded + `</RecordedAtTime><LineRef>A</LineRef>` +
		`<FramedVehicleJourneyRef><DataFrameRef>2026-10-25</DataFrameRef><DatedVehicleJourneyRef>` + ref + `</DatedVehicleJourneyRef></FramedVehicleJourneyRef>` +
		extra + `<EstimatedCalls>` + strings.Join(calls, "") + `</EstimatedCalls></EstimatedVehicleJourney>`
}

func TestDecodeRepeatedStopsKeepsLastAndSortsByPlannedTime(t *testing.T) {
	us := decodeAll(t, doc(journey("j1", "2026-10-25T08:00:00Z", "",
		call("2", "2026-10-25T09:10:00+01:00", "2026-10-25T09:11:00+01:00", ""),
		call("1", "2026-10-25T09:05:00+01:00", "2026-10-25T09:06:00+01:00", ""),
		call("2", "2026-10-25T09:10:00+01:00", "2026-10-25T09:13:00+01:00", ""),
	)))
	stops := us[0].Journey.Stops
	if len(stops) != 2 || stops[0].StopID != "1" || stops[1].StopID != "2" {
		t.Fatalf("stops = %+v", stops)
	}
	if d := stops[1].RealtimeDeparture.Sub(*stops[1].ScheduledDeparture); d != 3*time.Minute {
		t.Fatalf("last update should win, delay %v", d)
	}
}

func TestDecodeCancellationsAndMissingPredictions(t *testing.T) {
	us := decodeAll(t, doc(
		journey("whole", "", "<Cancellation>true</Cancellation>", call("1", "2026-10-25T09:05:00+01:00", "", "")),
		journey("part", "", "",
			call("1", "2026-10-25T09:05:00+01:00", "", ""),
			call("2", "2026-10-25T09:10:00+01:00", "", "<DepartureStatus>cancelled</DepartureStatus>"),
			call("3", "2026-10-25T09:15:00+01:00", "", "<Cancellation>true</Cancellation>"),
		),
		journey("", "", "", call("1", "2026-10-25T09:05:00+01:00", "", "")), // no ref: skipped
	))
	if len(us) != 2 {
		t.Fatalf("got %d journeys", len(us))
	}
	if !us[0].Journey.Cancelled || !us[0].RecordedAt.IsZero() {
		t.Fatalf("whole = %+v", us[0])
	}
	s := us[1].Journey.Stops
	if s[0].RealtimeDeparture != nil || s[0].DepartureCancelled {
		t.Fatalf("no prediction should stay nil: %+v", s[0])
	}
	if !s[1].DepartureCancelled || s[1].ArrivalCancelled || !s[2].Cancelled() {
		t.Fatalf("cancelled stops = %+v", s)
	}
}

func TestDecodeSingleMessage(t *testing.T) {
	us := decodeAll(t, journey("j1", "", "", call("1", "2026-10-25T09:05:00+01:00", "2026-10-25T09:05:00+01:00", "<Order>4</Order>")))
	if len(us) != 1 || us[0].Journey.Stops[0].Index != 4 {
		t.Fatalf("got %+v", us)
	}
}

func TestDecodeBadXML(t *testing.T) {
	if err := siri.Decode(strings.NewReader("<Siri><EstimatedVehicleJourney>"), func(siri.Update) {}); err == nil {
		t.Fatal("expected error")
	}
}

type fakeStore struct {
	mu       sync.Mutex
	observed []string
	pruned   int
}

func (f *fakeStore) ObserveJourney(j rejseplanen.Journey, _ time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, j.Ref)
}

func (f *fakeStore) Prune(time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned++
	return 0
}

func TestPollerPassesOnlyChangedJourneys(t *testing.T) {
	body := doc(
		journey("a", "2026-10-25T08:00:00Z", "", call("1", "2026-10-25T09:05:00+01:00", "2026-10-25T09:06:00+01:00", "")),
		journey("b", "2026-10-25T08:00:00Z", "", call("1", "2026-10-25T09:05:00+01:00", "2026-10-25T09:06:00+01:00", "")),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	store := &fakeStore{}
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	p := &siri.Poller{URL: srv.URL, Store: store, Now: func() time.Time { return now }}
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	body = strings.Replace(body, "<RecordedAtTime>2026-10-25T08:00:00Z</RecordedAtTime><LineRef>A</LineRef><FramedVehicleJourneyRef><DataFrameRef>2026-10-25</DataFrameRef><DatedVehicleJourneyRef>b",
		"<RecordedAtTime>2026-10-25T08:01:00Z</RecordedAtTime><LineRef>A</LineRef><FramedVehicleJourneyRef><DataFrameRef>2026-10-25</DataFrameRef><DatedVehicleJourneyRef>b", 1)
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-10-25/a", "2026-10-25/b", "2026-10-25/b"}
	if strings.Join(store.observed, " ") != strings.Join(want, " ") || store.pruned != 2 {
		t.Fatalf("observed %v, pruned %d", store.observed, store.pruned)
	}
	if st := p.Stats(); st.Polls != 2 || st.Journeys != 2 || st.Changed != 1 || st.Errors != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestPollerHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	p := &siri.Poller{URL: srv.URL, Store: &fakeStore{}}
	if err := p.Poll(context.Background()); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
	if st := p.Stats(); st.Errors != 1 || st.LastError == "" {
		t.Fatalf("stats = %+v", st)
	}
}
