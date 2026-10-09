package feed

import (
	"sort"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// AlertLanguage is the language of Rejseplanen traffic information.
const AlertLanguage = "da"

// AlertStore holds the latest himsearch result.
type AlertStore struct {
	mu        sync.RWMutex
	alerts    []rejseplanen.Alert
	fetchedAt time.Time
}

func (a *AlertStore) Set(alerts []rejseplanen.Alert, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts, a.fetchedAt = alerts, now
}

func (a *AlertStore) FetchedAt() time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.fetchedAt
}

// AlertStats counts alerts by what happened to them during a render.
type AlertStats struct {
	Received   int `json:"received"`
	Published  int `json:"published"`
	Inactive   int `json:"inactive"`
	NoEntities int `json:"no_entities"`
}

// Render builds the Alerts feed. Each alert is attached to the GTFS stops
// its affected stations resolve to; alerts with no resolvable stop are left
// out, since GTFS-RT requires at least one informed_entity and the API does
// not name affected lines.
func (a *AlertStore) Render(sched *gtfs.Schedule, now time.Time) (*rt.FeedMessage, AlertStats) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	msg := &rt.FeedMessage{Header: header(now)}
	stats := AlertStats{Received: len(a.alerts)}
	for _, al := range a.alerts {
		if !al.Active || (!al.End.IsZero() && now.After(al.End)) {
			stats.Inactive++
			continue
		}
		entities := informedStops(sched, al.StopIDs)
		if len(entities) == 0 {
			stats.NoEntities++
			continue
		}
		alert := &rt.Alert{
			InformedEntity: entities,
			HeaderText:     translated(al.Header),
			Cause:          rt.Alert_UNKNOWN_CAUSE.Enum(),
			Effect:         rt.Alert_UNKNOWN_EFFECT.Enum(),
		}
		if al.Text != "" {
			alert.DescriptionText = translated(al.Text)
		}
		if !al.Start.IsZero() || !al.End.IsZero() {
			period := &rt.TimeRange{}
			if !al.Start.IsZero() {
				period.Start = proto.Uint64(uint64(al.Start.Unix()))
			}
			if !al.End.IsZero() {
				period.End = proto.Uint64(uint64(al.End.Unix()))
			}
			alert.ActivePeriod = []*rt.TimeRange{period}
		}
		msg.Entity = append(msg.Entity, &rt.FeedEntity{Id: proto.String(al.ID), Alert: alert})
		stats.Published++
	}
	return msg, stats
}

// informedStops resolves API stop ids to GTFS stop_ids. A station id
// resolves to the station itself when it exists in GTFS, otherwise to all
// stops sharing that key.
func informedStops(sched *gtfs.Schedule, apiIDs []string) []*rt.EntitySelector {
	seen := map[string]bool{}
	var ids []string
	for _, id := range apiIDs {
		idx := sched.StopIndexes(id)
		var exact []string
		for _, i := range idx {
			if gtfs.StopKey(sched.Stops[i].ID) == gtfs.StopKey(id) {
				exact = append(exact, sched.Stops[i].ID)
			}
		}
		if len(exact) == 0 {
			for _, i := range idx {
				exact = append(exact, sched.Stops[i].ID)
			}
		}
		for _, s := range exact {
			if !seen[s] {
				seen[s] = true
				ids = append(ids, s)
			}
		}
	}
	sort.Strings(ids)
	out := make([]*rt.EntitySelector, 0, len(ids))
	for _, id := range ids {
		out = append(out, &rt.EntitySelector{StopId: proto.String(id)})
	}
	return out
}

func translated(text string) *rt.TranslatedString {
	return &rt.TranslatedString{Translation: []*rt.TranslatedString_Translation{{
		Text:     proto.String(text),
		Language: proto.String(AlertLanguage),
	}}}
}
