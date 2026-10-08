// Package matcher maps observed API departures onto static GTFS trips.
package matcher

import (
	"strings"
	"unicode"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

type Outcome int

const (
	Matched Outcome = iota
	NoCandidate
	Ambiguous
)

func (o Outcome) String() string {
	switch o {
	case Matched:
		return "matched"
	case NoCandidate:
		return "no_candidate"
	case Ambiguous:
		return "ambiguous"
	}
	return "unknown"
}

type Matcher struct {
	Schedule *gtfs.Schedule
	// StopAliases maps API stop ids to GTFS stop ids where the automatic
	// zero-stripped/stop_code/parent_station matching is not enough.
	StopAliases map[string]string
}

// Match finds the single GTFS trip for a departure. When several trips fit
// line and minute, the API direction is compared with each trip's headsign
// (or last stop). Anything still ambiguous is rejected: publishing a delay
// on the wrong trip is worse than publishing nothing.
func (m *Matcher) Match(dep rejseplanen.Departure) (gtfs.Match, Outcome) {
	stopID := dep.StopID
	if alias, ok := m.StopAliases[stopID]; ok {
		stopID = alias
	}
	candidates := m.Schedule.Candidates(stopID, dep.Line, dep.Scheduled)
	switch len(candidates) {
	case 0:
		return gtfs.Match{}, NoCandidate
	case 1:
		return candidates[0], Matched
	}
	want := normalizePlace(dep.Direction)
	if want == "" {
		return gtfs.Match{}, Ambiguous
	}
	var hits []gtfs.Match
	for _, c := range candidates {
		if samePlace(want, normalizePlace(m.Schedule.LastStopName(c.Trip))) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 1 {
		return hits[0], Matched
	}
	return gtfs.Match{}, Ambiguous
}

// normalizePlace lowercases, drops parenthesised suffixes and the common
// "St." station suffix, and collapses punctuation to spaces.
func normalizePlace(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, s)
	fields := strings.Fields(s)
	for len(fields) > 1 && (fields[len(fields)-1] == "st" || fields[len(fields)-1] == "station") {
		fields = fields[:len(fields)-1]
	}
	return strings.Join(fields, " ")
}

func samePlace(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+" ") || strings.HasPrefix(b, a+" ")
}
