package siri

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// Store receives journeys; feed.Store implements it.
type Store interface {
	ObserveJourney(j rejseplanen.Journey, now time.Time)
	Prune(now time.Time) int
}

// Poller fetches a SIRI-ET document over HTTP at a fixed interval and passes
// every changed journey to Store. It suits a service that serves the full
// estimated timetable as one document (for example the public mirror of
// Rejseplanen's national access point streams).
type Poller struct {
	URL      string
	Interval time.Duration
	HTTP     *http.Client
	Store    Store
	Metrics  *metrics.Registry
	Logger   *slog.Logger
	Now      func() time.Time

	mu    sync.Mutex
	seen  map[string]time.Time // journey key -> RecordedAtTime last passed on
	stats Stats
}

// Stats is reported on /healthz.
type Stats struct {
	Polls     int       `json:"polls"`
	Errors    int       `json:"errors"`
	LastError string    `json:"last_error,omitempty"`
	LastPoll  time.Time `json:"last_poll"`
	Journeys  int       `json:"journeys"` // in the last document
	Changed   int       `json:"changed"`  // passed on from the last document
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

// Poll fetches the document once.
func (p *Poller) Poll(ctx context.Context) error {
	err := p.poll(ctx)
	p.mu.Lock()
	p.stats.Polls++
	p.stats.LastPoll = p.now()
	if err != nil {
		p.stats.Errors++
		p.stats.LastError = err.Error()
	}
	p.mu.Unlock()
	if p.Metrics != nil {
		status := "ok"
		if err != nil {
			status = "error"
		}
		p.Metrics.Inc("rpgtfsrt_siri_polls_total", "SIRI-ET document fetches by status.", "status", status)
	}
	return err
}

func (p *Poller) poll(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("siri: http %d", resp.StatusCode)
	}

	now := p.now()
	seen := map[string]time.Time{}
	journeys, changed := 0, 0
	p.mu.Lock()
	prev := p.seen
	p.mu.Unlock()
	err = Decode(resp.Body, func(u Update) {
		journeys++
		key := u.Journey.Key()
		at := u.RecordedAt
		if at.IsZero() || at.After(now) {
			at = now
		}
		seen[key] = at
		// Unchanged journeys keep their cached match.
		if last, ok := prev[key]; ok && !u.RecordedAt.IsZero() && last.Equal(at) {
			return
		}
		changed++
		p.Store.ObserveJourney(u.Journey, at)
	})
	if err != nil {
		return fmt.Errorf("siri: decode: %w", err)
	}
	p.Store.Prune(now)
	p.mu.Lock()
	p.seen = seen
	p.stats.Journeys, p.stats.Changed = journeys, changed
	p.mu.Unlock()
	if p.Metrics != nil {
		p.Metrics.Set("rpgtfsrt_siri_journeys", "Journeys in the last SIRI-ET document.", float64(journeys))
	}
	p.log().Debug("siri poll", "journeys", journeys, "changed", changed)
	return nil
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	for {
		if err := p.Poll(ctx); err != nil && ctx.Err() == nil {
			p.log().Warn("siri poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Interval):
		}
	}
}

func (p *Poller) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
