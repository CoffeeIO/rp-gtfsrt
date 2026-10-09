// Package scheduler decides which API call to make next: departure boards to
// discover journeys and get delays at busy stops, journeyDetail to get every
// stop of a journey, and himsearch for traffic alerts. Calls are paced by a
// Pacer (monthly budget or fixed rate) and run on a small worker pool.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// API is the subset of the Rejseplanen client the scheduler uses.
type API interface {
	DepartureBoard(ctx context.Context, stopID string) ([]rejseplanen.Departure, error)
	JourneyDetail(ctx context.Context, ref string) (rejseplanen.Journey, error)
	Alerts(ctx context.Context) ([]rejseplanen.Alert, error)
}

// JourneyMode controls journeyDetail use.
type JourneyMode string

const (
	// JourneysOff never calls journeyDetail (cheapest).
	JourneysOff JourneyMode = "off"
	// JourneysCancelled fetches journeys with a cancelled departure, to learn
	// whether the whole trip or only some stops are cancelled.
	JourneysCancelled JourneyMode = "cancelled"
	// JourneysAll keeps every active journey's full stop list fresh.
	JourneysAll JourneyMode = "all"
)

type Config struct {
	Stops            []string      // discovery stops (API ids)
	BoardInterval    time.Duration // minimum time between polls of one stop
	JourneyMode      JourneyMode
	JourneyInterval  time.Duration // refresh interval per journey
	JourneyLookahead time.Duration // refresh journeys departing within this
	AlertsInterval   time.Duration // 0 disables alerts
	Concurrency      int
	// AssumeOnTime treats board rows without any prediction as on time.
	AssumeOnTime bool
}

type kind int

const (
	kindBoard kind = iota
	kindJourney
	kindAlerts
)

func (k kind) String() string {
	return [...]string{"board", "journey", "alerts"}[k]
}

// Task is one API call the scheduler can make.
type Task struct {
	kind kind
	key  string // stop id or journey key
	ref  string
	due  time.Time
	// priority is added to overdue time when choosing; bigger wins.
	priority time.Duration
}

type taskState struct {
	next     time.Time
	failures int
}

type Scheduler struct {
	API     API
	Pacer   Pacer
	Store   *feed.Store
	Alerts  *feed.AlertStore
	Config  Config
	Metrics *metrics.Registry
	Logger  *slog.Logger
	Now     func() time.Time
	Sleep   func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	state   map[string]*taskState // kind:key -> schedule state
	tracked []feed.Tracked
	stats   Stats
}

// Stats are cumulative counters for /healthz.
type Stats struct {
	Calls     map[string]int `json:"calls"`
	Errors    map[string]int `json:"errors"`
	LastError string         `json:"last_error,omitempty"`
	Journeys  int            `json:"tracked_journeys"`
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Scheduler) sleep(ctx context.Context, d time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Stats{Calls: map[string]int{}, Errors: map[string]int{}, LastError: s.stats.LastError, Journeys: len(s.tracked)}
	for k, v := range s.stats.Calls {
		out.Calls[k] = v
	}
	for k, v := range s.stats.Errors {
		out.Errors[k] = v
	}
	return out
}

func stateKey(k kind, key string) string { return k.String() + ":" + key }

// RefreshTracked reloads the journey list from the store and prunes it.
func (s *Scheduler) RefreshTracked() {
	now := s.now()
	s.Store.Prune(now)
	tracked := s.Store.Tracked()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracked = tracked
	live := map[string]bool{}
	for _, t := range tracked {
		live[stateKey(kindJourney, t.Key)] = true
	}
	for k := range s.state {
		if len(k) > 8 && k[:8] == "journey:" && !live[k] {
			delete(s.state, k)
		}
	}
}

func (s *Scheduler) due(k kind, key string, now time.Time) time.Time {
	if st, ok := s.state[stateKey(k, key)]; ok {
		return st.next
	}
	return now // never run: due immediately
}

// Next returns the most overdue task at now, or nil and the time the
// earliest task becomes due.
func (s *Scheduler) Next(now time.Time) (*Task, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		s.state = map[string]*taskState{}
	}
	var best *Task
	earliest := time.Time{}
	consider := func(t Task) {
		if t.due.After(now) {
			if earliest.IsZero() || t.due.Before(earliest) {
				earliest = t.due
			}
			return
		}
		if best == nil || now.Sub(t.due)+t.priority > now.Sub(best.due)+best.priority {
			tc := t
			best = &tc
		}
	}
	for _, stop := range s.Config.Stops {
		consider(Task{kind: kindBoard, key: stop, due: s.due(kindBoard, stop, now)})
	}
	if s.Config.AlertsInterval > 0 {
		consider(Task{kind: kindAlerts, key: "himsearch", due: s.due(kindAlerts, "himsearch", now)})
	}
	if s.Config.JourneyMode != JourneysOff && s.Config.JourneyMode != "" {
		for _, t := range s.tracked {
			if !s.wantJourney(t, now) {
				continue
			}
			jt := Task{kind: kindJourney, key: t.Key, ref: t.Ref, due: s.due(kindJourney, t.Key, now)}
			if t.Cancelled && !t.HasJourney {
				jt.priority = 10 * time.Minute
			}
			consider(jt)
		}
	}
	return best, earliest
}

