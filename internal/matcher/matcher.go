// Package matcher maps API observations onto static GTFS trips.
//
// Journeys (journeyDetail, many stops) are matched by aligning their stop
// sequence and scheduled times against candidate trips, which works for every
// mode without relying on line names. Single departures (departureBoard rows)
// are matched by stop and minute, then narrowed by line and direction.
// Anything ambiguous is rejected: a delay on the wrong trip is worse than none.
package matcher

import (
	"strings"
	"time"
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

// Result is a match plus, for journeys, the trip position of every observed
// stop (-1 where the stop could not be aligned).
type Result struct {
	Outcome   Outcome
	Match     gtfs.Match
	Positions []int
}

type Matcher struct {
	Schedule *gtfs.Schedule
}

// MatchDeparture matches one departure board row.
func (m *Matcher) MatchDeparture(d rejseplanen.Departure) Result {
	candidates := m.Schedule.Candidates(d.StopID, d.Scheduled, false)
	candidates = m.narrowByLine(candidates, d.Line)
	switch len(candidates) {
	case 0:
		return Result{Outcome: NoCandidate}
	case 1:
		return Result{Outcome: Matched, Match: candidates[0]}
	}
	want := normalizePlace(d.Direction)
	if want == "" {
		return Result{Outcome: Ambiguous}
	}
	var hits []gtfs.Match
	for _, c := range candidates {
		if samePlace(want, normalizePlace(m.Schedule.LastStopName(c.Trip))) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 1 {
		return Result{Outcome: Matched, Match: hits[0]}
	}
	return Result{Outcome: Ambiguous}
}

// narrowByLine keeps candidates whose route_short_name equals line, if any
// do. Train lines are often named differently in GTFS and the API, so a line
// that matches nothing is ignored rather than rejecting everything.
func (m *Matcher) narrowByLine(cs []gtfs.Match, line string) []gtfs.Match {
	line = gtfs.NormalizeLine(line)
	if line == "" || len(cs) < 2 {
		return cs
	}
	var out []gtfs.Match
	for _, c := range cs {
		if gtfs.NormalizeLine(m.Schedule.RouteOf(c.Trip).ShortName) == line {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return cs
	}
	return out
}

// maxAnchors is how many journey stops are tried as the lookup anchor when
// earlier ones are unknown to the GTFS.
const maxAnchors = 4

// MatchJourney matches a full journey by aligning its stops with each
// candidate trip found at an anchor stop.
func (m *Matcher) MatchJourney(j rejseplanen.Journey) Result {
	seen := map[string]bool{}
	var candidates []gtfs.Match
	anchors := 0
	for _, s := range j.Stops {
		if anchors == maxAnchors {
			break
		}
		t, arrival := s.ScheduledDeparture, false
		if t == nil {
			t, arrival = s.ScheduledArrival, true
		}
		if t == nil || len(m.Schedule.StopIndexes(s.StopID)) == 0 {
			continue
		}
		anchors++
		for _, c := range m.Schedule.Candidates(s.StopID, *t, arrival) {
			key := c.Trip.ID + "|" + c.ServiceDate
			if !seen[key] {
				seen[key] = true
				candidates = append(candidates, c)
			}
		}
		if len(candidates) > 0 {
			break
		}
	}
	if len(candidates) == 0 {
		return Result{Outcome: NoCandidate}
	}

	type scored struct {
		match     gtfs.Match
		positions []int
		score     int
	}
	best, second := scored{score: -1}, scored{score: -1}
	for _, c := range candidates {
		positions, score := m.align(j, c)
		sc := scored{c, positions, score}
		switch {
		case score > best.score:
			second, best = best, sc
		case score > second.score:
			second = sc
		}
	}
	need := min(2, len(j.Stops))
	if best.score < need {
		return Result{Outcome: NoCandidate}
	}
	if best.score == second.score {
		// Same stops and times: fall back to the line name.
		line := gtfs.NormalizeLine(j.Line)
		bestLine := gtfs.NormalizeLine(m.Schedule.RouteOf(best.match.Trip).ShortName) == line
		secondLine := gtfs.NormalizeLine(m.Schedule.RouteOf(second.match.Trip).ShortName) == line
		if line == "" || bestLine == secondLine {
			return Result{Outcome: Ambiguous}
		}
		if secondLine {
			best = second
		}
	}
	match := best.match
	for _, p := range best.positions {
		if p >= 0 {
			match.Position = p
			break
		}
	}
	return Result{Outcome: Matched, Match: match, Positions: best.positions}
}

// align walks the journey's stops and the trip's stop times in order. A
// journey stop aligns with the next trip stop at the same station; it scores
// when its scheduled time also matches to the minute.
func (m *Matcher) align(j rejseplanen.Journey, c gtfs.Match) ([]int, int) {
	s := m.Schedule
	sts := s.StopTimes(c.Trip)
	positions := make([]int, len(j.Stops))
	score := 0
	next := 0
	for i, js := range j.Stops {
		positions[i] = -1
		for p := next; p < len(sts); p++ {
			if !s.SameStop(js.StopID, sts[p].Stop) {
				continue
			}
			positions[i] = p
			next = p + 1
			if sameMinute(js.ScheduledDeparture, s.ScheduledDeparture(c, p)) ||
				sameMinute(js.ScheduledArrival, s.ScheduledArrival(c, p)) {
				score++
			}
			break
		}
	}
	return positions, score
}

func sameMinute(a *time.Time, b time.Time) bool {
	return a != nil && a.Truncate(time.Minute).Equal(b.Truncate(time.Minute))
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
