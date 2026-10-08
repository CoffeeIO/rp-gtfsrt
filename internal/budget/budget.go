// Package budget keeps API usage under a monthly call cap by pacing polls
// evenly across the remaining active hours of the month.
package budget

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"
)

type Budget struct {
	Limit       int           // calls per calendar month
	MinInterval time.Duration // never poll faster than this
	// ActiveFrom/ActiveTo bound the daily polling window in local hours
	// [from, to). Equal values mean all day. to < from wraps past midnight.
	ActiveFrom, ActiveTo int
	Location             *time.Location
	StatePath            string // optional; persists usage across restarts

	mu    sync.Mutex
	month string
	used  int
}

type state struct {
	Month string `json:"month"`
	Used  int    `json:"used"`
}

// Load restores persisted usage. A missing file is not an error.
func (b *Budget) Load() error {
	if b.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(b.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	b.mu.Lock()
	b.month, b.used = st.Month, st.Used
	b.mu.Unlock()
	return nil
}

func (b *Budget) loc() *time.Location {
	if b.Location == nil {
		return time.UTC
	}
	return b.Location
}

func (b *Budget) rollover(now time.Time) {
	m := now.In(b.loc()).Format("2006-01")
	if m != b.month {
		b.month, b.used = m, 0
	}
}

// Reserve records cost calls if they fit in this month's budget.
func (b *Budget) Reserve(now time.Time, cost int) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover(now)
	if b.used+cost > b.Limit {
		return false, nil
	}
	b.used += cost
	return true, b.saveLocked()
}

func (b *Budget) saveLocked() error {
	if b.StatePath == "" {
		return nil
	}
	data, _ := json.Marshal(state{Month: b.month, Used: b.used})
	tmp := b.StatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.StatePath)
}

// Used returns calls spent this month.
func (b *Budget) Used(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollover(now)
	return b.used
}

// Active reports whether now falls inside the daily polling window.
func (b *Budget) Active(now time.Time) bool {
	if b.ActiveFrom == b.ActiveTo {
		return true
	}
	h := now.In(b.loc()).Hour()
	if b.ActiveFrom < b.ActiveTo {
		return h >= b.ActiveFrom && h < b.ActiveTo
	}
	return h >= b.ActiveFrom || h < b.ActiveTo
}

func (b *Budget) activeHoursPerDay() int {
	if b.ActiveFrom == b.ActiveTo {
		return 24
	}
	return ((b.ActiveTo-b.ActiveFrom)%24 + 24) % 24
}

// nextActiveStart returns the next time the window opens after now.
func (b *Budget) nextActiveStart(now time.Time) time.Time {
	local := now.In(b.loc())
	start := time.Date(local.Year(), local.Month(), local.Day(), b.ActiveFrom, 0, 0, 0, b.loc())
	if !start.After(local) {
		start = start.AddDate(0, 0, 1)
	}
	return start
}

// NextDelay returns how long to wait before spending cost more calls, so the
// remaining budget lasts until the end of the month.
func (b *Budget) NextDelay(now time.Time, cost int) time.Duration {
	if !b.Active(now) {
		return b.nextActiveStart(now).Sub(now)
	}
	b.mu.Lock()
	b.rollover(now)
	remaining := b.Limit - b.used
	b.mu.Unlock()

	local := now.In(b.loc())
	monthEnd := time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, b.loc())
	if remaining < cost {
		return monthEnd.Sub(now)
	}
	activeLeft := time.Duration(float64(monthEnd.Sub(now)) * float64(b.activeHoursPerDay()) / 24)
	delay := time.Duration(float64(activeLeft) * float64(cost) / float64(remaining))
	if delay < b.MinInterval {
		delay = b.MinInterval
	}
	return delay
}
