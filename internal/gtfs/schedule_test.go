package gtfs_test

import (
	"sort"
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

func trip(t *testing.T, s *gtfs.Schedule, id string) *gtfs.Trip {
	t.Helper()
	tr, ok := s.TripByID(id)
	if !ok {
		t.Fatalf("trip %s not loaded", id)
	}
	return tr
}

func TestLoadFiltersRoutesAndTrips(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if len(s.Routes) != 2 {
		t.Fatalf("routes = %d, want 2 (bus filtered)", len(s.Routes))
	}
	if _, ok := s.TripByID("BUS"); ok {
		t.Fatal("bus trip should be filtered out")
	}
	if len(s.Trips) != 6 {
		t.Fatalf("trips = %d, want 6", len(s.Trips))
	}
	if st := s.Stats(); st.Trips != 6 || st.Routes != 2 || st.Stops != 6 || st.StopTimes != 16 {
		t.Fatalf("stats = %+v", st)
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
	if len(s.Routes) != 1 || s.Routes[0].ShortName != "1A" || s.Routes[0].AgencyID != "DSB" {
		t.Fatalf("routes = %v, want only bus1A", s.Routes)
	}
}

func TestLoadSortsStopTimes(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	sts := s.StopTimes(trip(t, s, "A1"))
	for i, want := range []int32{1, 2, 3, 4} {
		if got := sts[i].Sequence; got != want {
			t.Fatalf("stop %d sequence = %d, want %d", i, got, want)
		}
	}
	if sts[1].Arrival != 8*3600+11*60 || sts[1].Departure != 8*3600+12*60 {
		t.Fatalf("A1 København H times = %+v", sts[1])
	}
	if s.Stops[sts[1].Stop].ID != "8600626-1" {
		t.Fatalf("A1 stop 2 = %s", s.Stops[sts[1].Stop].ID)
	}
}

func TestTripIndexAndRoute(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	for i := range s.Trips {
		if s.TripIndex(&s.Trips[i]) != i {
			t.Fatalf("TripIndex(%d) wrong", i)
		}
	}
	if s.RouteOf(trip(t, s, "B1")).ShortName != "B" {
		t.Fatal("RouteOf")
	}
}

func TestLoadHandlesBOMHeader(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if len(s.StopIndexes("8600626")) == 0 || s.Stops[s.StopIndexes("8600626")[0]].Name != "København H" {
		t.Fatal("stop lookup failed; BOM not stripped?")
	}
}

func TestLoadFillsMissingArrivalOrDeparture(t *testing.T) {
	files := gtfstest.With(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,,08:12:00,8600626-1,1\n" +
			"A1,08:50:00,,8600669,2\n",
	})
	s := load(t, files, stogFilter())
	st := s.StopTimes(trip(t, s, "A1"))
	if st[0].Arrival != st[0].Departure || st[1].Departure != st[1].Arrival {
		t.Fatalf("missing times not filled: %+v", st)
	}
}

func TestLoadDropsTripsWithoutStopTimes(t *testing.T) {
	files := gtfstest.With(map[string]string{
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"B1,08:12:00,08:12:00,8600626-1,1\n" +
			"B1,08:40:00,08:40:00,8600695,2\n",
	})
	s := load(t, files, stogFilter())
	if len(s.Trips) != 1 {
		t.Fatalf("trips = %d, want 1", len(s.Trips))
	}
	// Indexes must be consistent after dropping trips.
	b1 := trip(t, s, "B1")
	if s.TripIndex(b1) != 0 || len(s.StopTimes(b1)) != 2 {
		t.Fatalf("B1 = %+v", b1)
	}
	if ms := s.Candidates("8600626", at(2026, 10, 8, 8, 12), false); len(ms) != 1 || ms[0].Trip.ID != "B1" {
		t.Fatalf("candidates after drop = %v", ids(ms))
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
		"unknown stop": {gtfstest.With(map[string]string{"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
			"A1,08:12:00,08:12:00,nowhere,1\n"}), stogFilter(), "unknown stop_id"},
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
	if s.ServiceActiveByID("WD", "20261008") {
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
		if got := s.ServiceActiveByID(tc.service, tc.date); got != tc.want {
			t.Errorf("ServiceActive(%s, %s) = %v, want %v", tc.service, tc.date, got, tc.want)
		}
	}
	if !s.ServiceActive(trip(t, s, "A1"), "20261008") || s.ServiceActive(trip(t, s, "W1"), "20261008") {
		t.Fatal("ServiceActive by trip")
	}
}

func ids(ms []gtfs.Match) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Trip.ID+"@"+m.ServiceDate)
	}
	sort.Strings(out)
	return out
}

