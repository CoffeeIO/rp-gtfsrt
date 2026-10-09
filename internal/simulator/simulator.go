// Package simulator serves a fake Rejseplanen API (departureBoard,
// journeyDetail, himsearch) generated from a static GTFS feed, with
// deterministic per-journey delays and cancellations. It exists so the
// bridge can be load-tested at national scale and in CI without an API key.
//
// Response shapes follow the captured live responses in
// internal/rejseplanen/testdata.
package simulator

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
)

type Simulator struct {
	Schedule *gtfs.Schedule
	Now      func() time.Time
	// CancelRate is the share of journeys that are cancelled (0..1).
	CancelRate float64
	// DelayRate is the share of journeys running late (0..1).
	DelayRate float64
	Seed      uint64
}

func (s *Simulator) now() time.Time {
	if s.Now != nil {
		return s.Now().In(s.Schedule.Location)
	}
	return time.Now().In(s.Schedule.Location)
}

func (s *Simulator) hash(parts ...string) uint64 {
	h := fnv.New64a()
	fmt.Fprint(h, s.Seed)
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return h.Sum64()
}

// journey is a trip instance on a service date.
type journey struct {
	trip int
	date string // YYYYMMDD
}

func (j journey) ref(s *gtfs.Schedule) string {
	day, _ := time.Parse("20060102", j.date)
	return fmt.Sprintf("2|#VN#1#ST#1#PI#0#ZI#%d#TA#0#DA#%s#SIM#%s#", j.trip, day.Format("020106"), s.Trips[j.trip].ID)
}

func parseRef(ref string) (journey, bool) {
	parts := strings.Split(ref, "#")
	var j journey
	var zi, da string
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "ZI":
			zi = parts[i+1]
		case "DA":
			da = parts[i+1]
		}
	}
	trip, err := strconv.Atoi(zi)
	day, err2 := time.Parse("020106", da)
	if err != nil || err2 != nil {
		return j, false
	}
	return journey{trip: trip, date: day.Format("20060102")}, true
}

// baseDelay is the journey's delay at its last stop; it ramps up from half
// that at the first stop.
func (s *Simulator) baseDelay(j journey) time.Duration {
	r := float64(s.hash("delay", strconv.Itoa(j.trip), j.date)%10000) / 10000
	if r >= s.DelayRate {
		return 0
	}
	minutes := 1 + s.hash("mag", strconv.Itoa(j.trip), j.date)%10
	if s.hash("big", strconv.Itoa(j.trip), j.date)%10 == 0 {
		minutes += 10 + s.hash("bigmag", strconv.Itoa(j.trip), j.date)%20
	}
	return time.Duration(minutes) * time.Minute
}

func (s *Simulator) cancelled(j journey) bool {
	return float64(s.hash("cancel", strconv.Itoa(j.trip), j.date)%10000)/10000 < s.CancelRate
}

func (s *Simulator) delayAt(j journey, pos, count int) time.Duration {
	base := s.baseDelay(j)
	if base == 0 || count < 2 {
		return base
	}
	d := base/2 + base/2*time.Duration(pos)/time.Duration(count-1)
	return max(d.Round(time.Minute), time.Minute)
}

func (s *Simulator) match(j journey) gtfs.Match {
	return gtfs.Match{Trip: &s.Schedule.Trips[j.trip], ServiceDate: j.date}
}

// started reports whether the journey has a prognosis yet (from 30 minutes
// before its first departure).
func (s *Simulator) started(j journey, now time.Time) bool {
	return !now.Before(s.Schedule.ScheduledDeparture(s.match(j), 0).Add(-30 * time.Minute))
}

func (s *Simulator) apiStopID(stop int32) string {
	st := s.Schedule.Stops[stop]
	if st.Parent != "" {
		return gtfs.StopKey(st.Parent)
	}
	return gtfs.StopKey(st.ID)
}

