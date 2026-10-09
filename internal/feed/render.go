package feed

import (
	"sort"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/matcher"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// RenderStats counts what happened to stored journeys during a render.
type RenderStats struct {
	Journeys         int            `json:"journeys"`
	Outcomes         map[string]int `json:"outcomes"`
	TripUpdates      int            `json:"trip_updates"`
	VehiclePositions int            `json:"vehicle_positions"`
	Cancelled        int            `json:"cancelled"`
}

// Snapshot is one rendering of the store.
type Snapshot struct {
	TripUpdates      *rt.FeedMessage
	VehiclePositions *rt.FeedMessage
	Stats            RenderStats
}

func header(now time.Time) *rt.FeedHeader {
	return &rt.FeedHeader{
		GtfsRealtimeVersion: proto.String("2.0"),
		Incrementality:      rt.FeedHeader_FULL_DATASET.Enum(),
		Timestamp:           proto.Uint64(uint64(now.Unix())),
	}
}

type event struct {
	delay time.Duration
	at    time.Time
}

type stopUpdate struct {
	arrival, departure *event
	skipped            bool
}

// Render matches every stored journey against sched and builds the feeds.
// It also refreshes each entry's expiry from its matched trip.
func (s *Store) Render(sched *gtfs.Schedule, now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := &matcher.Matcher{Schedule: sched}
	snap := Snapshot{
		TripUpdates:      &rt.FeedMessage{Header: header(now)},
		VehiclePositions: &rt.FeedMessage{Header: header(now)},
		Stats:            RenderStats{Outcomes: map[string]int{}},
	}
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := s.entries[k]
		snap.Stats.Journeys++
		res := e.match(m)
		snap.Stats.Outcomes[res.Outcome.String()]++
		if res.Outcome != matcher.Matched {
			e.expiresAt = e.updatedAt.Add(KeepUnmatched)
			continue
		}
		tu, maxDelay := e.tripUpdate(sched, res)
		e.expiresAt = sched.TripEnd(res.Match).Add(maxDelay).Add(KeepAfterTripEnd)
		if tu != nil {
			if tu.GetTrip().GetScheduleRelationship() == rt.TripDescriptor_CANCELED {
				snap.Stats.Cancelled++
			}
			snap.TripUpdates.Entity = append(snap.TripUpdates.Entity, &rt.FeedEntity{Id: proto.String(k), TripUpdate: tu})
		}
		if e.journey != nil && e.journey.Position != nil && (tu == nil || tu.GetTrip().GetScheduleRelationship() != rt.TripDescriptor_CANCELED) {
			snap.VehiclePositions.Entity = append(snap.VehiclePositions.Entity, &rt.FeedEntity{
				Id: proto.String(k),
				Vehicle: &rt.VehiclePosition{
					Trip: tripDescriptor(sched, res.Match),
					Position: &rt.Position{
						Latitude:  proto.Float32(float32(e.journey.Position.Lat)),
						Longitude: proto.Float32(float32(e.journey.Position.Lon)),
					},
					Timestamp: proto.Uint64(uint64(e.journeyAt.Unix())),
				},
			})
		}
	}
	snap.Stats.TripUpdates = len(snap.TripUpdates.Entity)
	snap.Stats.VehiclePositions = len(snap.VehiclePositions.Entity)
	return snap
}

func tripDescriptor(sched *gtfs.Schedule, m gtfs.Match) *rt.TripDescriptor {
	return &rt.TripDescriptor{
		TripId:               proto.String(m.Trip.ID),
		RouteId:              proto.String(sched.RouteOf(m.Trip).ID),
		StartDate:            proto.String(m.ServiceDate),
		ScheduleRelationship: rt.TripDescriptor_SCHEDULED.Enum(),
	}
}

