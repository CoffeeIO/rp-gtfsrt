// Package poller drives the fetch → match → store loop within the API budget.
package poller

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/matcher"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// Source fetches a departure board; *rejseplanen.Client satisfies it.
type Source interface {
	DepartureBoard(ctx context.Context, stopID string) ([]rejseplanen.Departure, error)
}

// ScheduleFunc returns the current static schedule (it may be reloaded).
type ScheduleFunc func() *gtfs.Schedule

type Stats struct {
	Polls      int            `json:"polls"`
	Errors     int            `json:"errors"`
	Skipped    int            `json:"skipped_budget"`
	Outcomes   map[string]int `json:"outcomes"`
	NoRealtime int            `json:"no_realtime"`
	LastPoll   time.Time      `json:"last_poll"`
	LastError  string         `json:"last_error,omitempty"`
}

type Poller struct {
	Source      Source
	Schedule    ScheduleFunc
	StopAliases map[string]string
	Store       *feed.Store
	Budget      *budget.Budget
	Hubs        []string
	// AssumeOnTime treats a departure without rtTime as running on time.
	// Off by default: no prediction is not proof of punctuality.
	AssumeOnTime bool
	Logger       *slog.Logger
	Now          func() time.Time

	mu    sync.Mutex
	stats Stats
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) log() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// Stats returns a copy of the counters.
func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	s.Outcomes = map[string]int{}
	for k, v := range p.stats.Outcomes {
		s.Outcomes[k] = v
	}
	return s
}

// RunOnce polls every hub once, if the budget allows.
func (p *Poller) RunOnce(ctx context.Context) {
	sched := p.Schedule()
	m := &matcher.Matcher{Schedule: sched, StopAliases: p.StopAliases}
	for _, hub := range p.Hubs {
		now := p.now()
		ok, err := p.Budget.Reserve(now, 1)
		if err != nil {
			p.log().Warn("budget state not saved", "err", err)
		}
		if !ok {
			p.record(func(s *Stats) { s.Skipped++ })
			continue
		}
		deps, err := p.Source.DepartureBoard(ctx, hub)
		if err != nil {
			p.log().Error("departure board failed", "hub", hub, "err", err)
			p.record(func(s *Stats) { s.Errors++; s.LastError = err.Error() })
			continue
		}
		for _, dep := range deps {
			if dep.Realtime == nil && !dep.Cancelled && !p.AssumeOnTime {
				p.record(func(s *Stats) { s.NoRealtime++ })
				continue
			}
			match, outcome := m.Match(dep)
			p.record(func(s *Stats) { s.Outcomes[outcome.String()]++ })
			if outcome != matcher.Matched {
				p.log().Debug("unmatched departure", "outcome", outcome, "stop", dep.StopID,
					"line", dep.Line, "scheduled", dep.Scheduled, "direction", dep.Direction)
				continue
			}
			p.Store.Observe(sched, match, dep, now)
		}
		p.record(func(s *Stats) { s.Polls++; s.LastPoll = now })
	}
	p.Store.Prune(p.now())
}

func (p *Poller) record(fn func(*Stats)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stats.Outcomes == nil {
		p.stats.Outcomes = map[string]int{}
	}
	fn(&p.stats)
}

// Run polls until ctx is cancelled, pacing with the budget.
func (p *Poller) Run(ctx context.Context) {
	for {
		if p.Budget.Active(p.now()) {
			p.RunOnce(ctx)
		}
		delay := p.Budget.NextDelay(p.now(), len(p.Hubs))
		p.log().Info("next poll", "in", delay.Round(time.Second), "used", p.Budget.Used(p.now()), "entities", p.Store.Len())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}
