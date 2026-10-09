package rejseplanen_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

func at9(h, m int) time.Time { return time.Date(2026, 10, 9, h, m, 0, 0, cph) }

// Captured from the live API: S-tog B Høje Taastrup → Farum, 2026-10-09.
func TestParseLiveJourneyDetail(t *testing.T) {
	j, err := rejseplanen.ParseJourneyDetail(fixture(t, "journeydetail_live_20261009.json"), cph)
	if err != nil {
		t.Fatal(err)
	}
	if j.Line != "B" || j.Direction != "Farum St." || j.Cancelled {
		t.Fatalf("journey = %+v", j)
	}
	if len(j.Stops) != 29 {
		t.Fatalf("stops = %d", len(j.Stops))
	}
	first, last := j.Stops[0], j.Stops[len(j.Stops)-1]
	if first.StopID != "8600798" || first.ScheduledArrival != nil || first.ScheduledDeparture == nil || !first.ScheduledDeparture.Equal(at9(9, 50)) {
		t.Fatalf("first = %+v", first)
	}
	if first.RealtimeDeparture == nil || !first.RealtimeDeparture.Equal(at9(9, 50)) {
		t.Fatal("PROGNOSED without rtDepTime means on time")
	}
	if last.ScheduledDeparture != nil || last.ScheduledArrival == nil {
		t.Fatalf("last = %+v", last)
	}
	rodovre := j.Stops[5]
	if rodovre.StopID != "8600680" || rodovre.Index != 5 || rodovre.Track != "3" || rodovre.Cancelled() {
		t.Fatalf("Rødovre = %+v", rodovre)
	}
	if j.Position == nil || j.Position.Lat == 0 {
		t.Fatalf("position = %v", j.Position)
	}
	if j.Key() != "58845@91026" {
		t.Fatalf("key = %s", j.Key())
	}
}

func TestParseJourneyRealtimeAndCancellations(t *testing.T) {
	body := `{"ref":"2|#ZI#1#DA#91026#","dayOfOperation":"2026-10-09","cancelled":false,
	 "Product":{"line":"C"},
	 "Directions":{"Direction":{"value":"Ballerup St."}},
	 "Stops":{"Stop":[
	  {"extId":"1","routeIdx":0,"depTime":"23:58:00","depPrognosisType":"PROGNOSED","rtDepTime":"23:59:00"},
	  {"extId":"2","routeIdx":1,"arrTime":"00:03:00","arrDate":"2026-10-10","rtArrTime":"00:05:00","rtArrDate":"2026-10-10","depTime":"00:03:00","depDate":"2026-10-10","cancelledDeparture":true},
	  {"extId":"3","routeIdx":2,"arrTime":"00:10:00","arrDate":"2026-10-10","cancelledArrival":true}
	 ]}}`
	j, err := rejseplanen.ParseJourneyDetail([]byte(body), cph)
	if err != nil {
		t.Fatal(err)
	}
	if j.Line != "C" || j.Direction != "Ballerup St." || j.Position != nil {
		t.Fatalf("journey = %+v", j)
	}
	s0, s1, s2 := j.Stops[0], j.Stops[1], j.Stops[2]
	if !s0.ScheduledDeparture.Equal(at9(23, 58)) || s0.RealtimeDeparture.Sub(*s0.ScheduledDeparture) != time.Minute {
		t.Fatalf("first stop uses dayOfOperation: %+v", s0)
	}
	if s1.RealtimeArrival.Sub(*s1.ScheduledArrival) != 2*time.Minute {
		t.Fatalf("arrival delay = %v", s1.RealtimeArrival.Sub(*s1.ScheduledArrival))
	}
	if s1.RealtimeDeparture != nil || !s1.DepartureCancelled || s1.Cancelled() {
		t.Fatalf("departure-only cancellation = %+v", s1)
	}
	if !s2.Cancelled() {
		t.Fatalf("last stop with cancelled arrival should be cancelled: %+v", s2)
	}
}

