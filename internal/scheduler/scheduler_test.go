package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(h, m int) time.Time { return time.Date(2026, 10, 8, h, m, 0, 0, cph) }

type fakeAPI struct {
	mu       sync.Mutex
	boards   map[string][]rejseplanen.Departure
	journeys map[string]rejseplanen.Journey
	alerts   []rejseplanen.Alert
	errs     map[string]error
	calls    []string
}

func (f *fakeAPI) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.errs[call]
}

func (f *fakeAPI) DepartureBoard(_ context.Context, stop string) ([]rejseplanen.Departure, error) {
	if err := f.record("board:" + stop); err != nil {
		return nil, err
	}
	return f.boards[stop], nil
}

func (f *fakeAPI) JourneyDetail(_ context.Context, ref string) (rejseplanen.Journey, error) {
	if err := f.record("journey:" + ref); err != nil {
		return rejseplanen.Journey{}, err
	}
	return f.journeys[ref], nil
}

func (f *fakeAPI) Alerts(context.Context) ([]rejseplanen.Alert, error) {
	if err := f.record("alerts"); err != nil {
		return nil, err
	}
	return f.alerts, nil
}

func (f *fakeAPI) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newScheduler(api *fakeAPI, cfg Config, now *time.Time) *Scheduler {
	return &Scheduler{
		API: api, Pacer: &RatePacer{}, Store: feed.NewStore(), Alerts: &feed.AlertStore{},
		Config: cfg, Metrics: metrics.New(), Now: func() time.Time { return *now },
	}
}

const ref1 = "2|#ZI#1#DA#81026#"

func departure(stop string, sched time.Time, cancelled bool) rejseplanen.Departure {
	return rejseplanen.Departure{StopID: stop, Line: "A", Scheduled: sched, JourneyRef: ref1, Cancelled: cancelled}
}

// runDue executes every due task at now, in priority order.
func runDue(t *testing.T, s *Scheduler) []string {
	t.Helper()
	var ran []string
	for i := 0; i < 100; i++ {
		s.RefreshTracked()
		task, _ := s.Next(s.now())
		if task == nil {
			return ran
		}
		s.claim(task, s.now())
		s.Execute(context.Background(), task)
		ran = append(ran, task.kind.String()+":"+task.key)
	}
	t.Fatal("too many tasks")
	return nil
}

func TestBoardsAlertsAndIntervals(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{boards: map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), false)}}}
	s := newScheduler(api, Config{Stops: []string{"s1", "s2"}, BoardInterval: 2 * time.Minute, AlertsInterval: 15 * time.Minute, JourneyMode: JourneysOff}, &now)

	ran := runDue(t, s)
	if len(ran) != 3 {
		t.Fatalf("first round = %v", ran)
	}
	if s.Store.Len() != 1 {
		t.Fatal("board rows should be stored")
	}
	now = now.Add(time.Minute)
	if ran := runDue(t, s); len(ran) != 0 {
		t.Fatalf("nothing due after 1 min, ran %v", ran)
	}
	now = now.Add(time.Minute)
	if ran := runDue(t, s); strings.Join(ran, ",") != "board:s1,board:s2" {
		t.Fatalf("after 2 min boards are due again: %v", ran)
	}
	now = now.Add(13 * time.Minute)
	if ran := runDue(t, s); !contains(ran, "alerts:himsearch") {
		t.Fatalf("alerts due after 15 min: %v", ran)
	}
	st := s.Stats()
	if st.Calls["board"] != 6 || st.Calls["alerts"] != 2 || len(st.Errors) != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestJourneyModes(t *testing.T) {
	cases := []struct {
		mode      JourneyMode
		cancelled bool
		want      bool
	}{
		{JourneysOff, true, false},
		{JourneysCancelled, false, false},
		{JourneysCancelled, true, true},
		{JourneysAll, false, true},
	}
	for _, tc := range cases {
		now := at(8, 0)
		api := &fakeAPI{
			boards:   map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), tc.cancelled)}},
			journeys: map[string]rejseplanen.Journey{ref1: {Ref: ref1}},
		}
		s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: time.Hour, JourneyMode: tc.mode,
			JourneyInterval: time.Minute, JourneyLookahead: 30 * time.Minute}, &now)
		ran := runDue(t, s)
		if got := contains(ran, "journey:1@81026"); got != tc.want {
			t.Errorf("mode %s cancelled=%v: journey fetched = %v (%v)", tc.mode, tc.cancelled, got, ran)
		}
	}
}