func (s *Simulator) stationName(stop int32) string {
	st := s.Schedule.Stops[stop]
	if st.Parent != "" {
		for _, i := range s.Schedule.StopIndexes(st.Parent) {
			if gtfs.StopKey(s.Schedule.Stops[i].ID) == gtfs.StopKey(st.Parent) {
				return s.Schedule.Stops[i].Name
			}
		}
	}
	return st.Name
}

func clock(t time.Time) string { return t.Format("15:04:05") }
func date(t time.Time) string  { return t.Format("2006-01-02") }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// Handler serves the fake API under any prefix ending in the endpoint name.
func (s *Simulator) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accessId") == "" {
			writeJSON(w, map[string]string{"errorCode": "API_AUTH", "errorText": "missing accessId"})
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/departureBoard"):
			s.departureBoard(w, r)
		case strings.HasSuffix(r.URL.Path, "/journeyDetail"):
			s.journeyDetail(w, r)
		case strings.HasSuffix(r.URL.Path, "/himsearch"):
			s.himsearch(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

type boardRow struct {
	sched time.Time
	row   map[string]any
}

func (s *Simulator) departureBoard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	duration, _ := strconv.Atoi(q.Get("duration"))
	if duration <= 0 {
		duration = 60
	}
	maxJourneys, _ := strconv.Atoi(q.Get("maxJourneys"))
	if maxJourneys == 0 {
		maxJourneys = 20
	}
	now := s.now()
	until := now.Add(time.Duration(duration) * time.Minute)
	sched := s.Schedule
	var rows []boardRow
	// Departures in the window, found via the schedule's per-minute lookup.
	seen := map[string]bool{}
	for t := now.Truncate(time.Minute); !t.After(until); t = t.Add(time.Minute) {
		for _, m := range sched.Candidates(id, t, false) {
			sts := sched.StopTimes(m.Trip)
			if m.Position == len(sts)-1 {
				continue // arrival only
			}
			trip := sched.TripIndex(m.Trip)
			j := journey{trip: trip, date: m.ServiceDate}
			key := fmt.Sprint(trip, m.ServiceDate, m.Position)
			if seen[key] {
				continue
			}
			seen[key] = true
			dep := sched.ScheduledDeparture(m, m.Position)
			if dep.Before(now) {
				continue
			}
			route := sched.RouteOf(m.Trip)
			row := map[string]any{
				"name":             route.ShortName,
				"type":             strconv.Itoa(route.Type),
				"stop":             s.stationName(sts[m.Position].Stop),
				"stopExtId":        s.apiStopID(sts[m.Position].Stop),
				"time":             clock(dep),
				"date":             date(dep),
				"direction":        sched.LastStopName(m.Trip),
				"JourneyDetailRef": map[string]string{"ref": j.ref(sched)},
				"ProductAtStop":    map[string]string{"name": route.ShortName, "line": route.ShortName},
			}
			s.addRealtime(row, j, m.Position, len(sts), dep, now)
			rows = append(rows, boardRow{dep, row})
		}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].sched.Before(rows[b].sched) })
	if maxJourneys > 0 && len(rows) > maxJourneys {
		rows = rows[:maxJourneys]
	}
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = r.row
	}
	writeJSON(w, map[string]any{"Departure": out})
}

// addRealtime adds prognosis, rtTime and cancellation fields the way the
// live API does: rtTime only when it differs from the schedule.
func (s *Simulator) addRealtime(row map[string]any, j journey, pos, count int, sched time.Time, now time.Time) {
	if s.cancelled(j) {
		row["cancelled"] = true
		row["reachable"] = false
		return
	}
	if !s.started(j, now) {
		return
	}
	row["prognosisType"] = "PROGNOSED"
	if d := s.delayAt(j, pos, count); d != 0 {
		rtT := sched.Add(d)
		row["rtTime"], row["rtDate"] = clock(rtT), date(rtT)
	}
}