func TestParseJourneyErrors(t *testing.T) {
	cases := map[string]string{
		`nope`: "decode journey",
		`{"errorCode":"SVC_PARAM","errorText":"x"}`: "SVC_PARAM",
		`{"ref":"x"}`:            "no stops",
		`{"Stops":{"Stop":[]}}`:  "no stops",
		`{"Stops":{"Stop":"x"}}`: "decode journey stops",
		`{"Stops":{"Stop":[{"extId":"1","depTime":"xx"}]},"dayOfOperation":"2026-10-09"}`: "bad date/time",
	}
	for body, want := range cases {
		_, err := rejseplanen.ParseJourneyDetail([]byte(body), cph)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}

func TestKeyFromRef(t *testing.T) {
	cases := map[string]string{
		"2|#VN#1#ST#1791273300#PI#0#ZI#58845#TA#0#DA#91026#1S#8600798#": "58845@91026",
		"2|#ZI#1#":    "",
		"no-hashes":   "",
		"":            "",
		"#DA#1#ZI#2#": "2@1",
	}
	for ref, want := range cases {
		if got := rejseplanen.KeyFromRef(ref); got != want {
			t.Errorf("KeyFromRef(%q) = %q, want %q", ref, got, want)
		}
	}
	if k := (rejseplanen.Journey{Ref: "odd"}).Key(); k != "ref:odd" {
		t.Errorf("fallback key = %q", k)
	}
}

func TestDepartureJourneyKey(t *testing.T) {
	deps, err := rejseplanen.ParseDepartureBoard(fixture(t, "departureboard_live_20261008.json"), "8600626", cph)
	if err != nil {
		t.Fatal(err)
	}
	if deps[0].JourneyRef == "" || !strings.Contains(deps[0].JourneyKey(), "@") {
		t.Fatalf("first departure ref=%q key=%q", deps[0].JourneyRef, deps[0].JourneyKey())
	}
	noRef := rejseplanen.Departure{StopID: "1", Line: "A", Scheduled: at9(8, 0)}
	if !strings.HasPrefix(noRef.JourneyKey(), "dep:1|A|") {
		t.Fatalf("fallback key = %s", noRef.JourneyKey())
	}
}

func TestParseLiveAlerts(t *testing.T) {
	alerts, err := rejseplanen.ParseAlerts(fixture(t, "himsearch_live_20261009.json"), cph)
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 6 {
		t.Fatalf("alerts = %d", len(alerts))
	}
	a := alerts[0]
	if a.ID != "ROI_HIM_FREETEXT_6010000540557729" || !a.Active || a.Products != 32 || a.Company != "Movia H" {
		t.Fatalf("first = %+v", a)
	}
	if !strings.HasPrefix(a.Header, "Linje 2A") || !strings.Contains(a.Text, "Tingbjerg") {
		t.Fatalf("texts = %q / %q", a.Header, a.Text)
	}
	if !a.Start.Equal(time.Date(2024, 12, 16, 10, 48, 25, 0, cph)) || !a.End.Equal(time.Date(2027, 9, 30, 5, 0, 0, 0, cph)) {
		t.Fatalf("period = %v – %v", a.Start, a.End)
	}
	if len(a.StopIDs) == 0 || a.StopIDs[0] != "2027" {
		t.Fatalf("stops = %v", a.StopIDs)
	}
	if last := alerts[5]; len(last.StopIDs) != 0 {
		t.Fatalf("alert without stops = %v", last.StopIDs)
	}
}

func TestParseAlertsEdgeCases(t *testing.T) {
	body := `{"Message":{"id":"x","act":"true","head":"h","lead":"lead text",
	  "sDate":"2026-10-09","eDate":"2026-10-10",
	  "affectedStops":{"StopLocation":{"id":"A=1@O=X@L=8600626@"}},
	  "validFromStop":{"extId":"8600626"},"validToStop":{"extId":"8600646"}}}`
	alerts, err := rejseplanen.ParseAlerts([]byte(body), cph)
	if err != nil {
		t.Fatal(err)
	}
	a := alerts[0]
	if a.Text != "lead text" || !a.Start.Equal(at9(0, 0)) || a.End.Format("15:04:05") != "23:59:59" {
		t.Fatalf("alert = %+v", a)
	}
	if strings.Join(a.StopIDs, ",") != "8600626,8600646" {
		t.Fatalf("stops deduplicated in order = %v", a.StopIDs)
	}
	for body, want := range map[string]string{
		`nope`:                        "decode alerts",
		`{"errorCode":"API_AUTH"}`:    "API_AUTH",
		`{"Message":[{"sDate":"x"}]}`: "bad date/time",
		`{"Message":[{"affectedStops":{"StopLocation":1}}]}`: "alert stops",
	} {
		if _, err := rejseplanen.ParseAlerts([]byte(body), cph); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}

func TestClientJourneyAndAlerts(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.Query().Get("id")+"|"+r.URL.Query().Get("maxNum"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/journeyDetail"):
			w.Write([]byte(`{"Stops":{"Stop":[{"extId":"1","depTime":"08:00:00","depDate":"2026-10-09"}]}}`))
		case strings.HasSuffix(r.URL.Path, "/himsearch"):
			w.Write([]byte(`{"Message":[]}`))
		}
	}))
	defer srv.Close()
	var calls []string
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k", MaxAlerts: 50, Location: cph,
		OnCall: func(endpoint string, err error, _ time.Duration) { calls = append(calls, endpoint) }}
	j, err := c.JourneyDetail(context.Background(), "2|#ZI#5#DA#91026#")
	if err != nil {
		t.Fatal(err)
	}
	if j.Ref != "2|#ZI#5#DA#91026#" || j.Key() != "5@91026" {
		t.Fatalf("ref should default to the requested one: %+v", j)
	}
	if _, err := c.Alerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, " ") != "/journeyDetail?2|#ZI#5#DA#91026#| /himsearch?|50" {
		t.Fatalf("paths = %v", paths)
	}
	if strings.Join(calls, ",") != "journeyDetail,himsearch" {
		t.Fatalf("OnCall = %v", calls)
	}
}

func TestClientErrorsAreTyped(t *testing.T) {
	status := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k"}
	_, err := c.JourneyDetail(context.Background(), "x")
	if !rejseplanen.IsTemporary(err) {
		t.Fatalf("503 should be temporary: %v", err)
	}
	status = http.StatusBadRequest
	_, err = c.Alerts(context.Background())
	if err == nil || rejseplanen.IsTemporary(err) {
		t.Fatalf("400 should be permanent: %v", err)
	}
	if rejseplanen.IsTemporary(context.Canceled) || rejseplanen.IsTemporary(nil) {
		t.Fatal("cancel/nil are not temporary")
	}
}