func TestJourneyLookaheadAndRefresh(t *testing.T) {
	now := at(7, 0)
	api := &fakeAPI{
		boards:   map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), false)}},
		journeys: map[string]rejseplanen.Journey{ref1: {Ref: ref1}},
	}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: 6 * time.Hour, JourneyMode: JourneysAll,
		JourneyInterval: time.Minute, JourneyLookahead: 30 * time.Minute}, &now)
	if ran := runDue(t, s); contains(ran, "journey:1@81026") {
		t.Fatalf("journey departing in 72 min is outside the lookahead: %v", ran)
	}
	now = at(7, 45)
	if ran := runDue(t, s); !contains(ran, "journey:1@81026") {
		t.Fatalf("journey departing in 27 min should be fetched: %v", ran)
	}
	now = now.Add(30 * time.Second)
	if ran := runDue(t, s); len(ran) != 0 {
		t.Fatalf("refresh interval not reached: %v", ran)
	}
	now = now.Add(31 * time.Second)
	if ran := runDue(t, s); !contains(ran, "journey:1@81026") {
		t.Fatalf("refresh due: %v", ran)
	}
}

func TestCancelledJourneyHasPriority(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{
		boards:   map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), true)}},
		journeys: map[string]rejseplanen.Journey{ref1: {Ref: ref1}},
	}
	s := newScheduler(api, Config{Stops: []string{"s1", "s2", "s3"}, BoardInterval: time.Minute, JourneyMode: JourneysCancelled, JourneyInterval: time.Minute}, &now)
	s.RefreshTracked()
	task, _ := s.Next(now)
	s.Execute(context.Background(), task) // board s1 (all equally due; s1 first)
	s.RefreshTracked()
	task, _ = s.Next(now)
	if task.kind != kindJourney {
		t.Fatalf("cancelled journey should jump the queue, got %v %s", task.kind, task.key)
	}
}

func TestBackoffOnErrors(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{errs: map[string]error{"board:s1": &rejseplanen.HTTPError{Status: 503}}}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: 10 * time.Second}, &now)
	runDue(t, s)
	now = now.Add(20 * time.Second)
	if ran := runDue(t, s); len(ran) != 0 {
		t.Fatalf("first failure backs off 30s: %v", ran)
	}
	now = now.Add(11 * time.Second)
	if ran := runDue(t, s); len(ran) != 1 {
		t.Fatalf("retry after backoff: %v", ran)
	}
	now = now.Add(31 * time.Second)
	if ran := runDue(t, s); len(ran) != 0 {
		t.Fatalf("second failure backs off 60s: %v", ran)
	}
	st := s.Stats()
	if st.Errors["board"] != 2 || !strings.Contains(st.LastError, "503") {
		t.Fatalf("stats = %+v", st)
	}
	delete(api.errs, "board:s1")
	now = now.Add(30 * time.Second)
	runDue(t, s)
	now = now.Add(10 * time.Second)
	if ran := runDue(t, s); len(ran) != 1 {
		t.Fatalf("success resets backoff to the interval: %v", ran)
	}
}

func TestPermanentJourneyErrorBacksOffLong(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{
		boards: map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), false)}},
		errs:   map[string]error{"journey:" + ref1: &rejseplanen.HTTPError{Status: 400}},
	}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: 6 * time.Hour, JourneyMode: JourneysAll, JourneyInterval: time.Minute, JourneyLookahead: time.Hour}, &now)
	runDue(t, s)
	now = now.Add(30 * time.Minute)
	if ran := runDue(t, s); len(ran) != 0 {
		t.Fatalf("unknown journey should not be retried for an hour: %v", ran)
	}
}

