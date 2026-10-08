// Package gtfs loads the subset of a static GTFS feed the bridge needs and
// answers "which scheduled trip is this departure?" lookups.
package gtfs

import (
	"archive/zip"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Stop struct {
	ID     string
	Code   string
	Name   string
	Parent string
}

type Route struct {
	ID        string
	ShortName string
	Type      int
}

type StopTime struct {
	StopID   string
	Sequence int
	// Arrival and Departure are seconds since the service day reference
	// (noon minus 12h local time) and may exceed 24h.
	Arrival   int
	Departure int
}

type Trip struct {
	ID        string
	RouteID   string
	ServiceID string
	Headsign  string
	StopTimes []StopTime
}

// LastStopName falls back to the final stop's name when a trip has no headsign.
func (s *Schedule) LastStopName(t *Trip) string {
	if t.Headsign != "" {
		return t.Headsign
	}
	if len(t.StopTimes) == 0 {
		return ""
	}
	return s.Stops[t.StopTimes[len(t.StopTimes)-1].StopID].Name
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
}

type indexEntry struct {
	trip    *Trip
	stopIdx int
}

type Schedule struct {
	Location *time.Location
	Stops    map[string]Stop
	Routes   map[string]Route
	Trips    map[string]*Trip

	filter     Filter
	calendars  map[string]calendar
	exceptions map[string]map[string]int // service -> date -> 1 added / 2 removed
	byStop     map[string][]indexEntry   // stop_id, stop_code and parent_station keys
}

// StopKey makes stop ids from GTFS and the API comparable. The API uses
// zero-padded ids like "000052887"; GTFS may not.
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

// Load reads routes, trips, stop_times, stops and calendars, keeping only
// trips on routes the filter accepts.
func Load(zr *zip.Reader, filter Filter, loc *time.Location) (*Schedule, error) {
	s := &Schedule{
		Location:   loc,
		filter:     filter,
		Stops:      map[string]Stop{},
		Routes:     map[string]Route{},
		Trips:      map[string]*Trip{},
		calendars:  map[string]calendar{},
		exceptions: map[string]map[string]int{},
		byStop:     map[string][]indexEntry{},
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	steps := []struct {
		name     string
		required bool
		fn       func(row map[string]string) error
	}{
		{"routes.txt", true, s.readRoute},
		{"trips.txt", true, s.readTrip},
		{"stop_times.txt", true, s.readStopTime},
		{"stops.txt", true, s.readStop},
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
	s.buildIndex()
	return s, nil
}

func (s *Schedule) readRoute(row map[string]string) error {
	r := Route{ID: row["route_id"], ShortName: row["route_short_name"]}
	fmt.Sscan(row["route_type"], &r.Type)
	if s.filter.matches(r) {
		s.Routes[r.ID] = r
	}
	return nil
}

func (s *Schedule) readTrip(row map[string]string) error {
	if _, ok := s.Routes[row["route_id"]]; !ok {
		return nil
	}
	s.Trips[row["trip_id"]] = &Trip{
		ID:        row["trip_id"],
		RouteID:   row["route_id"],
		ServiceID: row["service_id"],
		Headsign:  row["trip_headsign"],
	}
	return nil
}

func (s *Schedule) readStopTime(row map[string]string) error {
	t, ok := s.Trips[row["trip_id"]]
	if !ok {
		return nil
	}
	st := StopTime{StopID: row["stop_id"]}
	var err error
	if st.Arrival, err = ParseTime(row["arrival_time"]); err != nil {
		return err
	}
	if st.Departure, err = ParseTime(row["departure_time"]); err != nil {
		return err
	}
	if st.Arrival < 0 {
		st.Arrival = st.Departure
	}
	if st.Departure < 0 {
		st.Departure = st.Arrival
	}
	if _, err := fmt.Sscan(row["stop_sequence"], &st.Sequence); err != nil {
		return fmt.Errorf("bad stop_sequence %q", row["stop_sequence"])
	}
	t.StopTimes = append(t.StopTimes, st)
	return nil
}

func (s *Schedule) readStop(row map[string]string) error {
	s.Stops[row["stop_id"]] = Stop{
		ID:     row["stop_id"],
		Code:   row["stop_code"],
		Name:   row["stop_name"],
		Parent: row["parent_station"],
	}
	return nil
}

func (s *Schedule) readCalendar(row map[string]string) error {
	c := calendar{start: row["start_date"], end: row["end_date"]}
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for i, d := range days {
		c.weekdays[i] = row[d] == "1"
	}
	s.calendars[row["service_id"]] = c
	return nil
}

func (s *Schedule) readCalendarDate(row map[string]string) error {
	id := row["service_id"]
	if s.exceptions[id] == nil {
		s.exceptions[id] = map[string]int{}
	}
	var kind int
	fmt.Sscan(row["exception_type"], &kind)
	s.exceptions[id][row["date"]] = kind
	return nil
}

func (s *Schedule) buildIndex() {
	for id, t := range s.Trips {
		if len(t.StopTimes) == 0 {
			delete(s.Trips, id)
			continue
		}
		sort.Slice(t.StopTimes, func(i, j int) bool { return t.StopTimes[i].Sequence < t.StopTimes[j].Sequence })
		for i, st := range t.StopTimes {
			e := indexEntry{trip: t, stopIdx: i}
			stop := s.Stops[st.StopID]
			keys := []string{st.StopID}
			if stop.Code != "" && stop.Code != st.StopID {
				keys = append(keys, stop.Code)
			}
			if stop.Parent != "" {
				keys = append(keys, stop.Parent)
				if parent, ok := s.Stops[stop.Parent]; ok && parent.Code != "" && parent.Code != stop.Parent {
					keys = append(keys, parent.Code)
				}
			}
			seenKeys := map[string]bool{}
			for _, k := range keys {
				k = StopKey(k)
				if seenKeys[k] {
					continue
				}
				seenKeys[k] = true
				s.byStop[k] = append(s.byStop[k], e)
			}
		}
	}
}

// ServiceActive reports whether serviceID runs on date (YYYYMMDD).
func (s *Schedule) ServiceActive(serviceID, date string) bool {
	if kind, ok := s.exceptions[serviceID][date]; ok {
		return kind == 1
	}
	c, ok := s.calendars[serviceID]
	if !ok || date < c.start || date > c.end {
		return false
	}
	d, err := time.ParseInLocation("20060102", date, time.UTC)
	if err != nil {
		return false
	}
	return c.weekdays[d.Weekday()]
}

// Match is a scheduled trip found for an observed departure.
type Match struct {
	Trip        *Trip
	StopIndex   int
	ServiceDate string // YYYYMMDD
}

// Candidates returns active trips on the given line that are scheduled to
// depart stopID at the same minute as scheduled (local wall-clock time).
// Both the calendar day and the previous day are checked, because GTFS times
// past 24:00 belong to the previous service day.
func (s *Schedule) Candidates(stopID, line string, scheduled time.Time) []Match {
	line = NormalizeLine(line)
	scheduled = scheduled.In(s.Location)
	var out []Match
	seen := map[string]bool{}
	for _, back := range []int{0, -1} {
		day := time.Date(scheduled.Year(), scheduled.Month(), scheduled.Day()+back, 0, 0, 0, 0, s.Location)
		date := day.Format("20060102")
		secs := int(scheduled.Sub(ServiceDayStart(day)).Seconds())
		for _, e := range s.byStop[StopKey(stopID)] {
			st := e.trip.StopTimes[e.stopIdx]
			if st.Departure/60 != secs/60 {
				continue
			}
			if NormalizeLine(s.Routes[e.trip.RouteID].ShortName) != line {
				continue
			}
			if !s.ServiceActive(e.trip.ServiceID, date) {
				continue
			}
			key := e.trip.ID + "|" + date
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Match{Trip: e.trip, StopIndex: e.stopIdx, ServiceDate: date})
		}
	}
	return out
}

// FindStops does a case-insensitive substring search on stop names, for the
// `stops` CLI helper used to pick hub stop IDs.
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

// TripEnd returns the absolute time of the trip's last arrival.
func (s *Schedule) TripEnd(m Match) time.Time {
	day, _ := time.ParseInLocation("20060102", m.ServiceDate, s.Location)
	last := m.Trip.StopTimes[len(m.Trip.StopTimes)-1]
	return ServiceDayStart(day).Add(time.Duration(last.Arrival) * time.Second)
}

// ScheduledAt returns the absolute scheduled departure at stop index i.
func (s *Schedule) ScheduledAt(m Match, i int) time.Time {
	day, _ := time.ParseInLocation("20060102", m.ServiceDate, s.Location)
	return ServiceDayStart(day).Add(time.Duration(m.Trip.StopTimes[i].Departure) * time.Second)
}