func TestCandidates(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	cases := []struct {
		name, stop string
		when       time.Time
		arrival    bool
		want       []string
	}{
		{"parent station id", "8600626", at(2026, 10, 8, 8, 12), false, []string{"A1@20261008", "A2@20261008", "B1@20261008"}},
		{"platform id", "8600626-1", at(2026, 10, 8, 8, 12), false, []string{"A1@20261008", "A2@20261008", "B1@20261008"}},
		{"zero padded api id", "0008600626", at(2026, 10, 8, 8, 12), false, []string{"A1@20261008", "A2@20261008", "B1@20261008"}},
		{"stop_code", "8646", at(2026, 10, 8, 8, 15), false, []string{"A1@20261008"}},
		{"seconds ignored within minute", "8600646", at(2026, 10, 8, 8, 15).Add(45 * time.Second), false, []string{"A1@20261008"}},
		{"arrival time", "8600626", at(2026, 10, 8, 8, 11), true, []string{"A1@20261008"}},
		{"arrival is not departure", "8600626", at(2026, 10, 8, 8, 11), false, nil},
		{"wrong minute", "8600626", at(2026, 10, 8, 8, 13), false, nil},
		{"unknown stop", "999", at(2026, 10, 8, 8, 12), false, nil},
		{"weekday removed friday", "8600626", at(2026, 10, 9, 8, 12), false, nil},
		{"weekend only", "8600626", at(2026, 10, 10, 8, 12), false, []string{"W1@20261010"}},
		{"calendar_dates added", "8600626", at(2026, 10, 8, 9, 0), false, []string{"X1@20261008"}},
		// 00:30 on Friday is 24:30 on Thursday's service day.
		{"after midnight", "8600626", at(2026, 10, 9, 0, 30), false, []string{"A3@20261008"}},
		// 00:30 on Saturday would be Friday's service, which is removed.
		{"after midnight removed day", "8600626", at(2026, 10, 10, 0, 30), false, nil},
		{"input in UTC", "8600695", at(2026, 10, 8, 8, 40).UTC(), true, []string{"B1@20261008"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ids(s.Candidates(tc.stop, tc.when, tc.arrival))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCandidatesPosition(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	for _, m := range s.Candidates("8600626", at(2026, 10, 8, 8, 12), false) {
		if s.Stops[s.StopTimes(m.Trip)[m.Position].Stop].ID != "8600626-1" {
			t.Fatalf("%s position points at the wrong stop", m.Trip.ID)
		}
	}
}

func TestStopIndexesAndSameStop(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	station := s.StopIndexes("8600626")
	if len(station) != 2 {
		t.Fatalf("station should resolve to itself and its platform, got %v", station)
	}
	platform := s.StopIndexes("8600626-1")
	if len(platform) != 1 {
		t.Fatalf("platform = %v", platform)
	}
	if !s.SameStop("8600626", platform[0]) || !s.SameStop("0008600626", platform[0]) {
		t.Fatal("station id should cover its platform")
	}
	if s.SameStop("8600646", platform[0]) {
		t.Fatal("different station")
	}
}

func TestScheduledTimesAndTripEnd(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	ms := s.Candidates("8600626", at(2026, 10, 9, 0, 30), false)
	if len(ms) != 1 {
		t.Fatalf("matches = %v", ids(ms))
	}
	m := ms[0]
	if got := s.ScheduledDeparture(m, m.Position); !got.Equal(at(2026, 10, 9, 0, 30)) {
		t.Fatalf("ScheduledDeparture = %v", got)
	}
	if got := s.ScheduledArrival(m, 1); !got.Equal(at(2026, 10, 9, 1, 5)) {
		t.Fatalf("ScheduledArrival = %v", got)
	}
	if got := s.TripEnd(m); !got.Equal(at(2026, 10, 9, 1, 5)) {
		t.Fatalf("TripEnd = %v", got)
	}
	if got := s.ServiceDayStartOf(m); !got.Equal(at(2026, 10, 8, 0, 0)) {
		t.Fatalf("ServiceDayStartOf = %v", got)
	}
}

func TestLastStopName(t *testing.T) {
	s := load(t, gtfstest.Fixture, stogFilter())
	if got := s.LastStopName(trip(t, s, "A1")); got != "Hillerød" {
		t.Fatalf("headsign = %q", got)
	}
	if got := s.LastStopName(trip(t, s, "A3")); got != "Hillerød St." {
		t.Fatalf("fallback = %q", got)
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

func TestStopCoordinates(t *testing.T) {
	files := gtfstest.With(map[string]string{
		"stops.txt": "stop_id,stop_name,stop_lat,stop_lon\n" +
			"8600626-1,København H,55.672991,12.564502\n" +
			"8600669,Hillerød St.,55.9,12.3\n" + "8600646,Nørreport,0,0\n" + "8600601,Hundige,0,0\n" + "8600695,Høje Taastrup,0,0\n" + "8600626,København H,0,0\n",
	})
	s := load(t, files, stogFilter())
	st := s.Stops[s.StopIndexes("8600626-1")[0]]
	if st.Lat != 55.672991 || st.Lon != 12.564502 {
		t.Fatalf("coords = %v,%v", st.Lat, st.Lon)
	}
}

func TestStopKey(t *testing.T) {
	cases := map[string]string{
		"000052887":    "52887",
		"000008600626": "8600626",
		"52887":        "52887",
		" 0042 ":       "42",
		"000":          "0",
		"":             "",
		"A=1@L=8@":     "A=1@L=8@",
	}
	for in, want := range cases {
		if got := gtfs.StopKey(in); got != want {
			t.Errorf("StopKey(%q) = %q, want %q", in, got, want)
		}
	}
}