// tripUpdate builds the TripUpdate for a matched entry and returns the
// largest delay seen (for expiry). It returns nil when there is nothing to say.
func (e *entry) tripUpdate(sched *gtfs.Schedule, res matcher.Result) (*rt.TripUpdate, time.Duration) {
	sts := sched.StopTimes(res.Match.Trip)
	updates := map[int]*stopUpdate{}
	at := func(p int) *stopUpdate {
		u, ok := updates[p]
		if !ok {
			u = &stopUpdate{}
			updates[p] = u
		}
		return u
	}
	var maxDelay time.Duration
	note := func(d time.Duration) {
		if d > maxDelay {
			maxDelay = d
		}
	}
	cancelled := false
	observedAt := e.updatedAt

	if j := e.journey; j != nil {
		cancelled = j.Cancelled
		aligned, skipped := 0, 0
		for i, js := range j.Stops {
			p := res.Positions[i]
			if p < 0 {
				continue
			}
			aligned++
			if js.Cancelled() {
				at(p).skipped = true
				skipped++
				continue
			}
			if js.RealtimeArrival != nil && js.ScheduledArrival != nil {
				d := js.RealtimeArrival.Sub(*js.ScheduledArrival)
				at(p).arrival = &event{delay: d, at: *js.RealtimeArrival}
				note(d)
			}
			if js.RealtimeDeparture != nil && js.ScheduledDeparture != nil {
				d := js.RealtimeDeparture.Sub(*js.ScheduledDeparture)
				at(p).departure = &event{delay: d, at: *js.RealtimeDeparture}
				note(d)
			}
		}
		if aligned > 0 && skipped == aligned {
			cancelled = true
		}
	}

	// Board rows newer than the journey detail refine single stops.
	for _, obs := range e.departures {
		if e.journey != nil && !obs.at.After(e.journeyAt) {
			continue
		}
		p := boardPosition(sched, res.Match, obs.dep.StopID, obs.dep.Scheduled)
		if p < 0 {
			continue
		}
		if obs.dep.Cancelled {
			// A cancelled departure at one stop; a journeyDetail fetch
			// tells whether the whole trip is cancelled.
			u := at(p)
			u.skipped, u.arrival, u.departure = true, nil, nil
			continue
		}
		if obs.dep.Realtime == nil {
			continue
		}
		d := obs.dep.Delay()
		u := at(p)
		u.skipped = false
		u.departure = &event{delay: d, at: *obs.dep.Realtime}
		note(d)
	}

	trip := tripDescriptor(sched, res.Match)
	tu := &rt.TripUpdate{Trip: trip, Timestamp: proto.Uint64(uint64(observedAt.Unix()))}
	if cancelled {
		trip.ScheduleRelationship = rt.TripDescriptor_CANCELED.Enum()
		return tu, maxDelay
	}
	positions := make([]int, 0, len(updates))
	for p := range updates {
		positions = append(positions, p)
	}
	sort.Ints(positions)
	for _, p := range positions {
		u := updates[p]
		stu := &rt.TripUpdate_StopTimeUpdate{
			StopSequence: proto.Uint32(uint32(sts[p].Sequence)),
			StopId:       proto.String(sched.Stops[sts[p].Stop].ID),
		}
		switch {
		case u.skipped:
			stu.ScheduleRelationship = rt.TripUpdate_StopTimeUpdate_SKIPPED.Enum()
		case u.arrival == nil && u.departure == nil:
			continue
		default:
			// Explicit although it is the default; validators flag it (W009).
			stu.ScheduleRelationship = rt.TripUpdate_StopTimeUpdate_SCHEDULED.Enum()
			if u.arrival != nil {
				stu.Arrival = stopTimeEvent(u.arrival)
			}
			if u.departure != nil {
				stu.Departure = stopTimeEvent(u.departure)
			}
		}
		tu.StopTimeUpdate = append(tu.StopTimeUpdate, stu)
	}
	if len(tu.StopTimeUpdate) == 0 {
		return nil, maxDelay
	}
	return tu, maxDelay
}

func stopTimeEvent(e *event) *rt.TripUpdate_StopTimeEvent {
	return &rt.TripUpdate_StopTimeEvent{
		Delay: proto.Int32(int32(e.delay / time.Second)),
		Time:  proto.Int64(e.at.Unix()),
	}
}

// boardPosition finds where in the matched trip a board row's stop is,
// using the scheduled departure minute to pick between repeated visits.
func boardPosition(sched *gtfs.Schedule, m gtfs.Match, stopID string, scheduled time.Time) int {
	sts := sched.StopTimes(m.Trip)
	fallback := -1
	for p, st := range sts {
		if !sched.SameStop(stopID, st.Stop) {
			continue
		}
		if sched.ScheduledDeparture(m, p).Truncate(time.Minute).Equal(scheduled.Truncate(time.Minute)) {
			return p
		}
		if fallback < 0 {
			fallback = p
		}
	}
	return fallback
}
