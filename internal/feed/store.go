// Package feed keeps raw realtime observations and renders them as
// GTFS-Realtime TripUpdates, VehiclePositions and Alerts feeds.
//
// Observations are stored as the API returned them, keyed by journey, and
// matched to GTFS trips at render time. The match is cached per schedule, so
// a weekly GTFS reload (where trip_ids may change) simply re-matches.
package feed

import (
	"sort"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/matcher"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// KeepAfterTripEnd is how long a trip stays in the feed after its scheduled
// end plus its last known delay.
const KeepAfterTripEnd = 30 * time.Minute

// KeepUnmatched is how long observations that match no GTFS trip are kept.
const KeepUnmatched = 2 * time.Hour

type departureObs struct {
	dep rejseplanen.Departure
	at  time.Time
}

type entry struct {
	key        string
	ref        string
	journey    *rejseplanen.Journey
	journeyAt  time.Time
	departures map[string]departureObs // gtfs.StopKey(stop) -> latest board row
	first      rejseplanen.Departure   // first board row seen, used to match without a journey
	updatedAt  time.Time
	expiresAt  time.Time

	// match cache
	cacheSched *gtfs.Schedule
	cacheAt    time.Time
	result     matcher.Result
}

type Store struct {
	mu      sync.RWMutex
	entries map[string]*entry
}

func NewStore() *Store {
	return &Store{entries: map[string]*entry{}}
}

func (s *Store) get(key string, now time.Time) *entry {
	e, ok := s.entries[key]
	if !ok {
		e = &entry{key: key, departures: map[string]departureObs{}, expiresAt: now.Add(KeepUnmatched)}
		s.entries[key] = e
	}
	return e
}

// ObserveDeparture records one departure board row.
func (s *Store) ObserveDeparture(d rejseplanen.Departure, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.get(d.JourneyKey(), now)
	if len(e.departures) == 0 && e.journey == nil {
		e.first = d
	}
	if d.JourneyRef != "" {
		e.ref = d.JourneyRef
	}
	e.departures[gtfs.StopKey(d.StopID)] = departureObs{dep: d, at: now}
	e.updatedAt = now
}

// ObserveJourney records a full journeyDetail response.
func (s *Store) ObserveJourney(j rejseplanen.Journey, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.get(j.Key(), now)
	jc := j
	e.journey = &jc
	e.journeyAt = now
	if j.Ref != "" {
		e.ref = j.Ref
	}
	e.updatedAt = now
}

// Tracked describes a journey the scheduler may want to refresh.
type Tracked struct {
	Key         string
	Ref         string
	HasJourney  bool
	JourneyAt   time.Time
	Cancelled   bool      // a board row or the journey reports a cancellation
	Departure   time.Time // earliest observed scheduled departure
	ExpiresAt   time.Time
	LastUpdated time.Time
}

// Tracked lists journeys that have a journeyDetail reference.
func (s *Store) Tracked() []Tracked {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tracked, 0, len(s.entries))
	for _, e := range s.entries {
		if e.ref == "" {
			continue
		}
		t := Tracked{
			Key: e.key, Ref: e.ref, HasJourney: e.journey != nil, JourneyAt: e.journeyAt,
			ExpiresAt: e.expiresAt, LastUpdated: e.updatedAt,
		}
		for _, d := range e.departures {
			if d.dep.Cancelled {
				t.Cancelled = true
			}
			if t.Departure.IsZero() || d.dep.Scheduled.Before(t.Departure) {
				t.Departure = d.dep.Scheduled
			}
		}
		if e.journey != nil {
			if e.journey.Cancelled {
				t.Cancelled = true
			}
			for _, st := range e.journey.Stops {
				if st.ScheduledDeparture != nil {
					if t.Departure.IsZero() || st.ScheduledDeparture.Before(t.Departure) {
						t.Departure = *st.ScheduledDeparture
					}
					break
				}
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Prune drops entries whose trips have finished.
func (s *Store) Prune(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
			removed++
		}
	}
	return removed
}

func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// match returns the cached or fresh match for e. Callers hold s.mu.
func (e *entry) match(m *matcher.Matcher) matcher.Result {
	if e.cacheSched == m.Schedule && e.cacheAt.Equal(e.updatedAt) {
		return e.result
	}
	if e.journey != nil {
		e.result = m.MatchJourney(*e.journey)
	} else {
		e.result = m.MatchDeparture(e.first)
	}
	e.cacheSched, e.cacheAt = m.Schedule, e.updatedAt
	return e.result
}
