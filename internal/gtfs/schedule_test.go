package gtfs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
)

var cph = mustLoc("Europe/Copenhagen")

func mustLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func stogFilter() gtfs.Filter {
	return gtfs.Filter{RouteShortNames: map[string]bool{"A": true, "B": true}}
}

func load(t *testing.T, files map[string]string, f gtfs.Filter) *gtfs.Schedule {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, files), f, cph)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, cph)
}

func TestLoadFiltersRoutesAndTrips(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if len(s.Routes) != 2 {
		t.Fatalf("routes = %d, want 2 (bus filtered)", len(s.Routes))
	}
	if _, ok := s.Trips["BUS"]; ok {
		t.Fatal("bus trip should be filtered out")
	}
	if len(s.Trips) != 6 {
		t.Fatalf("trips = %d, want 6", len(s.Trips))
	}
}

func TestLoadEmptyFilterKeepsEverything(t *testing.T) {
	s := load(t, gtfstest.Fixture, gtfs.Filter{})
	if len(s.Routes) != 3 || len(s.Trips) != 7 {
		t.Fatalf("routes=%d trips=%d, want 3/7", len(s.Routes), len(s.Trips))
	}
}

func TestLoadRouteTypeFilter(t *testing.T) {
	s := load(t, gtfstest.Fixture, gtfs.Filter{RouteTypes: map[int]bool{3: true}})
	if len(s.Routes) != 1 || s.Routes["bus1A"].ShortName != "1A" {
		t.Fatalf("routes = %v, want only bus1A", s.Routes)
	}
}

func TestLoadSortsStopTimes(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	a1 := s.Trips["A1"]
	for i, want := range []int{1, 2, 3, 4} {
		if got := a1.StopTimes[i].Sequence; got != want {
			t.Fatalf("stop %d sequence = %d, want %d", i, got, want)
		}
	}
	if a1.StopTimes[1].Arrival != 8*3600+11*60 || a1.StopTimes[1].Departure != 8*3600+12*60 {
		t.Fatalf("A1 København H times = %+v", a1.StopTimes[1])
	}
}

func TestLoadHandlesBOMHeader(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if s.Stops["8600626"].Name != "København H" {
		t.Fatalf("stop lookup failed; BOM not stripped? stops=%v", s.Stops)
	}
}

func TestLoadFillsMissingArrivalOrDeparture(t *testing.T) {
	files := gtfstest.With(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,,08:12:00,8600626-1,1\n" +
			"A1,08:50:00,,8600669,2\n",
	})
	s := load(t, files, stogFilter())
	st := s.Trips["A1"].StopTimes
	if st[0].Arrival != st[0].Departure || st[1].Departure != st[1].Arrival {
		t.Fatalf("missing times not filled: %+v", st)
	}
}

func TestLoadDropsTripsWithoutStopTimes(t *testing.T) {
	files := gtfstest.With(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,08:12:00,08:12:00,8600626-1,1\n",
	})
	s := load(t, files, stogFilter())
	if len(s.Trips) != 1 {
		t.Fatalf("trips = %d, want 1", len(s.Trips))
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]struct {
		files  map[string]string
		filter gtfs.Filter
		want   string
	}{
		"missing routes": {gtfstest.With(map[string]string{"routes.txt": ""}), stogFilter(), "missing routes.txt"},
		"missing stops":  {gtfstest.With(map[string]string{"stops.txt": ""}), stogFilter(), "missing stops.txt"},
		"no match":       {gtfstest.Fixture, gtfs.Filter{RouteShortNames: map[string]bool{"Z": true}}, "no routes match"},
		"bad time": {gtfstest.With(map[string]string{"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,8h12,08:12:00,8600626-1,1\n"}), stogFilter(), "bad time"},
		"bad sequence": {gtfstest.With(map[string]string{"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,08:12:00,08:12:00,8600626-1,x\n"}), stogFilter(), "bad stop_sequence"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := gtfs.Load(gtfstest.Reader(t, tc.files), tc.filter, cph)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLoadOptionalCalendarFiles(t *testing.T) {
	files := gtfstest.With(map[string]string{"calendar.txt": "", "calendar_dates.txt": ""})
	s := load(t, files, stogFilter())
	if s.ServiceActive("WD", "20261008") {
		t.Fatal("no calendar means no active service")
	}
}

func TestLoadZip(t *testing.T) {
	path := gtfstest.WriteFile(t, gtfstest.Fixture)
	s, err := gtfs.LoadZip(path, stogFilter(), cph)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Trips) != 6 {
		t.Fatalf("trips = %d", len(s.Trips))
	}
	if _, err := gtfs.LoadZip(path+".missing", stogFilter(), cph); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestServiceActive(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	cases := []struct {
		service, date string
		want          bool
	}{
		{"WD", "20261008", true},  // Thursday
		{"WD", "20261009", false}, // Friday, removed by calendar_dates
		{"WD", "20261010", false}, // Saturday
		{"WE", "20261010", true},
		{"WE", "20261008", false},
		{"XS", "20261008", true}, // added by calendar_dates only
		{"XS", "20261015", false},
		{"WD", "20251231", false}, // before start_date
		{"WD", "20270101", false}, // after end_date
		{"WD", "garbage", false},
		{"NOPE", "20261008", false},
	}
	for _, tc := range cases {
		if got := s.ServiceActive(tc.service, tc.date); got != tc.want {
			t.Errorf("ServiceActive(%s, %s) = %v, want %v", tc.service, tc.date, got, tc.want)
		}
	}
}

func tripIDs(ms []gtfs.Match) []string {
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.Trip.ID+"@"+m.ServiceDate)
	}
	return ids
}

