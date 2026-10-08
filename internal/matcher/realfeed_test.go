package matcher

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// TestRealFeedRoundTrip replays every scheduled S-tog departure at the hub
// stops through the matcher and checks it maps back to its own trip. It
// needs a real Rejseplanen GTFS zip and makes no API calls:
//
//	RP_GTFS_ZIP=data/gtfs.zip RP_DATE=2026-10-08 go test ./internal/matcher -run RealFeed -v
func TestRealFeedRoundTrip(t *testing.T) {
	path := os.Getenv("RP_GTFS_ZIP")
	if path == "" {
		t.Skip("RP_GTFS_ZIP not set")
	}
	day, err := time.ParseInLocation("2006-01-02", os.Getenv("RP_DATE"), cph)
	if err != nil {
		t.Skip("RP_DATE (YYYY-MM-DD) not set")
	}
	hubs := strings.Split(envOr("RP_HUBS", "8600626,8600646"), ",")
	lines := map[string]bool{"A": true, "B": true, "BX": true, "C": true, "E": true, "F": true, "H": true}
	s, err := gtfs.LoadZip(path, gtfs.Filter{RouteShortNames: lines}, cph)
	if err != nil {
		t.Fatal(err)
	}
	m := &Matcher{Schedule: s}
	date := day.Format("20060102")

	var total, ok, ambiguous, wrong int
	for _, trip := range s.Trips {
		if !s.ServiceActive(trip.ServiceID, date) {
			continue
		}
		for i, st := range trip.StopTimes {
			if !isHub(s, st.StopID, hubs) || i == len(trip.StopTimes)-1 {
				continue
			}
			total++
			match := gtfs.Match{Trip: trip, StopIndex: i, ServiceDate: date}
			dep := rejseplanen.Departure{
				StopID:    st.StopID,
				Line:      s.Routes[trip.RouteID].ShortName,
				Direction: s.LastStopName(trip),
				Scheduled: s.ScheduledAt(match, i),
			}
			got, outcome := m.Match(dep)
			switch {
			case outcome == Ambiguous:
				ambiguous++
			case outcome != Matched || got.Trip.ID != trip.ID:
				wrong++
				if wrong <= 5 {
					t.Logf("mismatch: trip %s line %s at %s %s → %v %v", trip.ID, dep.Line, st.StopID,
						dep.Scheduled.Format("15:04"), outcome, got.Trip)
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

func isHub(s *gtfs.Schedule, stopID string, hubs []string) bool {
	keys := []string{gtfs.StopKey(stopID), gtfs.StopKey(s.Stops[stopID].Parent)}
	for _, h := range hubs {
		for _, k := range keys {
			if k != "" && k == gtfs.StopKey(h) {
				return true
			}
		}
	}
	return false
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// TestRealBoardCapture runs a captured live departure board through the
// matcher against the real GTFS. Rows on lines outside the loaded routes are
// ignored. No API calls:
//
//	RP_GTFS_ZIP=data/gtfs.zip RP_BOARD_JSON=board.json go test ./internal/matcher -run RealBoard -v
func TestRealBoardCapture(t *testing.T) {
	zipPath, boardPath := os.Getenv("RP_GTFS_ZIP"), os.Getenv("RP_BOARD_JSON")
	if zipPath == "" || boardPath == "" {
		t.Skip("RP_GTFS_ZIP and RP_BOARD_JSON not set")
	}
	lines := map[string]bool{"A": true, "B": true, "BX": true, "C": true, "E": true, "F": true, "H": true}
	s, err := gtfs.LoadZip(zipPath, gtfs.Filter{RouteShortNames: lines}, cph)
	if err != nil {
		t.Fatal(err)
	}
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
		if !lines[gtfs.NormalizeLine(d.Line)] {
			continue
		}
		match, outcome := m.Match(d)
		outcomes[outcome]++
		if outcome != Matched {
			t.Errorf("%v: %s %s → %s", outcome, d.Line, d.Scheduled.Format("15:04"), d.Direction)
			continue
		}
		if d.Cancelled {
			cancelled++
			t.Logf("cancelled %s %s → %s matched trip %s", d.Line, d.Scheduled.Format("15:04"), d.Direction, match.Trip.ID)
		}
	}
	t.Logf("matched %d, no candidate %d, ambiguous %d, of which cancelled %d",
		outcomes[Matched], outcomes[NoCandidate], outcomes[Ambiguous], cancelled)
}
