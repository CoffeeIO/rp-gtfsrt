// Package gtfs loads a static GTFS feed into a compact in-memory schedule and
// answers "which scheduled trip is this?" lookups. The national Rejseplanen
// feed has ~5M stop_times, so stop times are stored as 16-byte values in one
// flat slice and stops/trips/routes are referred to by int32 index.
package gtfs

import (
	"archive/zip"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Stop struct {
	ID       string
	Code     string
	Name     string
	Parent   string
	Lat, Lon float64
}

type Route struct {
	ID        string
	AgencyID  string
	ShortName string
	LongName  string
	Type      int
}

// StopTime is one call of a trip at a stop. Times are seconds since the
// service day reference (noon minus 12h local time) and may exceed 24h.
type StopTime struct {
	Stop      int32 // index into Schedule.Stops
	Sequence  int32
	Arrival   int32
	Departure int32
}

type Trip struct {
	ID        string
	Headsign  string
	ShortName string
	Route     int32 // index into Schedule.Routes
	Service   int32 // index into the service table
	first     int32
	count     int32
}

// Filter selects which routes are loaded. Empty sets match everything.
type Filter struct {
	RouteShortNames map[string]bool
	RouteTypes      map[int]bool
}

func (f Filter) matches(r Route) bool {
	if len(f.RouteShortNames) > 0 && !f.RouteShortNames[NormalizeLine(r.ShortName)] {
		return false
	}
	if len(f.RouteTypes) > 0 && !f.RouteTypes[r.Type] {
		return false
	}
	return true
}

type calendar struct {
	weekdays   [7]bool // indexed by time.Weekday
	start, end string  // YYYYMMDD
	defined    bool
}

type Schedule struct {
	Location *time.Location
	Stops    []Stop
	Routes   []Route
	Trips    []Trip

	stopTimes []StopTime
	tripOf    []int32 // stopTimes index -> trip index

	stopByID  map[string]int32   // exact stop_id
	lookup    map[string][]int32 // StopKey(id|code|parent) -> stops (station + platforms)
	routeByID map[string]int32
	tripByID  map[string]int32

	departures [][]int32 // per stop: stopTimes indexes sorted by Departure

	serviceByID map[string]int32
	calendars   []calendar
	exceptions  []map[string]int8 // per service: date -> 1 added / 2 removed

	filter Filter
}

// StopKey makes stop ids from GTFS and the API comparable. The API uses
// "8600626" or zero-padded "000052887"; Rejseplanen GTFS uses "000008600626".
func StopKey(id string) string {
	id = strings.TrimSpace(id)
	trimmed := strings.TrimLeft(id, "0")
	if trimmed == "" && id != "" {
		return "0"
	}
	return trimmed
}

// NormalizeLine makes line names from GTFS and the API comparable.
func NormalizeLine(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// LoadZip loads a GTFS zip file from disk.
func LoadZip(path string, filter Filter, loc *time.Location) (*Schedule, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return Load(&zr.Reader, filter, loc)
}

type pendingStopTime struct {
	trip int32
	st   StopTime
}

// Load reads routes, trips, stops, stop_times and calendars, keeping only
// trips on routes the filter accepts.
func Load(zr *zip.Reader, filter Filter, loc *time.Location) (*Schedule, error) {
	s := &Schedule{
		Location:    loc,
		filter:      filter,
		stopByID:    map[string]int32{},
		lookup:      map[string][]int32{},
		routeByID:   map[string]int32{},
		tripByID:    map[string]int32{},
		serviceByID: map[string]int32{},
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	var pending []pendingStopTime
	steps := []struct {
		name     string
		required bool
		fn       func(row map[string]string) error
	}{
		{"routes.txt", true, s.readRoute},
		{"trips.txt", true, s.readTrip},
		{"stops.txt", true, s.readStop},
		{"stop_times.txt", true, func(row map[string]string) error { return s.readStopTime(row, &pending) }},
		{"calendar.txt", false, s.readCalendar},
		{"calendar_dates.txt", false, s.readCalendarDate},
	}
	for _, step := range steps {
		f, ok := files[step.name]
		if !ok {
			if step.required {
				return nil, fmt.Errorf("gtfs: missing %s", step.name)
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = eachRow(rc, step.fn)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("gtfs: %s: %w", step.name, err)
		}
		if step.name == "routes.txt" && len(s.Routes) == 0 {
			return nil, fmt.Errorf("gtfs: no routes match the filter")
		}
	}
	s.build(pending)
	return s, nil
}

func (s *Schedule) readRoute(row map[string]string) error {
	r := Route{
		ID:        row["route_id"],
		AgencyID:  row["agency_id"],
		ShortName: row["route_short_name"],
		LongName:  row["route_long_name"],
	}
	r.Type, _ = strconv.Atoi(row["route_type"])
	if s.filter.matches(r) {
		s.routeByID[r.ID] = int32(len(s.Routes))
		s.Routes = append(s.Routes, r)
	}
	return nil
}

func (s *Schedule) service(id string) int32 {
	if i, ok := s.serviceByID[id]; ok {
		return i
	}
	i := int32(len(s.calendars))
	s.serviceByID[id] = i
	s.calendars = append(s.calendars, calendar{})
	s.exceptions = append(s.exceptions, nil)
	return i
}

func (s *Schedule) readTrip(row map[string]string) error {
	route, ok := s.routeByID[row["route_id"]]
	if !ok {
		return nil
	}
	s.tripByID[row["trip_id"]] = int32(len(s.Trips))
	s.Trips = append(s.Trips, Trip{
		ID:        row["trip_id"],
		Headsign:  row["trip_headsign"],
		ShortName: row["trip_short_name"],
		Route:     route,
		Service:   s.service(row["service_id"]),
	})
	return nil
}

func (s *Schedule) readStop(row map[string]string) error {
	st := Stop{
		ID:     row["stop_id"],
		Code:   row["stop_code"],
		Name:   row["stop_name"],
		Parent: row["parent_station"],
	}
	st.Lat, _ = strconv.ParseFloat(row["stop_lat"], 64)
	st.Lon, _ = strconv.ParseFloat(row["stop_lon"], 64)
	s.stopByID[st.ID] = int32(len(s.Stops))
	s.Stops = append(s.Stops, st)
	return nil
}

func (s *Schedule) readStopTime(row map[string]string, pending *[]pendingStopTime) error {
	trip, ok := s.tripByID[row["trip_id"]]
	if !ok {
		return nil
	}
	stop, ok := s.stopByID[row["stop_id"]]
	if !ok {
		return fmt.Errorf("unknown stop_id %q", row["stop_id"])
	}
	arr, err := ParseTime(row["arrival_time"])
	if err != nil {
		return err
	}
	dep, err := ParseTime(row["departure_time"])
	if err != nil {
		return err
	}
	if arr < 0 {
		arr = dep
	}
	if dep < 0 {
		dep = arr
	}
	seq, err := strconv.Atoi(row["stop_sequence"])
	if err != nil {
		return fmt.Errorf("bad stop_sequence %q", row["stop_sequence"])
	}
	*pending = append(*pending, pendingStopTime{trip: trip, st: StopTime{
		Stop: stop, Sequence: int32(seq), Arrival: int32(arr), Departure: int32(dep),
	}})
	return nil
}

func (s *Schedule) readCalendar(row map[string]string) error {
	c := calendar{start: row["start_date"], end: row["end_date"], defined: true}
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for i, d := range days {
		c.weekdays[i] = row[d] == "1"
	}
	s.calendars[s.service(row["service_id"])] = c
	return nil
}

func (s *Schedule) readCalendarDate(row map[string]string) error {
	i := s.service(row["service_id"])
	if s.exceptions[i] == nil {
		s.exceptions[i] = map[string]int8{}
	}
	kind, _ := strconv.Atoi(row["exception_type"])
	s.exceptions[i][row["date"]] = int8(kind)
	return nil
}

// build flattens stop times per trip, drops trips without stop times and
// builds the stop lookup and per-stop departure indexes.
func (s *Schedule) build(pending []pendingStopTime) {
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].trip != pending[j].trip {
			return pending[i].trip < pending[j].trip
		}
		return pending[i].st.Sequence < pending[j].st.Sequence
	})
	counts := make([]int32, len(s.Trips))
	for _, p := range pending {
		counts[p.trip]++
	}
	// Re-number trips, dropping those without stop times.
	newIndex := make([]int32, len(s.Trips))
	kept := s.Trips[:0]
	for i, t := range s.Trips {
		if counts[i] == 0 {
			newIndex[i] = -1
			delete(s.tripByID, t.ID)
			continue
		}
		newIndex[i] = int32(len(kept))
		s.tripByID[t.ID] = newIndex[i]
		kept = append(kept, t)
	}
	s.Trips = kept

	s.stopTimes = make([]StopTime, len(pending))
	s.tripOf = make([]int32, len(pending))
	for i, p := range pending {
		trip := newIndex[p.trip]
		s.stopTimes[i] = p.st
		s.tripOf[i] = trip
		t := &s.Trips[trip]
		if t.count == 0 {
			t.first = int32(i)
		}
		t.count++
	}

	for i, st := range s.Stops {
		keys := []string{st.ID, st.Code}
		if st.Parent != "" {
			keys = append(keys, st.Parent)
			if p, ok := s.stopByID[st.Parent]; ok {
				keys = append(keys, s.Stops[p].Code)
			}
		}
		seen := map[string]bool{}
		for _, k := range keys {
			k = StopKey(k)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			s.lookup[k] = append(s.lookup[k], int32(i))
		}
	}

	s.departures = make([][]int32, len(s.Stops))
	for i, st := range s.stopTimes {
		s.departures[st.Stop] = append(s.departures[st.Stop], int32(i))
	}
	for _, d := range s.departures {
		sort.Slice(d, func(a, b int) bool { return s.stopTimes[d[a]].Departure < s.stopTimes[d[b]].Departure })
	}
}

// StopTimes returns the trip's stop times in sequence order.
func (s *Schedule) StopTimes(t *Trip) []StopTime {
	return s.stopTimes[t.first : t.first+t.count]
}

// TripByID returns a trip by its GTFS trip_id.
func (s *Schedule) TripByID(id string) (*Trip, bool) {
	i, ok := s.tripByID[id]
	if !ok {
		return nil, false
	}
	return &s.Trips[i], true
}

// TripIndex returns t's index in Trips.
func (s *Schedule) TripIndex(t *Trip) int {
	return int(s.tripByID[t.ID])
}

// RouteOf returns the trip's route.
func (s *Schedule) RouteOf(t *Trip) *Route {
	return &s.Routes[t.Route]
}

// StopIndexes resolves an API or GTFS stop id (or stop_code, or parent
// station) to the stops it covers. A station resolves to itself and its
// platforms.
func (s *Schedule) StopIndexes(id string) []int32 {
	return s.lookup[StopKey(id)]
}

// SameStop reports whether GTFS stop index i is covered by API stop id.
func (s *Schedule) SameStop(id string, i int32) bool {
	for _, j := range s.lookup[StopKey(id)] {
		if j == i {
			return true
		}
	}
	return false
}

// LastStopName falls back to the final stop's name when a trip has no headsign.
func (s *Schedule) LastStopName(t *Trip) string {
	if t.Headsign != "" {
		return t.Headsign
	}
	sts := s.StopTimes(t)
	if len(sts) == 0 {
		return ""
	}
	return s.Stops[sts[len(sts)-1].Stop].Name
}

// ServiceActive reports whether the trip's service runs on date (YYYYMMDD).
func (s *Schedule) ServiceActive(t *Trip, date string) bool {
	return s.serviceActive(t.Service, date)
}

// ServiceActiveByID is ServiceActive for a GTFS service_id.
func (s *Schedule) ServiceActiveByID(serviceID, date string) bool {
	i, ok := s.serviceByID[serviceID]
	return ok && s.serviceActive(i, date)
}

func (s *Schedule) serviceActive(i int32, date string) bool {
	if kind, ok := s.exceptions[i][date]; ok {
		return kind == 1
	}
	c := s.calendars[i]
	if !c.defined || date < c.start || date > c.end {
		return false
	}
	d, err := time.ParseInLocation("20060102", date, time.UTC)
	if err != nil {
		return false
	}
	return c.weekdays[d.Weekday()]
}

// Match is a scheduled trip instance found for an observation.
type Match struct {
	Trip        *Trip
	Position    int    // index into StopTimes(Trip) of the observed stop
	ServiceDate string // YYYYMMDD
}

// Candidates returns active trips scheduled to depart stopID (API or GTFS id)
// in the same minute as scheduled (wall clock). Both the calendar day and the
// previous day are checked, because GTFS times past 24:00 belong to the
// previous service day. When arrival is true, arrival times are compared
// instead (for the last stop of a trip).
func (s *Schedule) Candidates(stopID string, scheduled time.Time, arrival bool) []Match {
	scheduled = scheduled.In(s.Location)
	var out []Match
	for _, back := range []int{0, -1} {
		day := time.Date(scheduled.Year(), scheduled.Month(), scheduled.Day()+back, 0, 0, 0, 0, s.Location)
		date := day.Format("20060102")
		minute := int32(scheduled.Sub(ServiceDayStart(day)).Seconds()) / 60
		for _, stop := range s.StopIndexes(stopID) {
			deps := s.departures[stop]
			// departures are sorted by Departure; arrival lookups scan the
			// window just before it (dwell is short), so search a bit earlier.
			lo := sort.Search(len(deps), func(i int) bool {
				return s.stopTimes[deps[i]].Departure/60 >= minute-60
			})
			for _, idx := range deps[lo:] {
				st := s.stopTimes[idx]
				if st.Departure/60 > minute+60 {
					break
				}
				t := st.Departure
				if arrival {
					t = st.Arrival
				}
				if t/60 != minute {
					continue
				}
				trip := &s.Trips[s.tripOf[idx]]
				if !s.ServiceActive(trip, date) {
					continue
				}
				out = append(out, Match{Trip: trip, Position: int(idx - trip.first), ServiceDate: date})
			}
		}
	}
	return out
}

// ServiceDayStartOf returns the reference time of a match's service day.
func (s *Schedule) ServiceDayStartOf(m Match) time.Time {
	day, _ := time.ParseInLocation("20060102", m.ServiceDate, s.Location)
	return ServiceDayStart(day)
}

// ScheduledDeparture returns the absolute scheduled departure at position i.
func (s *Schedule) ScheduledDeparture(m Match, i int) time.Time {
	return s.ServiceDayStartOf(m).Add(time.Duration(s.StopTimes(m.Trip)[i].Departure) * time.Second)
}

// ScheduledArrival returns the absolute scheduled arrival at position i.
func (s *Schedule) ScheduledArrival(m Match, i int) time.Time {
	return s.ServiceDayStartOf(m).Add(time.Duration(s.StopTimes(m.Trip)[i].Arrival) * time.Second)
}

// TripEnd returns the absolute time of the trip's last arrival.
func (s *Schedule) TripEnd(m Match) time.Time {
	return s.ScheduledArrival(m, int(m.Trip.count)-1)
}

// FindStops does a case-insensitive substring search on stop names.
func (s *Schedule) FindStops(query string) []Stop {
	q := strings.ToLower(query)
	var out []Stop
	for _, st := range s.Stops {
		if strings.Contains(strings.ToLower(st.Name), q) {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stats summarizes the loaded schedule.
type Stats struct {
	Routes, Trips, Stops, StopTimes int
}

func (s *Schedule) Stats() Stats {
	return Stats{Routes: len(s.Routes), Trips: len(s.Trips), Stops: len(s.Stops), StopTimes: len(s.stopTimes)}
}