func (s *Simulator) journeyDetail(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("id")
	j, ok := parseRef(ref)
	if !ok || j.trip < 0 || j.trip >= len(s.Schedule.Trips) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"errorCode": "SVC_PARAM", "errorText": "unknown journey"})
		return
	}
	sched := s.Schedule
	now := s.now()
	m := s.match(j)
	sts := sched.StopTimes(m.Trip)
	route := sched.RouteOf(m.Trip)
	stops := make([]map[string]any, len(sts))
	var lastPos map[string]float64
	for p, st := range sts {
		stop := sched.Stops[st.Stop]
		row := map[string]any{
			"extId":    s.apiStopID(st.Stop),
			"name":     s.stationName(st.Stop),
			"routeIdx": p,
			"lat":      stop.Lat,
			"lon":      stop.Lon,
		}
		if p > 0 {
			arr := sched.ScheduledArrival(m, p)
			row["arrTime"], row["arrDate"] = clock(arr), date(arr)
			s.addCall(row, j, p, len(sts), arr, "Arr", now)
		}
		if p < len(sts)-1 {
			dep := sched.ScheduledDeparture(m, p)
			row["depTime"], row["depDate"] = clock(dep), date(dep)
			s.addCall(row, j, p, len(sts), dep, "Dep", now)
			if !s.cancelled(j) && !dep.Add(s.delayAt(j, p, len(sts))).After(now) {
				lastPos = map[string]float64{"lat": stop.Lat, "lon": stop.Lon}
			}
		}
		stops[p] = row
	}
	body := map[string]any{
		"ref":            ref,
		"dayOfOperation": date(sched.ServiceDayStartOf(m).Add(12 * time.Hour)),
		"Product":        []map[string]string{{"name": route.ShortName, "line": route.ShortName}},
		"Directions":     map[string]any{"Direction": []map[string]string{{"value": sched.LastStopName(m.Trip)}}},
		"Stops":          map[string]any{"Stop": stops},
	}
	if s.cancelled(j) {
		body["cancelled"] = true
	}
	if lastPos != nil {
		body["lastPos"] = lastPos
	}
	writeJSON(w, body)
}

func (s *Simulator) addCall(row map[string]any, j journey, pos, count int, sched time.Time, kind string, now time.Time) {
	if s.cancelled(j) {
		row["cancelled"+map[string]string{"Arr": "Arrival", "Dep": "Departure"}[kind]] = true
		return
	}
	if !s.started(j, now) {
		return
	}
	row[strings.ToLower(kind)+"PrognosisType"] = "PROGNOSED"
	if d := s.delayAt(j, pos, count); d != 0 {
		rtT := sched.Add(d)
		row["rt"+kind+"Time"], row["rt"+kind+"Date"] = clock(rtT), date(rtT)
	}
}

// himsearch returns two synthetic alerts on the first stations of the feed.
func (s *Simulator) himsearch(w http.ResponseWriter, _ *http.Request) {
	now := s.now()
	var msgs []map[string]any
	for i, st := range s.Schedule.Stops {
		if len(msgs) == 2 {
			break
		}
		if st.Parent != "" {
			continue
		}
		msgs = append(msgs, map[string]any{
			"id":       fmt.Sprintf("SIM_HIM_%d", i),
			"act":      true,
			"head":     "Simuleret driftsforstyrrelse ved " + st.Name,
			"text":     "Dette er en simuleret meddelelse til test af rp-gtfsrt.",
			"priority": 50,
			"products": 0,
			"sDate":    date(now.Add(-time.Hour)), "sTime": clock(now.Add(-time.Hour)),
			"eDate": date(now.Add(24 * time.Hour)), "eTime": clock(now.Add(24 * time.Hour)),
			"affectedStops": map[string]any{"StopLocation": []map[string]string{{"extId": gtfs.StopKey(st.ID), "name": st.Name}}},
		})
	}
	writeJSON(w, map[string]any{"Message": msgs})
}
