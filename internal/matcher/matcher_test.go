package matcher

import (
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, cph) }

func newMatcher(t *testing.T, files map[string]string) *Matcher {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, files), gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return &Matcher{Schedule: s}
}

func dep(stop, line, direction string, h, m int) rejseplanen.Departure {
	return rejseplanen.Departure{StopID: stop, Line: line, Direction: direction, Scheduled: at(8, h, m)}
}

func TestMatchDeparture(t *testing.T) {
	m := newMatcher(t, gtfstest.Fixture)
	cases := []struct {
		name    string
		dep     rejseplanen.Departure
		trip    string
		outcome Outcome
	}{
		{"line narrows to one", dep("8600626", "B", "", 8, 12), "B1", Matched},
		{"line case insensitive", dep("8600626", " b ", "", 8, 12), "B1", Matched},
		{"bus line", dep("8600626", "1A", "", 8, 12), "BUS", Matched},
		{"direction picks northbound", dep("8600626", "A", "Hillerød St.", 8, 12), "A1", Matched},
		{"direction picks southbound", dep("8600626", "A", "Hundige", 8, 12), "A2", Matched},
		{"direction with suffix", dep("8600626", "A", "Hundige St. (Greve)", 8, 12), "A2", Matched},
		{"unknown line is ignored, direction decides", dep("8600626", "RE 1234", "Høje Taastrup St.", 8, 12), "B1", Matched},
		{"no direction is ambiguous", dep("8600626", "A", "", 8, 12), "", Ambiguous},
		{"unknown direction is ambiguous", dep("8600626", "A", "Køge", 8, 12), "", Ambiguous},
		{"nothing scheduled", dep("8600626", "A", "Hillerød", 8, 13), "", NoCandidate},
		{"zero padded api id", dep("0008600626", "B", "", 8, 12), "B1", Matched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := m.MatchDeparture(tc.dep)
			if res.Outcome != tc.outcome {
				t.Fatalf("outcome = %v, want %v", res.Outcome, tc.outcome)
			}
			if tc.trip != "" && res.Match.Trip.ID != tc.trip {
				t.Fatalf("trip = %s, want %s", res.Match.Trip.ID, tc.trip)
			}
		})
	}
}

func tp(t time.Time) *time.Time { return &t }

func call(stop string, arr, dep *time.Time) rejseplanen.JourneyStop {
	return rejseplanen.JourneyStop{StopID: stop, ScheduledArrival: arr, ScheduledDeparture: dep}
}

func TestMatchJourney(t *testing.T) {
	m := newMatcher(t, gtfstest.Fixture)
	a1 := rejseplanen.Journey{Line: "A", Stops: []rejseplanen.JourneyStop{
		call("8600601", nil, tp(at(8, 7, 50))),
		call("8600626", tp(at(8, 8, 11)), tp(at(8, 8, 12))),
		call("8600646", tp(at(8, 8, 15)), tp(at(8, 8, 15))),
		call("8600669", tp(at(8, 8, 50)), nil),
	}}
	res := m.MatchJourney(a1)
	if res.Outcome != Matched || res.Match.Trip.ID != "A1" {
		t.Fatalf("A1 = %v %v", res.Outcome, res.Match.Trip)
	}
	if want := []int{0, 1, 2, 3}; !equal(res.Positions, want) {
		t.Fatalf("positions = %v, want %v", res.Positions, want)
	}
	if res.Match.Position != 0 || res.Match.ServiceDate != "20261008" {
		t.Fatalf("match = %+v", res.Match)
	}
}

func TestMatchJourneyStartsAtCommonStop(t *testing.T) {
	m := newMatcher(t, gtfstest.Fixture)
	// The journey starts at København H where A1, A2, B1 and BUS all depart
	// at 08:12; the following stops decide.
	j := rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{
		call("8600626", nil, tp(at(8, 8, 12))),
		call("8600601", tp(at(8, 8, 35)), nil),
	}}
	res := m.MatchJourney(j)
	if res.Outcome != Matched || res.Match.Trip.ID != "A2" {
		t.Fatalf("got %v %v, want A2", res.Outcome, res.Match.Trip)
	}
	if !equal(res.Positions, []int{2, 3}) {
		t.Fatalf("positions = %v", res.Positions)
	}
	if res.Match.Position != 2 {
		t.Fatalf("anchor position = %d", res.Match.Position)
	}
}

