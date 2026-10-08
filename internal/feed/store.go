// Package feed holds the current trip updates and serves them as a
// GTFS-Realtime FeedMessage.
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

// KeepAfterTripEnd is how long an update stays in the feed after the trip's
// scheduled end plus its last known delay.
const KeepAfterTripEnd = 30 * time.Minute

type stopObservation struct {
	sequence  int
	stopID    string
	delay     time.Duration
	predicted time.Time
}

type entry struct {
	tripID     string
	routeID    string
	startDate  string
	cancelled  bool
	stops      map[int]stopObservation // keyed by stop_sequence
	observedAt time.Time
	expiresAt  time.Time
}

type Store struct {
	mu      sync.RWMutex
	entries map[string]*entry // trip_id|start_date
}

func NewStore() *Store {
	return &Store{entries: map[string]*entry{}}
}

// Observe records one matched departure. sched provides absolute times.
func (s *Store) Observe(sched *gtfs.Schedule, m gtfs.Match, dep rejseplanen.Departure, now time.Time) {
	key := m.Trip.ID + "|" + m.ServiceDate
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		e = &entry{
			tripID:    m.Trip.ID,
			routeID:   m.Trip.RouteID,
			startDate: m.ServiceDate,
			stops:     map[int]stopObservation{},
		}
		s.entries[key] = e
	}
	e.observedAt = now
	e.cancelled = dep.Cancelled
	delay := dep.Delay()
	st := m.Trip.StopTimes[m.StopIndex]
	if !dep.Cancelled {
		e.stops[st.Sequence] = stopObservation{
			sequence:  st.Sequence,
			stopID:    st.StopID,
			delay:     delay,
			predicted: sched.ScheduledAt(m, m.StopIndex).Add(delay),
		}
	}
	e.expiresAt = sched.TripEnd(m).Add(delay).Add(KeepAfterTripEnd)
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

// Message builds a FULL_DATASET feed of all current entries, sorted by id
// so output is deterministic.
func (s *Store) Message(now time.Time) *rt.FeedMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	msg := &rt.FeedMessage{
		Header: &rt.FeedHeader{
			GtfsRealtimeVersion: proto.String("2.0"),
			Incrementality:      rt.FeedHeader_FULL_DATASET.Enum(),
			Timestamp:           proto.Uint64(uint64(now.Unix())),
		},
	}
	for _, k := range keys {
		e := s.entries[k]
		trip := &rt.TripDescriptor{
			TripId:    proto.String(e.tripID),
			RouteId:   proto.String(e.routeID),
			StartDate: proto.String(e.startDate),
		}
		tu := &rt.TripUpdate{
			Trip:      trip,
			Timestamp: proto.Uint64(uint64(e.observedAt.Unix())),
		}
		if e.cancelled {
			trip.ScheduleRelationship = rt.TripDescriptor_CANCELED.Enum()
		} else {
			trip.ScheduleRelationship = rt.TripDescriptor_SCHEDULED.Enum()
			seqs := make([]int, 0, len(e.stops))
			for seq := range e.stops {
				seqs = append(seqs, seq)
			}
			sort.Ints(seqs)
			for _, seq := range seqs {
				o := e.stops[seq]
				tu.StopTimeUpdate = append(tu.StopTimeUpdate, &rt.TripUpdate_StopTimeUpdate{
					StopSequence: proto.Uint32(uint32(o.sequence)),
					StopId:       proto.String(o.stopID),
					// Explicit although it is the default; validators flag it (W009).
					ScheduleRelationship: rt.TripUpdate_StopTimeUpdate_SCHEDULED.Enum(),
					Departure: &rt.TripUpdate_StopTimeEvent{
						Delay: proto.Int32(int32(o.delay / time.Second)),
						Time:  proto.Int64(o.predicted.Unix()),
					},
				})
			}
			if len(tu.StopTimeUpdate) == 0 {
				continue
			}
		}
		msg.Entity = append(msg.Entity, &rt.FeedEntity{
			Id:         proto.String(k),
			TripUpdate: tu,
		})
	}
	return msg
}
