// Package rejseplanen talks to the Rejseplanen Labs API (HAFAS ReST):
// departureBoard, journeyDetail and himsearch.
//
// The decoder accepts both the HAFAS ReST (API 2.0) JSON shape
// ({"Departure":[{"time":"08:12:00","date":"2026-10-08",...}]}) and the
// legacy 1.0 shape ({"DepartureBoard":{"Departure":{...}}} with
// "time":"08:12","date":"08.10.26"). Everything API-specific lives in this
// file so it can be corrected against a captured live response.
// The live shape was confirmed on 2026-10-08 (testdata/departureboard_live_*.json).
package rejseplanen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Departure is one normalized row of a departure board.
type Departure struct {
	StopID    string
	Line      string
	Direction string
	Track     string
	Scheduled time.Time
	// Realtime is nil when the API has no prediction. A prediction equal to
	// the schedule (prognosisType set, rtTime omitted) is Scheduled.
	Realtime  *time.Time
	Cancelled bool
	// JourneyRef is the journeyDetail reference for this departure, if any.
	JourneyRef string
}

// JourneyKey identifies the journey instance (see KeyFromRef). Without a
// ref it falls back to stop, line and scheduled time.
func (d Departure) JourneyKey() string {
	if k := KeyFromRef(d.JourneyRef); k != "" {
		return k
	}
	return "dep:" + d.StopID + "|" + d.Line + "|" + d.Scheduled.Format(time.RFC3339)
}

// Delay is realtime minus scheduled; zero without a prediction.
func (d Departure) Delay() time.Duration {
	if d.Realtime == nil {
		return 0
	}
	return d.Realtime.Sub(d.Scheduled)
}

type rawProduct struct {
	Name string `json:"name"`
	Line string `json:"line"`
}

type rawDeparture struct {
	Name          string   `json:"name"`
	Line          string   `json:"line"`
	StopID        string   `json:"stopid"`
	StopExtID     string   `json:"stopExtId"`
	Time          string   `json:"time"`
	Date          string   `json:"date"`
	RtTime        string   `json:"rtTime"`
	RtDate        string   `json:"rtDate"`
	Direction     string   `json:"direction"`
	FinalStop     string   `json:"finalStop"`
	Track         string   `json:"track"`
	RtTrack       string   `json:"rtTrack"`
	PrognosisType string   `json:"prognosisType"`
	Cancelled     flexBool `json:"cancelled"`
	JourneyDetail *struct {
		Ref string `json:"ref"`
	} `json:"JourneyDetailRef"`
	ProductAtStop *rawProduct     `json:"ProductAtStop"`
	Product       json.RawMessage `json:"Product"`
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(strings.EqualFold(s, "true"))
	return nil
}

// oneOrMany decodes either a JSON object or an array of them.
func oneOrMany[T any](data json.RawMessage) ([]T, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	if data[0] == '[' {
		var many []T
		err := json.Unmarshal(data, &many)
		return many, err
	}
	var one T
	if err := json.Unmarshal(data, &one); err != nil {
		return nil, err
	}
	return []T{one}, nil
}

// apiError covers HAFAS ReST error bodies, e.g. {"errorCode":"API_AUTH","errorText":"..."}.
type apiError struct {
	ErrorCode string `json:"errorCode"`
	ErrorText string `json:"errorText"`
	Error     string `json:"error"`
}

// ParseDepartureBoard decodes a departure board response. requestedStop is
// used when rows do not carry their own stop id.
func ParseDepartureBoard(body []byte, requestedStop string, loc *time.Location) ([]Departure, error) {
	var top struct {
		apiError
		Departure      json.RawMessage `json:"Departure"`
		DepartureBoard *struct {
			apiError
			Departure json.RawMessage `json:"Departure"`
		} `json:"DepartureBoard"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("rejseplanen: decode: %w", err)
	}
	rawList := top.Departure
	errInfo := top.apiError
	if top.DepartureBoard != nil {
		if len(rawList) == 0 {
			rawList = top.DepartureBoard.Departure
		}
		if errInfo == (apiError{}) {
			errInfo = top.DepartureBoard.apiError
		}
	}
	if errInfo != (apiError{}) {
		return nil, fmt.Errorf("rejseplanen: api error %s: %s%s", errInfo.ErrorCode, errInfo.ErrorText, errInfo.Error)
	}
	raws, err := oneOrMany[rawDeparture](rawList)
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: decode departures: %w", err)
	}
	out := make([]Departure, 0, len(raws))
	for _, r := range raws {
		d, err := r.normalize(requestedStop, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (r rawDeparture) normalize(requestedStop string, loc *time.Location) (Departure, error) {
	sched, err := parseDateTime(r.Date, r.Time, loc)
	if err != nil {
		return Departure{}, err
	}
	d := Departure{
		StopID:    firstNonEmpty(r.StopExtID, extIDFromHafasID(r.StopID), requestedStop),
		Line:      r.line(),
		Direction: firstNonEmpty(r.Direction, r.FinalStop),
		Track:     firstNonEmpty(r.RtTrack, r.Track),
		Scheduled: sched,
		Cancelled: bool(r.Cancelled),
	}
	if r.JourneyDetail != nil {
		d.JourneyRef = r.JourneyDetail.Ref
	}
	if r.RtTime != "" {
		rt, err := parseDateTime(firstNonEmpty(r.RtDate, r.Date), r.RtTime, loc)
		if err != nil {
			return Departure{}, err
		}
		d.Realtime = &rt
	} else if r.PrognosisType != "" {
		// Live API: every tracked departure has prognosisType
		// (PROGNOSED/CALCULATED) and rtTime is omitted when on schedule.
		onTime := sched
		d.Realtime = &onTime
	}
	return d, nil
}

func (r rawDeparture) line() string {
	if r.ProductAtStop != nil {
		if l := firstNonEmpty(r.ProductAtStop.Line, r.ProductAtStop.Name); l != "" {
			return l
		}
	}
	if products, err := oneOrMany[rawProduct](r.Product); err == nil {
		for _, p := range products {
			if l := firstNonEmpty(p.Line, p.Name); l != "" {
				return l
			}
		}
	}
	if r.Line != "" {
		return r.Line
	}
	// Legacy names look like "S-tog A" or "Bus 1A"; the last token is the line.
	fields := strings.Fields(r.Name)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// extIDFromHafasID pulls the numeric external id out of a HAFAS location id
// like "A=1@O=København H@X=12564590@Y=55672731@U=86@L=8600626@".
func extIDFromHafasID(id string) string {
	if !strings.Contains(id, "@") {
		return id
	}
	for _, part := range strings.Split(id, "@") {
		if v, ok := strings.CutPrefix(part, "L="); ok {
			return v
		}
	}
	return ""
}

var (
	dateLayouts = []string{"2006-01-02", "02.01.06", "02.01.2006"}
	timeLayouts = []string{"15:04:05", "15:04"}
)

func parseDateTime(date, clock string, loc *time.Location) (time.Time, error) {
	for _, dl := range dateLayouts {
		for _, tl := range timeLayouts {
			if t, err := time.ParseInLocation(dl+" "+tl, date+" "+clock, loc); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("rejseplanen: bad date/time %q %q", date, clock)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
