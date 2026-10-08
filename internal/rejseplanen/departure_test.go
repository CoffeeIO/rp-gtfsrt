package rejseplanen_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func at(d, h, m int) time.Time {
	return time.Date(2026, 10, d, h, m, 0, 0, cph)
}

func TestParseV2(t *testing.T) {
	deps, err := rejseplanen.ParseDepartureBoard(fixture(t, "departureboard_v2.json"), "fallback", cph)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 4 {
		t.Fatalf("got %d departures", len(deps))
	}

	a := deps[0]
	if a.StopID != "000008626" || a.Line != "A" || a.Direction != "Hillerød St." || a.Track != "10" {
		t.Fatalf("A = %+v", a)
	}
	if !a.Scheduled.Equal(at(8, 8, 12)) || a.Realtime == nil || !a.Realtime.Equal(at(8, 8, 15)) {
		t.Fatalf("A times = %v / %v", a.Scheduled, a.Realtime)
	}
	if a.Delay() != 3*time.Minute {
		t.Fatalf("A delay = %v", a.Delay())
	}

	b := deps[1]
	if b.Line != "B" || b.Realtime != nil || b.Delay() != 0 || b.Track != "11" {
		t.Fatalf("B (single Product object, no rt) = %+v", b)
	}

	c := deps[2]
	if !c.Cancelled || c.Line != "C" {
		t.Fatalf("C = %+v", c)
	}
	if c.Realtime == nil || !c.Realtime.Equal(at(8, 8, 20)) {
		t.Fatalf("C rtTime without rtDate should reuse date: %v", c.Realtime)
	}

	f := deps[3]
	if f.Line != "F" {
		t.Fatalf("F line from name = %q", f.Line)
	}
	if f.Delay() != 5*time.Minute {
		t.Fatalf("F delay across midnight = %v", f.Delay())
	}
}

func TestParseV1Legacy(t *testing.T) {
	deps, err := rejseplanen.ParseDepartureBoard(fixture(t, "departureboard_v1.json"), "8600626", cph)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("single object should decode as one departure, got %d", len(deps))
	}
	d := deps[0]
	if d.StopID != "8600626" || d.Line != "A" || d.Direction != "Hillerød St." || !d.Cancelled {
		t.Fatalf("legacy = %+v", d)
	}
	if !d.Scheduled.Equal(at(8, 8, 12)) || d.Delay() != time.Minute {
		t.Fatalf("legacy times = %v delay %v", d.Scheduled, d.Delay())
	}
}

func TestParseLineFallbacks(t *testing.T) {
	cases := map[string]string{
		`{"name":"S-tog A","time":"08:12","date":"08.10.26"}`:                                     "A",
		`{"name":"Bus 1A","time":"08:12","date":"08.10.26"}`:                                      "1A",
		`{"name":"","time":"08:12","date":"08.10.26"}`:                                            "",
		`{"name":"x","line":"H","time":"08:12","date":"08.10.26"}`:                                "H",
		`{"name":"x","Product":[{"name":"Bx"}],"time":"08:12","date":"08.10.26"}`:                 "Bx",
		`{"name":"x","ProductAtStop":{"line":"E","name":"S E"},"time":"08:12","date":"08.10.26"}`: "E",
	}
	for body, want := range cases {
		deps, err := rejseplanen.ParseDepartureBoard([]byte(`{"Departure":[`+body+`]}`), "1", cph)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if deps[0].Line != want {
			t.Errorf("%s: line = %q, want %q", body, deps[0].Line, want)
		}
	}
}

func TestParseStopIDFromHafasID(t *testing.T) {
	body := `{"Departure":[{"stopid":"A=1@O=Nørreport St.@L=8600646@","time":"08:12:00","date":"2026-10-08"}]}`
	deps, err := rejseplanen.ParseDepartureBoard([]byte(body), "fallback", cph)
	if err != nil {
		t.Fatal(err)
	}
	if deps[0].StopID != "8600646" {
		t.Fatalf("StopID = %q", deps[0].StopID)
	}
	body = `{"Departure":[{"stopid":"8600646","time":"08:12:00","date":"2026-10-08"}]}`
	deps, _ = rejseplanen.ParseDepartureBoard([]byte(body), "fallback", cph)
	if deps[0].StopID != "8600646" {
		t.Fatalf("plain stopid = %q", deps[0].StopID)
	}
}