func (s *Scheduler) wantJourney(t feed.Tracked, now time.Time) bool {
	switch s.Config.JourneyMode {
	case JourneysCancelled:
		return t.Cancelled
	case JourneysAll:
		return t.Departure.IsZero() || t.Departure.Before(now.Add(s.Config.JourneyLookahead))
	}
	return false
}

func (s *Scheduler) interval(k kind) time.Duration {
	switch k {
	case kindBoard:
		return s.Config.BoardInterval
	case kindJourney:
		if s.Config.JourneyMode == JourneysCancelled {
			return 10 * s.Config.JourneyInterval
		}
		return s.Config.JourneyInterval
	}
	return s.Config.AlertsInterval
}

// schedule records the outcome of a task and sets its next due time, with
// exponential backoff after failures (capped at 10 minutes).
func (s *Scheduler) schedule(t *Task, start time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stateKey(t.kind, t.key)
	st, ok := s.state[key]
	if !ok {
		st = &taskState{}
		s.state[key] = st
	}
	if err == nil {
		st.failures = 0
		st.next = start.Add(s.interval(t.kind))
		return
	}
	st.failures++
	backoff := 30 * time.Second << min(st.failures-1, 5)
	if backoff > 10*time.Minute {
		backoff = 10 * time.Minute
	}
	if !rejseplanen.IsTemporary(err) && t.kind == kindJourney {
		backoff = time.Hour // e.g. unknown journey; don't hammer it
	}
	st.next = start.Add(max(backoff, s.interval(t.kind)))
}

// claim marks a task as in flight so the dispatcher does not pick it again.
func (s *Scheduler) claim(t *Task, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stateKey(t.kind, t.key)
	st, ok := s.state[key]
	if !ok {
		st = &taskState{}
		s.state[key] = st
	}
	st.next = now.Add(5 * time.Minute)
}

// Execute runs one task synchronously.
func (s *Scheduler) Execute(ctx context.Context, t *Task) error {
	start := s.now()
	var err error
	switch t.kind {
	case kindBoard:
		var deps []rejseplanen.Departure
		deps, err = s.API.DepartureBoard(ctx, t.key)
		if err == nil {
			now := s.now()
			for _, d := range deps {
				if d.Realtime == nil && !d.Cancelled && s.Config.AssumeOnTime {
					onTime := d.Scheduled
					d.Realtime = &onTime
				}
				s.Store.ObserveDeparture(d, now)
			}
		}
	case kindJourney:
		var j rejseplanen.Journey
		j, err = s.API.JourneyDetail(ctx, t.ref)
		if err == nil {
			if j.Ref == "" {
				j.Ref = t.ref
			}
			s.Store.ObserveJourney(j, s.now())
		}
	case kindAlerts:
		var alerts []rejseplanen.Alert
		alerts, err = s.API.Alerts(ctx)
		if err == nil {
			s.Alerts.Set(alerts, s.now())
		}
	}
	s.schedule(t, start, err)
	s.mu.Lock()
	if s.stats.Calls == nil {
		s.stats.Calls, s.stats.Errors = map[string]int{}, map[string]int{}
	}
	s.stats.Calls[t.kind.String()]++
	if err != nil {
		s.stats.Errors[t.kind.String()]++
		s.stats.LastError = err.Error()
	}
	s.mu.Unlock()
	if s.Metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		s.Metrics.Inc("rpgtfsrt_api_calls_total", "Rejseplanen API calls by kind and status.", "kind", t.kind.String(), "status", status)
	}
	if err != nil {
		s.log().Warn("api call failed", "kind", t.kind, "key", t.key, "err", err)
	}
	return err
}

// Run dispatches tasks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	workers := max(1, s.Config.Concurrency)
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	defer wg.Wait()
	s.RefreshTracked()
	lastRefresh := s.now()
	for ctx.Err() == nil {
		now := s.now()
		if now.Sub(lastRefresh) >= time.Second {
			s.RefreshTracked()
			lastRefresh = now
		}
		if d := s.Pacer.Delay(now); d > 0 {
			if s.sleep(ctx, min(d, 5*time.Second)) != nil {
				return
			}
			continue
		}
		t, earliest := s.Next(now)
		if t == nil {
			wait := 5 * time.Second
			if !earliest.IsZero() {
				wait = min(wait, earliest.Sub(now))
			}
			if s.sleep(ctx, max(wait, 10*time.Millisecond)) != nil {
				return
			}
			continue
		}
		if !s.Pacer.Take(now) {
			if s.sleep(ctx, time.Second) != nil {
				return
			}
			continue
		}
		s.claim(t, now)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			s.Execute(ctx, t)
		}()
	}
}