func TestMatchJourneyUnknownFirstStops(t *testing.T) {
	m := newMatcher(t, gtfstest.Fixture)
	j := rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{
		call("999", nil, tp(at(8, 7, 0))), // not in GTFS
		call("8600626", nil, tp(at(8, 8, 12))),
		call("8600695", tp(at(8, 8, 40)), nil),
	}}
	res := m.MatchJourney(j)
	if res.Outcome != Matched || res.Match.Trip.ID != "B1" || !equal(res.Positions, []int{-1, 0, 1}) {
		t.Fatalf("got %v %v %v", res.Outcome, res.Match.Trip, res.Positions)
	}
}

func TestMatchJourneyAfterMidnight(t *testing.T) {
	m := newMatcher(t, gtfstest.Fixture)
	j := rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{
		call("8600626", nil, tp(at(9, 0, 30))),
		call("8600669", tp(at(9, 1, 5)), nil),
	}}
	res := m.MatchJourney(j)
	if res.Outcome != Matched || res.Match.Trip.ID != "A3" || res.Match.ServiceDate != "20261008" {
		t.Fatalf("got %v %+v", res.Outcome, res.Match)
	}
}

func TestMatchJourneyRejectsWeakOrAmbiguous(t *testing.T) {
	// Two identical trips on different routes.
	files := gtfstest.With(map[string]string{
		"trips.txt": "route_id,service_id,trip_id,trip_headsign\n" +
			"rA,WD,T1,X\n" + "rB,WD,T2,X\n",
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"T1,08:00:00,08:00:00,8600626,1\n" + "T1,08:10:00,08:10:00,8600646,2\n" +
			"T2,08:00:00,08:00:00,8600626,1\n" + "T2,08:10:00,08:10:00,8600646,2\n",
	})
	m := newMatcher(t, files)
	j := rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{
		call("8600626", nil, tp(at(8, 8, 0))),
		call("8600646", tp(at(8, 8, 10)), nil),
	}}
	if res := m.MatchJourney(j); res.Outcome != Ambiguous {
		t.Fatalf("identical trips should be ambiguous, got %v", res.Outcome)
	}
	j.Line = "B"
	if res := m.MatchJourney(j); res.Outcome != Matched || res.Match.Trip.ID != "T2" {
		t.Fatalf("line should break the tie: %v %v", res.Outcome, res.Match.Trip)
	}
	j.Line = "A"
	if res := m.MatchJourney(j); res.Outcome != Matched || res.Match.Trip.ID != "T1" {
		t.Fatalf("line should break the tie: %v %v", res.Outcome, res.Match.Trip)
	}
	j.Line = "Z"
	if res := m.MatchJourney(j); res.Outcome != Ambiguous {
		t.Fatalf("unrelated line cannot break the tie: %v", res.Outcome)
	}
	// Only the anchor agrees in time: one agreeing stop is not enough.
	weak := rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{
		call("8600626", nil, tp(at(8, 8, 0))),
		call("8600646", tp(at(8, 8, 30)), nil),
	}}
	if res := m.MatchJourney(weak); res.Outcome != NoCandidate {
		t.Fatalf("weak match = %v", res.Outcome)
	}
	if res := m.MatchJourney(rejseplanen.Journey{Stops: []rejseplanen.JourneyStop{call("999", nil, tp(at(8, 8, 0)))}}); res.Outcome != NoCandidate {
		t.Fatalf("unknown stops = %v", res.Outcome)
	}
}

func TestNormalizePlace(t *testing.T) {
	cases := map[string]string{
		"Hillerød St.":          "hillerød",
		"Hillerød St":           "hillerød",
		"Hundige St. (Greve)":   "hundige",
		"København H":           "københavn h",
		"Høje Taastrup Station": "høje taastrup",
		"Ny Ellebjerg St.":      "ny ellebjerg",
		"  Østerport  ":         "østerport",
		"St.":                   "st",
		"":                      "",
	}
	for in, want := range cases {
		if got := normalizePlace(in); got != want {
			t.Errorf("normalizePlace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSamePlace(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"hillerød", "hillerød", true},
		{"københavn h", "københavn", true},
		{"københavn", "københavn h", true},
		{"hillerød", "hundige", false},
		{"ny", "nyhavn", false},
		{"", "", false},
	}
	for _, tc := range cases {
		if got := samePlace(tc.a, tc.b); got != tc.want {
			t.Errorf("samePlace(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestOutcomeString(t *testing.T) {
	for o, want := range map[Outcome]string{Matched: "matched", NoCandidate: "no_candidate", Ambiguous: "ambiguous", Outcome(99): "unknown"} {
		if o.String() != want {
			t.Errorf("%d = %q", o, o.String())
		}
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