func TestCandidates(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	cases := []struct {
		name, stop, line string
		when             time.Time
		want             []string
	}{
		{"parent station id", "8600626", "A", at(2026, 10, 8, 8, 12), []string{"A1@20261008", "A2@20261008"}},
		{"platform id", "8600626-1", "B", at(2026, 10, 8, 8, 12), []string{"B1@20261008"}},
		{"zero padded api id", "0008600626", "B", at(2026, 10, 8, 8, 12), []string{"B1@20261008"}},
		{"stop_code", "8646", "A", at(2026, 10, 8, 8, 15), []string{"A1@20261008"}},
		{"seconds ignored within minute", "8600646", "A", at(2026, 10, 8, 8, 15).Add(45 * time.Second), []string{"A1@20261008"}},
		{"line case insensitive", "8600626", " b ", at(2026, 10, 8, 8, 12), []string{"B1@20261008"}},
		{"wrong minute", "8600626", "B", at(2026, 10, 8, 8, 13), nil},
		{"wrong line", "8600626", "C", at(2026, 10, 8, 8, 12), nil},
		{"filtered bus", "8600626", "1A", at(2026, 10, 8, 8, 12), nil},
		{"unknown stop", "999", "A", at(2026, 10, 8, 8, 12), nil},
		{"weekday removed friday", "8600626", "B", at(2026, 10, 9, 8, 12), nil},
		{"weekend only", "8600626", "A", at(2026, 10, 10, 8, 12), []string{"W1@20261010"}},
		{"calendar_dates added", "8600626", "A", at(2026, 10, 8, 9, 0), []string{"X1@20261008"}},
		// 00:30 on Friday is 24:30 on Thursday's service day.
		{"after midnight", "8600626", "A", at(2026, 10, 9, 0, 30), []string{"A3@20261008"}},
		// 00:30 on Saturday would be Friday's service, which is removed.
		{"after midnight removed day", "8600626", "A", at(2026, 10, 10, 0, 30), nil},
		{"input in UTC", "8600626", "B", at(2026, 10, 8, 8, 12).UTC(), []string{"B1@20261008"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tripIDs(s.Candidates(tc.stop, tc.line, tc.when))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") && !sameSet(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestCandidatesStopIndex(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	for _, m := range s.Candidates("8600626", "A", at(2026, 10, 8, 8, 12)) {
		if m.Trip.StopTimes[m.StopIndex].StopID != "8600626-1" {
			t.Fatalf("%s stop index points at %s", m.Trip.ID, m.Trip.StopTimes[m.StopIndex].StopID)
		}
	}
}

func TestScheduledAtAndTripEnd(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	ms := s.Candidates("8600626", "A", at(2026, 10, 9, 0, 30))
	if len(ms) != 1 {
		t.Fatalf("matches = %v", tripIDs(ms))
	}
	if got, want := s.ScheduledAt(ms[0], ms[0].StopIndex), at(2026, 10, 9, 0, 30); !got.Equal(want) {
		t.Fatalf("ScheduledAt = %v, want %v", got, want)
	}
	if got, want := s.TripEnd(ms[0]), at(2026, 10, 9, 1, 5); !got.Equal(want) {
		t.Fatalf("TripEnd = %v, want %v", got, want)
	}
}

func TestLastStopName(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if got := s.LastStopName(s.Trips["A1"]); got != "Hillerød" {
		t.Fatalf("headsign = %q", got)
	}
	if got := s.LastStopName(s.Trips["A3"]); got != "Hillerød St." {
		t.Fatalf("fallback = %q", got)
	}
	if got := s.LastStopName(&gtfs.Trip{}); got != "" {
		t.Fatalf("empty trip = %q", got)
	}
}

func TestFindStops(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	got := s.FindStops("københavn")
	if len(got) != 2 || got[0].ID != "8600626" || got[1].Parent != "8600626" {
		t.Fatalf("FindStops = %+v", got)
	}
	if len(s.FindStops("nowhere")) != 0 {
		t.Fatal("expected no results")
	}
}

func TestStopKey(t *testing.T) {
	cases := map[string]string{
		"000052887": "52887",
		"52887":     "52887",
		" 0042 ":    "42",
		"000":       "0",
		"":          "",
		"A=1@L=8@":  "A=1@L=8@",
	}
	for in, want := range cases {
		if got := gtfs.StopKey(in); got != want {
			t.Errorf("StopKey(%q) = %q, want %q", in, got, want)
		}
	}
}
