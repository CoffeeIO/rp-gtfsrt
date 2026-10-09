package rejseplanen

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Journey is a normalized journeyDetail response: one vehicle trip with its
// full stop list.
type Journey struct {
	Ref       string
	Line      string
	Direction string
	Cancelled bool
	Stops     []JourneyStop
	Position  *Position // last known vehicle position
}

// JourneyStop is one call of a journey. Scheduled times are nil where the
// stop has no arrival (first stop) or departure (last stop). Realtime times
// are nil without a prediction, and equal the scheduled time when the API
// reports a prognosis but omits rtTime (on time).
type JourneyStop struct {
	StopID             string
	Name               string
	Index              int // routeIdx
	ScheduledArrival   *time.Time
	ScheduledDeparture *time.Time
	RealtimeArrival    *time.Time
	RealtimeDeparture  *time.Time
	ArrivalCancelled   bool
	DepartureCancelled bool
	Track              string
}

// Cancelled reports whether the train does not call here at all.
func (s JourneyStop) Cancelled() bool {
	hasArr, hasDep := s.ScheduledArrival != nil, s.ScheduledDeparture != nil
	return (!hasArr || s.ArrivalCancelled) && (!hasDep || s.DepartureCancelled) && (hasArr || hasDep)
}

type Position struct {
	Lat, Lon float64
}

// Key identifies the journey instance, see KeyFromRef.
func (j Journey) Key() string {
	if k := KeyFromRef(j.Ref); k != "" {
		return k
	}
	return "ref:" + j.Ref
}

// KeyFromRef extracts the journey number (ZI) and day (DA) from a HAFAS
// journey reference like "2|#VN#1#...#ZI#58845#TA#0#DA#91026#...", giving
// "58845@91026". The same journey on the same day always has the same key,
// while the full ref also encodes the timetable version.
func KeyFromRef(ref string) string {
	parts := strings.Split(ref, "#")
	var zi, da string
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "ZI":
			zi = parts[i+1]
		case "DA":
			da = parts[i+1]
		}
	}
	if zi == "" || da == "" {
		return ""
	}
	return zi + "@" + da
}

type rawJourneyStop struct {
	ExtID              string   `json:"extId"`
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	RouteIdx           int      `json:"routeIdx"`
	ArrTime            string   `json:"arrTime"`
	ArrDate            string   `json:"arrDate"`
	RtArrTime          string   `json:"rtArrTime"`
	RtArrDate          string   `json:"rtArrDate"`
	ArrPrognosisType   string   `json:"arrPrognosisType"`
	DepTime            string   `json:"depTime"`
	DepDate            string   `json:"depDate"`
	RtDepTime          string   `json:"rtDepTime"`
	RtDepDate          string   `json:"rtDepDate"`
	DepPrognosisType   string   `json:"depPrognosisType"`
	CancelledArrival   flexBool `json:"cancelledArrival"`
	CancelledDeparture flexBool `json:"cancelledDeparture"`
	Cancelled          flexBool `json:"cancelled"`
	ArrTrack           string   `json:"arrTrack"`
	DepTrack           string   `json:"depTrack"`
	RtArrTrack         string   `json:"rtArrTrack"`
	RtDepTrack         string   `json:"rtDepTrack"`
}

type rawJourney struct {
	apiError
	Ref        string          `json:"ref"`
	Cancelled  flexBool        `json:"cancelled"`
	Product    json.RawMessage `json:"Product"`
	Directions *struct {
		Direction json.RawMessage `json:"Direction"`
	} `json:"Directions"`
	Stops *struct {
		Stop json.RawMessage `json:"Stop"`
	} `json:"Stops"`
	LastPos *struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"lastPos"`
	DayOfOperation string `json:"dayOfOperation"`
}

// ParseJourneyDetail decodes a journeyDetail response.
func ParseJourneyDetail(body []byte, loc *time.Location) (Journey, error) {
	var raw rawJourney
	if err := json.Unmarshal(body, &raw); err != nil {
		return Journey{}, fmt.Errorf("rejseplanen: decode journey: %w", err)
	}
	if raw.apiError != (apiError{}) {
		return Journey{}, fmt.Errorf("rejseplanen: api error %s: %s%s", raw.ErrorCode, raw.ErrorText, raw.Error)
	}
	j := Journey{Ref: raw.Ref, Cancelled: bool(raw.Cancelled)}
	if products, err := oneOrMany[rawProduct](raw.Product); err == nil {
		for _, p := range products {
			if l := firstNonEmpty(p.Line, p.Name); l != "" {
				j.Line = l
				break
			}
		}
	}
	if raw.Directions != nil {
		dirs, _ := oneOrMany[struct {
			Value string `json:"value"`
		}](raw.Directions.Direction)
		if len(dirs) > 0 {
			j.Direction = dirs[0].Value
		}
	}
	if raw.LastPos != nil && (raw.LastPos.Lat != 0 || raw.LastPos.Lon != 0) {
		j.Position = &Position{Lat: raw.LastPos.Lat, Lon: raw.LastPos.Lon}
	}
	if raw.Stops == nil {
		return Journey{}, fmt.Errorf("rejseplanen: journey has no stops")
	}
	stops, err := oneOrMany[rawJourneyStop](raw.Stops.Stop)
	if err != nil {
		return Journey{}, fmt.Errorf("rejseplanen: decode journey stops: %w", err)
	}
	for _, rs := range stops {
		s, err := rs.normalize(raw.DayOfOperation, loc)
		if err != nil {
			return Journey{}, err
		}
		j.Stops = append(j.Stops, s)
	}
	if len(j.Stops) == 0 {
		return Journey{}, fmt.Errorf("rejseplanen: journey has no stops")
	}
	return j, nil
}

func (r rawJourneyStop) normalize(day string, loc *time.Location) (JourneyStop, error) {
	s := JourneyStop{
		StopID:             firstNonEmpty(r.ExtID, extIDFromHafasID(r.ID)),
		Name:               r.Name,
		Index:              r.RouteIdx,
		ArrivalCancelled:   bool(r.CancelledArrival) || bool(r.Cancelled),
		DepartureCancelled: bool(r.CancelledDeparture) || bool(r.Cancelled),
		Track:              firstNonEmpty(r.RtDepTrack, r.RtArrTrack, r.DepTrack, r.ArrTrack),
	}
	var err error
	if s.ScheduledArrival, s.RealtimeArrival, err = callTimes(r.ArrDate, r.ArrTime, r.RtArrDate, r.RtArrTime, r.ArrPrognosisType, day, loc); err != nil {
		return s, err
	}
	if s.ScheduledDeparture, s.RealtimeDeparture, err = callTimes(r.DepDate, r.DepTime, r.RtDepDate, r.RtDepTime, r.DepPrognosisType, day, loc); err != nil {
		return s, err
	}
	return s, nil
}

// callTimes parses one scheduled/realtime pair. The first stop of a journey
// has no arrival date, so the journey's day of operation is the fallback.
func callTimes(date, clock, rtDate, rtClock, prognosis, day string, loc *time.Location) (*time.Time, *time.Time, error) {
	if clock == "" {
		return nil, nil, nil
	}
	sched, err := parseDateTime(firstNonEmpty(date, day), clock, loc)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case rtClock != "":
		rt, err := parseDateTime(firstNonEmpty(rtDate, date, day), rtClock, loc)
		if err != nil {
			return nil, nil, err
		}
		return &sched, &rt, nil
	case prognosis != "":
		onTime := sched
		return &sched, &onTime, nil
	}
	return &sched, nil, nil
}
