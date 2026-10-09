package matcher

import (
	"os"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// The tests in this file need a real Rejseplanen GTFS zip and make no API
// calls. They are skipped unless RP_GTFS_ZIP is set:
//
//	RP_GTFS_ZIP=data/gtfs.zip RP_DATE=2026-10-09 go test ./internal/matcher -run Real -v

func realSchedule(t *testing.T) (*gtfs.Schedule, string) {
	t.Helper()
	path := os.Getenv("RP_GTFS_ZIP")
	if path == "" {
		t.Skip("RP_GTFS_ZIP not set")
	}
	day, err := time.ParseInLocation("2006-01-02", os.Getenv("RP_DATE"), cph)
	if err != nil {
		t.Skip("RP_DATE (YYYY-MM-DD) not set")
	}
	s, err := gtfs.LoadZip(path, gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return s, day.Format("20060102")
}

// journeyFromGTFS builds what journeyDetail would return for a trip, using
// API-style stop ids (leading zeros stripped).
func journeyFromGTFS(s *gtfs.Schedule, m gtfs.Match) rejseplanen.Journey {
	sts := s.StopTimes(m.Trip)
	j := rejseplanen.Journey{Line: s.RouteOf(m.Trip).ShortName}
	for p, st := range sts {
		js := rejseplanen.JourneyStop{StopID: gtfs.StopKey(s.Stops[st.Stop].ID), Index: p}
		if p > 0 {
			arr := s.ScheduledArrival(m, p)
			js.ScheduledArrival = &arr
		}
		if p < len(sts)-1 {
			dep := s.ScheduledDeparture(m, p)
			js.ScheduledDeparture = &dep
		}
		j.Stops = append(j.Stops, js)
	}
	return j
}

// TestRealJourneyRoundTrip matches every trip active on RP_DATE, all modes,
// as a journeyDetail would describe it, and checks it maps back to itself.
// Ambiguous results are allowed (the GTFS contains some exact duplicate
// trips); a wrong trip is not.
func TestRealJourneyRoundTrip(t *testing.T) {
	s, date := realSchedule(t)
	m := &Matcher{Schedule: s}
	var total, ok, ambiguous, none, wrong int
	for i := range s.Trips {
		trip := &s.Trips[i]
		if !s.ServiceActive(trip, date) {
			continue
		}
		total++
		match := gtfs.Match{Trip: trip, ServiceDate: date}
		res := m.MatchJourney(journeyFromGTFS(s, match))
		switch {
		case res.Outcome == Ambiguous:
			ambiguous++
		case res.Outcome == NoCandidate:
			none++
			if none <= 3 {
				t.Logf("no candidate: trip %s", trip.ID)
			}
		case res.Match.Trip.ID != trip.ID || res.Match.ServiceDate != date:
			wrong++
			if wrong <= 5 {
				t.Logf("wrong: trip %s matched %s@%s", trip.ID, res.Match.Trip.ID, res.Match.ServiceDate)
			}
		default:
			ok++
		}
	}
	t.Logf("%s: %d journeys, %d matched, %d ambiguous, %d no candidate, %d wrong", date, total, ok, ambiguous, none, wrong)
	if total == 0 {
		t.Fatal("no trips active; wrong date?")
	}
	if wrong > 0 || none > 0 {
		t.Fatalf("%d wrong, %d unmatched", wrong, none)
	}
}

// TestRealDepartureRoundTrip replays every scheduled departure at the hub
// stops (RP_HUBS, default København H and Nørreport) as board rows.
func TestRealDepartureRoundTrip(t *testing.T) {
	s, date := realSchedule(t)
	hubs := []string{"8600626", "8600646"}
	if v := os.Getenv("RP_HUBS"); v != "" {
		hubs = splitComma(v)
	}
	m := &Matcher{Schedule: s}
	var total, ok, ambiguous, wrong int
	for i := range s.Trips {
		trip := &s.Trips[i]
		if !s.ServiceActive(trip, date) {
			continue
		}
		sts := s.StopTimes(trip)
		for p, st := range sts {
			hub := ""
			for _, h := range hubs {
				if s.SameStop(h, st.Stop) {
					hub = h
				}
			}
			if hub == "" || p == len(sts)-1 {
				continue
			}
			total++
			match := gtfs.Match{Trip: trip, Position: p, ServiceDate: date}
			res := m.MatchDeparture(rejseplanen.Departure{
				StopID:    hub,
				Line:      s.RouteOf(trip).ShortName,
				Direction: s.LastStopName(trip),
				Scheduled: s.ScheduledDeparture(match, p),
			})
			switch {
			case res.Outcome == Ambiguous:
				ambiguous++
			case res.Outcome != Matched || res.Match.Trip.ID != trip.ID:
				wrong++
				if wrong <= 5 {
					t.Logf("wrong: trip %s line %s at %s → %v %v", trip.ID, s.RouteOf(trip).ShortName, hub, res.Outcome, res.Match.Trip)
				}
			default:
				ok++
			}
		}
	}
	t.Logf("%s: %d hub departures, %d matched, %d ambiguous, %d wrong", date, total, ok, ambiguous, wrong)
	if total == 0 {
		t.Fatal("no departures found; wrong date or hubs?")
	}
	if wrong > 0 {
		t.Fatalf("%d departures matched the wrong trip", wrong)
	}
}

// TestRealBoardCapture runs a captured live departure board (RP_BOARD_JSON)
// through the matcher against the real GTFS.
func TestRealBoardCapture(t *testing.T) {
	boardPath := os.Getenv("RP_BOARD_JSON")
	if boardPath == "" {
		t.Skip("RP_BOARD_JSON not set")
	}
	s, _ := realSchedule(t)
	body, err := os.ReadFile(boardPath)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := rejseplanen.ParseDepartureBoard(body, "", cph)
	if err != nil {
		t.Fatal(err)
	}
	m := &Matcher{Schedule: s}
	outcomes := map[Outcome]int{}
	cancelled := 0
	for _, d := range deps {
		res := m.MatchDeparture(d)
		outcomes[res.Outcome]++
		if res.Outcome != Matched {
			t.Logf("%v: %s %s → %s", res.Outcome, d.Line, d.Scheduled.Format("15:04"), d.Direction)
			continue
		}
		if d.Cancelled {
			cancelled++
		}
	}
	t.Logf("matched %d, no candidate %d, ambiguous %d, of which cancelled %d",
		outcomes[Matched], outcomes[NoCandidate], outcomes[Ambiguous], cancelled)
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
