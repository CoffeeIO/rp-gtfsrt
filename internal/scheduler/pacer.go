package scheduler

import (
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
)

// Pacer decides when the next API call may start.
type Pacer interface {
	// Delay returns how long to wait before the next call, measured from now.
	Delay(now time.Time) time.Duration
	// Take records a call starting now. It returns false when the call is
	// not allowed (for example, the monthly budget is spent).
	Take(now time.Time) bool
}

// RatePacer allows a fixed number of calls per second (operator mode).
type RatePacer struct {
	PerSecond float64

	mu   sync.Mutex
	next time.Time
}

func (p *RatePacer) interval() time.Duration {
	if p.PerSecond <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / p.PerSecond)
}

func (p *RatePacer) Delay(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := p.next.Sub(now); d > 0 {
		return d
	}
	return 0
}

func (p *RatePacer) Take(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.next) {
		return false
	}
	p.next = now.Add(p.interval())
	return true
}

// BudgetPacer spreads a monthly call budget over the active hours left in
// the month (free-key / demo mode).
type BudgetPacer struct {
	Budget *budget.Budget

	mu   sync.Mutex
	last time.Time
}

func (p *BudgetPacer) Delay(now time.Time) time.Duration {
	if !p.Budget.Active(now) {
		return p.Budget.NextDelay(now, 1)
	}
	p.mu.Lock()
	last := p.last
	p.mu.Unlock()
	if last.IsZero() {
		return 0
	}
	if d := last.Add(p.Budget.NextDelay(now, 1)).Sub(now); d > 0 {
		return d
	}
	return 0
}

func (p *BudgetPacer) Take(now time.Time) bool {
	if !p.Budget.Active(now) {
		return false
	}
	ok, _ := p.Budget.Reserve(now, 1)
	if ok {
		p.mu.Lock()
		p.last = now
		p.mu.Unlock()
	}
	return ok
}
