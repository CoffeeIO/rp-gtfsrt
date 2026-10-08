package poller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/poller"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(h, m int) time.Time { return time.Date(2026, 10, 8, h, m, 0, 0, cph) }

type fakeSource struct {
	boards map[string][]rejseplanen.Departure
	errs   map[string]error
	calls  []string
}

func (f *fakeSource) DepartureBoard(_ context.Context, stop string) ([]rejseplanen.Departure, error) {
	f.calls = append(f.calls, stop)
	if err := f.errs[stop]; err != nil {
		return nil, err
	}
	return f.boards[stop], nil
}

func rtDep(stop, line, dir string, sched time.Time, delay time.Duration) rejseplanen.Departure {
	rtTime := sched.Add(delay)
	return rejseplanen.Departure{StopID: stop, Line: line, Direction: dir, Scheduled: sched, Realtime: &rtTime}
}

func setup(t *testing.T, src *fakeSource, hubs []string, limit int) (*poller.Poller, *feed.Store) {
	t.Helper()
	s, err := gtfs.Load(gtfstest.Reader(t, gtfstest.Fixture),
		gtfs.Filter{RouteShortNames: map[string]bool{"A": true, "B": true}}, cph)
	if err != nil {
		t.Fatal(err)
	}
	store := feed.NewStore()
	return &poller.Poller{
		Source:   src,
		Schedule: func() *gtfs.Schedule { return s },
		Store:    store,
		Budget:   &budget.Budget{Limit: limit, Location: cph},
		Hubs:     hubs,
		Now:      func() time.Time { return at(8, 0) },
	}, store
}

func TestRunOnceMatchesAndStores(t *testing.T) {
	src := &fakeSource{boards: map[string][]rejseplanen.Departure{
		"8600626": {
			rtDep("8600626", "A", "Hillerød St.", at(8, 12), 2*time.Minute),
			rtDep("8600626", "A", "Hundige", at(8, 12), 0),
			rtDep("8600626", "B", "Høje Taastrup", at(8, 12), time.Minute),
			rtDep("8600626", "A", "", at(8, 12), time.Minute),    // ambiguous
			rtDep("8600626", "C", "Ballerup", at(8, 12), 0),      // no candidate
			{StopID: "8600626", Line: "B", Scheduled: at(8, 20)}, // no realtime
		},
	}}
	p, store := setup(t, src, []string{"8600626"}, 100)
	p.RunOnce(context.Background())

	if store.Len() != 3 {
		t.Fatalf("stored = %d, want 3", store.Len())
	}
	st := p.Stats()
	if st.Polls != 1 || st.Outcomes["matched"] != 3 || st.Outcomes["ambiguous"] != 1 || st.Outcomes["no_candidate"] != 1 || st.NoRealtime != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if !st.LastPoll.Equal(at(8, 0)) {
		t.Fatalf("last poll = %v", st.LastPoll)
	}
}

func TestRunOnceAssumeOnTime(t *testing.T) {
	src := &fakeSource{boards: map[string][]rejseplanen.Departure{
		"8600626": {{StopID: "8600626", Line: "B", Scheduled: at(8, 12)}},
	}}
	p, store := setup(t, src, []string{"8600626"}, 100)
	p.AssumeOnTime = true
	p.RunOnce(context.Background())
	if store.Len() != 1 {
		t.Fatal("departure without rtTime should be stored as on time")
	}
	stu := store.Message(at(8, 0)).Entity[0].GetTripUpdate().StopTimeUpdate[0]
	if stu.GetDeparture().GetDelay() != 0 {
		t.Fatalf("delay = %d", stu.GetDeparture().GetDelay())
	}
}

func TestRunOnceCancelledWithoutRealtimeIsKept(t *testing.T) {
	src := &fakeSource{boards: map[string][]rejseplanen.Departure{
		"8600626": {{StopID: "8600626", Line: "B", Scheduled: at(8, 12), Cancelled: true}},
	}}
	p, store := setup(t, src, []string{"8600626"}, 100)
	p.RunOnce(context.Background())
	if store.Len() != 1 {
		t.Fatal("cancellation must be published even without rtTime")
	}
}

func TestRunOnceBudgetSkipsHubs(t *testing.T) {
	src := &fakeSource{}
	p, _ := setup(t, src, []string{"h1", "h2", "h3"}, 2)
	p.RunOnce(context.Background())
	if len(src.calls) != 2 {
		t.Fatalf("calls = %v, want 2 within budget", src.calls)
	}
	if p.Stats().Skipped != 1 {
		t.Fatalf("skipped = %d", p.Stats().Skipped)
	}
}

func TestRunOnceErrorsDoNotStopOtherHubs(t *testing.T) {
	src := &fakeSource{
		errs: map[string]error{"h1": errors.New("boom")},
		boards: map[string][]rejseplanen.Departure{
			"8600626": {rtDep("8600626", "B", "", at(8, 12), 0)},
		},
	}
	p, store := setup(t, src, []string{"h1", "8600626"}, 100)
	p.RunOnce(context.Background())
	st := p.Stats()
	if st.Errors != 1 || st.LastError != "boom" || st.Polls != 1 || store.Len() != 1 {
		t.Fatalf("stats = %+v, stored = %d", st, store.Len())
	}
}

func TestRunOncePrunesFinishedTrips(t *testing.T) {
	src := &fakeSource{boards: map[string][]rejseplanen.Departure{
		"8600626": {rtDep("8600626", "B", "", at(8, 12), 0)},
	}}
	p, store := setup(t, src, []string{"8600626"}, 100)
	p.RunOnce(context.Background())
	src.boards = nil
	p.Now = func() time.Time { return at(23, 0) }
	p.RunOnce(context.Background())
	if store.Len() != 0 {
		t.Fatal("finished trip should be pruned")
	}
}

func TestStatsIsACopy(t *testing.T) {
	p, _ := setup(t, &fakeSource{}, []string{"x"}, 100)
	p.RunOnce(context.Background())
	st := p.Stats()
	st.Outcomes["matched"] = 999
	if p.Stats().Outcomes["matched"] == 999 {
		t.Fatal("Stats leaked internal map")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	src := &fakeSource{}
	p, _ := setup(t, src, []string{"x"}, 100)
	p.Now = time.Now
	p.Budget.MinInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for p.Stats().Polls == 0 {
		select {
		case <-deadline:
			t.Fatal("first poll never happened")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(src.calls) != 1 {
		t.Fatalf("calls = %d, want 1 before waiting", len(src.calls))
	}
}
