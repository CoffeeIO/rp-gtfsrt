package coverage_test

import (
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/coverage"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
)

func load(t *testing.T) *gtfs.Schedule {
	t.Helper()
	cph, _ := time.LoadLocation("Europe/Copenhagen")
	s, err := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture), gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanCoversEveryActiveTrip(t *testing.T) {
	s := load(t)
	// Thursday: A1 A2 A3 B1 X1 BUS are active (W1 is weekend only).
	plan := coverage.Plan(s, "20261008", 1)
	total := 0
	for _, st := range plan {
		total += st.Trips
	}
	if total != 6 {
		t.Fatalf("covered %d trips, want 6: %+v", total, plan)
	}
	// København H (station) starts A3, B1, X1 and BUS: the greedy pick.
	if plan[0].ID != "8600626" || plan[0].Name != "København H" || plan[0].Trips != 4 {
		t.Fatalf("first station = %+v", plan[0])
	}
	if len(plan) != 3 {
		t.Fatalf("A1 starts at Hundige and A2 at Hillerød: want 3 stations, got %+v", plan)
	}
}

func TestPlanMoreFirstStopsNeedsFewerStations(t *testing.T) {
	s := load(t)
	// With 2 leading stops, A1 (Hundige → København H) is covered by
	// København H too; only A2 (Hillerød → Nørreport) needs another station.
	plan := coverage.Plan(s, "20261008", 2)
	if len(plan) != 2 || plan[0].Trips != 5 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestPlanNoServiceDay(t *testing.T) {
	if plan := coverage.Plan(load(t), "20200101", 3); len(plan) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if plan := coverage.Plan(load(t), "20261008", 0); len(plan) == 0 {
		t.Fatal("firstStops < 1 is treated as 1")
	}
}

func TestSummarize(t *testing.T) {
	plan := []coverage.Station{{Trips: 50}, {Trips: 30}, {Trips: 15}, {Trips: 5}}
	sum := coverage.Summarize(plan)
	if sum.Stations != 4 || sum.Trips != 100 {
		t.Fatalf("sum = %+v", sum)
	}
	want := map[int]int{50: 1, 80: 2, 90: 3, 95: 3, 99: 4, 100: 4}
	for share, n := range want {
		if sum.Top[share] != n {
			t.Errorf("top %d%% = %d, want %d", share, sum.Top[share], n)
		}
	}
}