func TestNewRefEndsPermanentJourneyBackoff(t *testing.T) {
	now := at(8, 0)
	const ref2 = "2|#ZI#1#TA#1#DA#81026#" // same journey, newer timetable version
	api := &fakeAPI{
		boards:   map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), false)}},
		journeys: map[string]rejseplanen.Journey{ref2: {Ref: ref2}},
		errs:     map[string]error{"journey:" + ref1: &rejseplanen.HTTPError{Status: 400}},
	}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: time.Minute, JourneyMode: JourneysAll, JourneyInterval: 2 * time.Minute, JourneyLookahead: time.Hour}, &now)
	runDue(t, s)
	now = now.Add(time.Minute)
	if ran := runDue(t, s); contains(ran, "journey:1@81026") {
		t.Fatalf("same ref should stay backed off: %v", ran)
	}
	d := departure("s1", at(8, 12), false)
	d.JourneyRef = ref2
	api.boards["s1"] = []rejseplanen.Departure{d}
	now = now.Add(time.Minute)
	if ran := runDue(t, s); !contains(ran, "journey:1@81026") {
		t.Fatalf("new ref should be fetched at once: %v", ran)
	}
	if c := api.Calls(); c[len(c)-1] != "journey:"+ref2 {
		t.Fatalf("calls = %v", c)
	}
	now = now.Add(time.Minute)
	if ran := runDue(t, s); contains(ran, "journey:1@81026") {
		t.Fatalf("after success the normal interval applies: %v", ran)
	}
}

func TestAssumeOnTime(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{boards: map[string][]rejseplanen.Departure{"s1": {departure("s1", at(8, 12), false)}}}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: time.Hour, AssumeOnTime: true}, &now)
	runDue(t, s)
	// Can't inspect the store directly without a schedule; the tracked list
	// shows the observation arrived.
	if tr := s.Store.Tracked(); len(tr) != 1 {
		t.Fatalf("tracked = %v", tr)
	}
}

// allowN lets exactly n calls through.
type allowN struct {
	mu sync.Mutex
	n  int
}

func (p *allowN) Delay(time.Time) time.Duration { return 0 }
func (p *allowN) Take(time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n == 0 {
		return false
	}
	p.n--
	return true
}

func TestRunRespectsPacerAndCancel(t *testing.T) {
	api := &fakeAPI{}
	s := &Scheduler{
		API: api, Pacer: &allowN{n: 2}, Store: feed.NewStore(), Alerts: &feed.AlertStore{},
		Config: Config{Stops: []string{"s1", "s2", "s3"}, BoardInterval: time.Hour, Concurrency: 2},
		Sleep: func(ctx context.Context, d time.Duration) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(time.Millisecond)
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)
	if n := len(api.Calls()); n != 2 {
		t.Fatalf("pacer allows 2 calls, got %d: %v", n, api.Calls())
	}
}

func TestRatePacer(t *testing.T) {
	p := &RatePacer{PerSecond: 2}
	now := at(8, 0)
	if p.Delay(now) != 0 || !p.Take(now) {
		t.Fatal("first call allowed")
	}
	if p.Take(now) || p.Delay(now) != 500*time.Millisecond {
		t.Fatalf("second call must wait 500ms, delay = %v", p.Delay(now))
	}
	if !p.Take(now.Add(500 * time.Millisecond)) {
		t.Fatal("allowed after interval")
	}
	unlimited := &RatePacer{}
	if !unlimited.Take(now) || !unlimited.Take(now) {
		t.Fatal("zero rate means unlimited")
	}
}

func TestBudgetPacer(t *testing.T) {
	b := &budget.Budget{Limit: 1000, MinInterval: time.Minute, ActiveFrom: 5, ActiveTo: 1, Location: cph}
	p := &BudgetPacer{Budget: b}
	if p.Delay(at(8, 0)) != 0 || !p.Take(at(8, 0)) {
		t.Fatal("first call allowed")
	}
	if d := p.Delay(at(8, 0)); d < time.Minute {
		t.Fatalf("next call waits at least min interval, got %v", d)
	}
	if p.Take(at(3, 0)) {
		t.Fatal("outside active hours")
	}
	if d := p.Delay(at(3, 0)); d != 2*time.Hour {
		t.Fatalf("delay until window opens = %v", d)
	}
}

func TestMetricsRecorded(t *testing.T) {
	now := at(8, 0)
	api := &fakeAPI{errs: map[string]error{"alerts": errors.New("boom")}}
	s := newScheduler(api, Config{Stops: []string{"s1"}, BoardInterval: time.Hour, AlertsInterval: time.Hour}, &now)
	runDue(t, s)
	var sb strings.Builder
	s.Metrics.Write(&sb)
	for _, want := range []string{
		`rpgtfsrt_api_calls_total{kind="alerts",status="error"} 1`,
		`rpgtfsrt_api_calls_total{kind="board",status="ok"} 1`,
	} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("missing %q in\n%s", want, sb.String())
		}
	}
}
