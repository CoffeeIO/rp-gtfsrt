// Package coverage chooses which stations to poll so that departure boards
// discover every trip. A trip counts as covered when one of its first few
// stops is a chosen station, so it is seen before or soon after it starts.
// This is a greedy set cover, which is within a log factor of optimal.
package coverage

import (
	"container/heap"
	"sort"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
)

// Station is one stop to poll.
type Station struct {
	ID    string // API-style id (leading zeros stripped)
	Name  string
	Trips int // trips newly covered when this station was chosen
}

// Plan returns stations covering every trip active on date (YYYYMMDD),
// most useful first. firstStops is how many leading stops of a trip are
// eligible to discover it.
func Plan(s *gtfs.Schedule, date string, firstStops int) []Station {
	if firstStops < 1 {
		firstStops = 1
	}
	stationOf := func(stop int32) int32 {
		if p := s.Stops[stop].Parent; p != "" {
			for _, i := range s.StopIndexes(p) {
				if gtfs.StopKey(s.Stops[i].ID) == gtfs.StopKey(p) {
					return i
				}
			}
		}
		return stop
	}
	eligible := map[int32][]int32{} // station -> trips
	active := 0
	for i := range s.Trips {
		t := &s.Trips[i]
		if !s.ServiceActive(t, date) {
			continue
		}
		active++
		seen := map[int32]bool{}
		for k, st := range s.StopTimes(t) {
			if k == firstStops {
				break
			}
			station := stationOf(st.Stop)
			if !seen[station] {
				seen[station] = true
				eligible[station] = append(eligible[station], int32(i))
			}
		}
	}

	h := &stationHeap{}
	for station, trips := range eligible {
		*h = append(*h, item{station: station, count: len(trips)})
	}
	heap.Init(h)
	covered := map[int32]bool{}
	var out []Station
	for h.Len() > 0 && len(covered) < active {
		top := heap.Pop(h).(item)
		fresh := 0
		for _, t := range eligible[top.station] {
			if !covered[t] {
				fresh++
			}
		}
		if fresh == 0 {
			continue
		}
		// Lazy greedy: counts only shrink, so if the recomputed count still
		// beats the next best stale count, this station is the best choice.
		if h.Len() > 0 && fresh < (*h)[0].count {
			heap.Push(h, item{station: top.station, count: fresh})
			continue
		}
		for _, t := range eligible[top.station] {
			covered[t] = true
		}
		stop := s.Stops[top.station]
		out = append(out, Station{ID: gtfs.StopKey(stop.ID), Name: stop.Name, Trips: fresh})
	}
	return out
}

// Summary describes a plan for printing.
type Summary struct {
	Stations, Trips int
	// Top is how many stations cover the given share of trips.
	Top map[int]int
}

// Summarize reports how quickly the plan's coverage accumulates.
func Summarize(plan []Station) Summary {
	sum := Summary{Stations: len(plan), Top: map[int]int{}}
	for _, st := range plan {
		sum.Trips += st.Trips
	}
	shares := []int{50, 80, 90, 95, 99, 100}
	sort.Ints(shares)
	acc := 0
	next := 0
	for i, st := range plan {
		acc += st.Trips
		for next < len(shares) && acc*100 >= shares[next]*sum.Trips {
			sum.Top[shares[next]] = i + 1
			next++
		}
	}
	return sum
}

type item struct {
	station int32
	count   int
}

type stationHeap []item

func (h stationHeap) Len() int { return len(h) }
func (h stationHeap) Less(i, j int) bool {
	if h[i].count != h[j].count {
		return h[i].count > h[j].count
	}
	return h[i].station < h[j].station
}
func (h stationHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *stationHeap) Push(x any)   { *h = append(*h, x.(item)) }
func (h *stationHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
