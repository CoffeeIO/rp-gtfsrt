// Package siri reads SIRI Estimated Timetable (SIRI-ET), the realtime
// format Rejseplanen publishes on the Danish national access point, and turns
// each vehicle journey into a rejseplanen.Journey so the rest of the bridge
// matches and renders it like a journeyDetail response.
package siri

import (
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

type rawCall struct {
	StopPointRef          string `xml:"StopPointRef"`
	Order                 string `xml:"Order"`
	Cancellation          string `xml:"Cancellation"`
	AimedArrivalTime      string `xml:"AimedArrivalTime"`
	ExpectedArrivalTime   string `xml:"ExpectedArrivalTime"`
	ActualArrivalTime     string `xml:"ActualArrivalTime"`
	ArrivalStatus         string `xml:"ArrivalStatus"`
	ArrivalPlatformName   string `xml:"ArrivalPlatformName"`
	AimedDepartureTime    string `xml:"AimedDepartureTime"`
	ExpectedDepartureTime string `xml:"ExpectedDepartureTime"`
	ActualDepartureTime   string `xml:"ActualDepartureTime"`
	DepartureStatus       string `xml:"DepartureStatus"`
	DeparturePlatformName string `xml:"DeparturePlatformName"`
}

type rawJourney struct {
	RecordedAtTime         string    `xml:"RecordedAtTime"`
	LineRef                string    `xml:"LineRef"`
	DataFrameRef           string    `xml:"FramedVehicleJourneyRef>DataFrameRef"`
	DatedVehicleJourneyRef string    `xml:"FramedVehicleJourneyRef>DatedVehicleJourneyRef"`
	VehicleJourneyRef      string    `xml:"VehicleJourneyRef"`
	Cancellation           string    `xml:"Cancellation"`
	RecordedCalls          []rawCall `xml:"RecordedCalls>RecordedCall"`
	EstimatedCalls         []rawCall `xml:"EstimatedCalls>EstimatedCall"`
}

// Update is one vehicle journey from a SIRI-ET delivery.
type Update struct {
	Journey    rejseplanen.Journey
	RecordedAt time.Time // zero if the delivery did not say
}

// Decode streams a SIRI-ET document (a full ServiceDelivery or a single
// EstimatedVehicleJourney message) and calls fn for every journey with a
// reference. It never holds more than one journey in memory.
func Decode(r io.Reader, fn func(Update)) error {
	d := xml.NewDecoder(r)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "EstimatedVehicleJourney" {
			continue
		}
		var raw rawJourney
		if err := d.DecodeElement(&raw, &se); err != nil {
			return err
		}
		if u, ok := raw.update(); ok {
			fn(u)
		}
	}
}

func (r rawJourney) update() (Update, bool) {
	ref := r.DatedVehicleJourneyRef
	if ref == "" {
		ref = r.VehicleJourneyRef
	}
	if ref == "" {
		return Update{}, false
	}
	if r.DataFrameRef != "" {
		ref = r.DataFrameRef + "/" + ref
	}
	j := rejseplanen.Journey{Ref: ref, Line: strings.TrimSpace(r.LineRef), Cancelled: isTrue(r.Cancellation)}

	// A delivery may repeat a stop, once per update it has seen; the last
	// one is the newest.
	latest := map[string]rawCall{}
	var order []string
	for _, c := range append(r.RecordedCalls, r.EstimatedCalls...) {
		if c.StopPointRef == "" {
			continue
		}
		if _, seen := latest[c.StopPointRef]; !seen {
			order = append(order, c.StopPointRef)
		}
		latest[c.StopPointRef] = c
	}
	for _, id := range order {
		j.Stops = append(j.Stops, latest[id].stop())
	}
	sortStops(j.Stops)
	return Update{Journey: j, RecordedAt: parseTime(r.RecordedAtTime)}, true
}

// Merge combines an earlier state of a journey with a newer update of it.
// Event messages carry only the stops that changed, so stops missing from
// next keep their earlier values; stops in both take next's.
func Merge(prev, next rejseplanen.Journey) rejseplanen.Journey {
	out := next
	out.Stops = append([]rejseplanen.JourneyStop(nil), next.Stops...)
	have := map[string]bool{}
	for _, s := range next.Stops {
		have[s.StopID] = true
	}
	for _, s := range prev.Stops {
		if !have[s.StopID] {
			out.Stops = append(out.Stops, s)
		}
	}
	sortStops(out.Stops)
	return out
}

// sortStops orders stops by Order when every stop has one, otherwise by
// planned time.
func sortStops(stops []rejseplanen.JourneyStop) {
	sort.SliceStable(stops, func(a, b int) bool {
		sa, sb := stops[a], stops[b]
		if sa.Index > 0 && sb.Index > 0 {
			return sa.Index < sb.Index
		}
		ta, tb := plannedTime(sa), plannedTime(sb)
		return ta != nil && tb != nil && ta.Before(*tb)
	})
}

func (c rawCall) stop() rejseplanen.JourneyStop {
	s := rejseplanen.JourneyStop{
		StopID:             strings.TrimSpace(c.StopPointRef),
		ScheduledArrival:   timePtr(c.AimedArrivalTime),
		ScheduledDeparture: timePtr(c.AimedDepartureTime),
		RealtimeArrival:    timePtr(first(c.ActualArrivalTime, c.ExpectedArrivalTime)),
		RealtimeDeparture:  timePtr(first(c.ActualDepartureTime, c.ExpectedDepartureTime)),
		ArrivalCancelled:   isTrue(c.Cancellation) || isCancelled(c.ArrivalStatus),
		DepartureCancelled: isTrue(c.Cancellation) || isCancelled(c.DepartureStatus),
		Track:              first(c.DeparturePlatformName, c.ArrivalPlatformName),
	}
	if n, err := strconv.Atoi(strings.TrimSpace(c.Order)); err == nil {
		s.Index = n
	}
	return s
}

func plannedTime(s rejseplanen.JourneyStop) *time.Time {
	if s.ScheduledDeparture != nil {
		return s.ScheduledDeparture
	}
	return s.ScheduledArrival
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

func timePtr(s string) *time.Time {
	t := parseTime(s)
	if t.IsZero() {
		return nil
	}
	return &t
}

func first(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func isTrue(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }

func isCancelled(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "cancelled")
}
