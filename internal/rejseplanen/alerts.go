package rejseplanen

import (
	"encoding/json"
	"fmt"
	"time"
)

// Alert is a normalized himsearch message (traffic information).
type Alert struct {
	ID       string
	Active   bool
	Header   string
	Text     string
	Company  string
	Priority int
	Start    time.Time // zero when open-ended
	End      time.Time
	// Products is the API product class bitmask (1 IC, 2 ICL, 4 regional,
	// 8 other train, 16 S-tog, 32 bus, ...). The API names no lines.
	Products int
	StopIDs  []string
}

type rawStopLocation struct {
	ExtID string `json:"extId"`
	ID    string `json:"id"`
}

type rawMessage struct {
	ID            string   `json:"id"`
	Act           flexBool `json:"act"`
	Head          string   `json:"head"`
	Lead          string   `json:"lead"`
	Text          string   `json:"text"`
	Company       string   `json:"company"`
	Priority      int      `json:"priority"`
	SDate         string   `json:"sDate"`
	STime         string   `json:"sTime"`
	EDate         string   `json:"eDate"`
	ETime         string   `json:"eTime"`
	Products      int      `json:"products"`
	AffectedStops *struct {
		StopLocation json.RawMessage `json:"StopLocation"`
	} `json:"affectedStops"`
	ValidFromStop *rawStopLocation `json:"validFromStop"`
	ValidToStop   *rawStopLocation `json:"validToStop"`
}

// ParseAlerts decodes a himsearch response.
func ParseAlerts(body []byte, loc *time.Location) ([]Alert, error) {
	var top struct {
		apiError
		Message json.RawMessage `json:"Message"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("rejseplanen: decode alerts: %w", err)
	}
	if top.apiError != (apiError{}) {
		return nil, fmt.Errorf("rejseplanen: api error %s: %s%s", top.ErrorCode, top.ErrorText, top.Error)
	}
	raws, err := oneOrMany[rawMessage](top.Message)
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: decode alerts: %w", err)
	}
	out := make([]Alert, 0, len(raws))
	for _, r := range raws {
		a := Alert{
			ID:       r.ID,
			Active:   bool(r.Act),
			Header:   r.Head,
			Text:     firstNonEmpty(r.Text, r.Lead),
			Company:  r.Company,
			Priority: r.Priority,
			Products: r.Products,
		}
		if r.SDate != "" {
			if a.Start, err = parseDateTime(r.SDate, firstNonEmpty(r.STime, "00:00:00"), loc); err != nil {
				return nil, err
			}
		}
		if r.EDate != "" {
			if a.End, err = parseDateTime(r.EDate, firstNonEmpty(r.ETime, "23:59:59"), loc); err != nil {
				return nil, err
			}
		}
		seen := map[string]bool{}
		add := func(l rawStopLocation) {
			id := firstNonEmpty(l.ExtID, extIDFromHafasID(l.ID))
			if id != "" && !seen[id] {
				seen[id] = true
				a.StopIDs = append(a.StopIDs, id)
			}
		}
		if r.AffectedStops != nil {
			locs, err := oneOrMany[rawStopLocation](r.AffectedStops.StopLocation)
			if err != nil {
				return nil, fmt.Errorf("rejseplanen: decode alert stops: %w", err)
			}
			for _, l := range locs {
				add(l)
			}
		}
		for _, l := range []*rawStopLocation{r.ValidFromStop, r.ValidToStop} {
			if l != nil {
				add(*l)
			}
		}
		out = append(out, a)
	}
	return out, nil
}
