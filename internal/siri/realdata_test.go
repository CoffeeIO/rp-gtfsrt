package siri_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/matcher"
	"github.com/CoffeeIO/rp-gtfsrt/internal/siri"
)

// TestRealMatchRate matches a saved SIRI-ET delivery against a real
// Rejseplanen GTFS zip. It makes no network calls and is skipped unless both
// files are given:
//
//	RP_GTFS_ZIP=data/gtfs.zip RP_SIRI_FILE=et.xml go test ./internal/siri -run Real -v
func TestRealMatchRate(t *testing.T) {
	zipPath, siriPath := os.Getenv("RP_GTFS_ZIP"), os.Getenv("RP_SIRI_FILE")
	if zipPath == "" || siriPath == "" {
		t.Skip("RP_GTFS_ZIP and RP_SIRI_FILE not set")
	}
	cph, _ := time.LoadLocation("Europe/Copenhagen")
	sched, err := gtfs.LoadZip(zipPath, gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(siriPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := &matcher.Matcher{Schedule: sched}
	outcomes := map[string]int{}
	byStops := map[int]map[string]int{}
	sameLine, otherLine := 0, []string{}
	err = siri.Decode(f, func(u siri.Update) {
		res := m.MatchJourney(u.Journey)
		o := res.Outcome.String()
		if res.Outcome == matcher.Matched {
			// Trains carry the train number as LineRef; GTFS has it as trip_short_name.
			route := sched.RouteOf(res.Match.Trip).ShortName
			if line := gtfs.NormalizeLine(u.Journey.Line); line == gtfs.NormalizeLine(route) || line == strings.TrimLeft(res.Match.Trip.ShortName, "0") {
				sameLine++
			} else {
				otherLine = append(otherLine, u.Journey.Line+"→"+route+"/"+res.Match.Trip.ShortName+"/"+res.Match.Trip.Headsign)
			}
		}
		outcomes[o]++
		n := min(len(u.Journey.Stops), 4)
		if byStops[n] == nil {
			byStops[n] = map[string]int{}
		}
		byStops[n][o]++
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("outcomes: %v", outcomes)
	t.Logf("matched with the same line or train number: %d, different: %d %v", sameLine, len(otherLine), otherLine[:min(len(otherLine), 30)])
	for n := 0; n <= 4; n++ {
		t.Logf("journeys with %d stops (4 = 4+): %v", n, byStops[n])
	}
}
