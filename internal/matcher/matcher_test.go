package matcher

import (
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func newMatcher(t *testing.T, aliases map[string]string) *Matcher {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture),
		gtfs.Filter{RouteShortNames: map[string]bool{"A": true, "B": true}}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return &Matcher{Schedule: s, StopAliases: aliases}
}

func dep(stop, line, direction string, h, m int) rejseplanen.Departure {
	return rejseplanen.Departure{
		StopID: stop, Line: line, Direction: direction,
		Scheduled: time.Date(2026, 10, 8, h, m, 0, 0, cph),
	}
}

func TestMatch(t *testing.T) {
	m := newMatcher(t, map[string]string{"000052887": "8600626"})
	cases := []struct {
		name    string
		dep     rejseplanen.Departure
		trip    string
		outcome Outcome
	}{
		{"unique", dep("8600626", "B", "", 8, 12), "B1", Matched},
		{"direction picks northbound", dep("8600626", "A", "Hillerød St.", 8, 12), "A1", Matched},
		{"direction picks southbound", dep("8600626", "A", "Hundige", 8, 12), "A2", Matched},
		{"direction with suffix", dep("8600626", "A", "Hundige St. (Greve)", 8, 12), "A2", Matched},
		{"no direction is ambiguous", dep("8600626", "A", "", 8, 12), "", Ambiguous},
		{"unknown direction is ambiguous", dep("8600626", "A", "Køge", 8, 12), "", Ambiguous},
		{"nothing scheduled", dep("8600626", "A", "Hillerød", 8, 13), "", NoCandidate},
		{"alias maps api id", dep("000052887", "B", "", 8, 12), "B1", Matched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := m.Match(tc.dep)
			if outcome != tc.outcome {
				t.Fatalf("outcome = %v, want %v", outcome, tc.outcome)
			}
			if tc.trip != "" && got.Trip.ID != tc.trip {
				t.Fatalf("trip = %s, want %s", got.Trip.ID, tc.trip)
			}
		})
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
		{"køge", "køge nord", true},
		{"hillerød", "hundige", false},
		{"ny", "nyhavn", false},
		{"", "", false},
		{"x", "", false},
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