func TestParseEmptyBoards(t *testing.T) {
	for _, body := range []string{`{}`, `{"Departure":[]}`, `{"Departure":null}`, `{"DepartureBoard":{}}`} {
		deps, err := rejseplanen.ParseDepartureBoard([]byte(body), "1", cph)
		if err != nil || len(deps) != 0 {
			t.Errorf("%s: deps=%v err=%v", body, deps, err)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		`not json`: "decode",
		`{"errorCode":"API_AUTH","errorText":"access denied"}`:                  "API_AUTH",
		`{"DepartureBoard":{"error":"No access"}}`:                              "No access",
		`{"Departure":[{"time":"8h12","date":"2026-10-08"}]}`:                   "bad date/time",
		`{"Departure":[{"time":"08:12:00","date":"2026-10-08","rtTime":"xx"}]}`: "bad date/time",
		`{"Departure":"nope"}`:                                                  "decode departures",
	}
	for body, want := range cases {
		_, err := rejseplanen.ParseDepartureBoard([]byte(body), "1", cph)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", body, err, want)
		}
	}
}

func TestParseUsesLocation(t *testing.T) {
	body := []byte(`{"Departure":[{"time":"08:12:00","date":"2026-10-08"}]}`)
	deps, err := rejseplanen.ParseDepartureBoard(body, "1", cph)
	if err != nil {
		t.Fatal(err)
	}
	if got := deps[0].Scheduled.UTC(); got != time.Date(2026, 10, 8, 6, 12, 0, 0, time.UTC) {
		t.Fatalf("08:12 CEST should be 06:12 UTC, got %v", got)
	}
}

// Captured from the live API (København H, S-tog, 2026-10-08 13:55).
func TestParseLiveCapture(t *testing.T) {
	deps, err := rejseplanen.ParseDepartureBoard(fixture(t, "departureboard_live_20261008.json"), "8600626", cph)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 20 {
		t.Fatalf("got %d departures", len(deps))
	}
	delays := map[string]time.Duration{}
	for _, d := range deps {
		if d.StopID != "8600626" || d.Line == "" || d.Direction == "" || d.Track == "" {
			t.Errorf("incomplete: %+v", d)
		}
		if d.Realtime == nil {
			t.Errorf("%s %s: every live row has prognosisType, so Realtime must be set", d.Line, d.Scheduled.Format("15:04"))
		}
		if d.Delay() != 0 {
			delays[d.Line+" "+d.Scheduled.Format("15:04")] = d.Delay()
		}
	}
	want := map[string]time.Duration{"B 14:00": 3 * time.Minute, "B 14:10": time.Minute, "C 14:11": time.Minute}
	if len(delays) != len(want) {
		t.Fatalf("delays = %v, want %v", delays, want)
	}
	for k, v := range want {
		if delays[k] != v {
			t.Errorf("%s delay = %v, want %v", k, delays[k], v)
		}
	}
	if deps[0].Line != "E" || deps[0].Direction != "Holte St." || deps[0].Track != "10" {
		t.Fatalf("first = %+v", deps[0])
	}
}

func TestPrognosisWithoutRtTimeIsOnTime(t *testing.T) {
	body := `{"Departure":[
	 {"name":"A","time":"08:12:00","date":"2026-10-08","prognosisType":"PROGNOSED"},
	 {"name":"A","time":"08:12:00","date":"2026-10-08"}]}`
	deps, err := rejseplanen.ParseDepartureBoard([]byte(body), "1", cph)
	if err != nil {
		t.Fatal(err)
	}
	if deps[0].Realtime == nil || deps[0].Delay() != 0 {
		t.Fatalf("prognosed row = %+v", deps[0])
	}
	if deps[1].Realtime != nil {
		t.Fatal("row without prognosisType has no realtime data")
	}
}
