package budget_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
)

var cph, _ = time.LoadLocation("Europe/Copenhagen")

func at(m time.Month, d, h, min int) time.Time {
	return time.Date(2026, m, d, h, min, 0, 0, cph)
}

func TestReserveStopsAtLimit(t *testing.T) {
	b := &budget.Budget{Limit: 3, Location: cph}
	now := at(10, 8, 9, 0)
	for i := 0; i < 3; i++ {
		if ok, err := b.Reserve(now, 1); !ok || err != nil {
			t.Fatalf("call %d rejected: %v", i, err)
		}
	}
	if ok, _ := b.Reserve(now, 1); ok {
		t.Fatal("4th call should be rejected")
	}
	if b.Used(now) != 3 {
		t.Fatalf("used = %d", b.Used(now))
	}
}

func TestReserveCostLargerThanRemaining(t *testing.T) {
	b := &budget.Budget{Limit: 5, Location: cph}
	now := at(10, 8, 9, 0)
	b.Reserve(now, 4)
	if ok, _ := b.Reserve(now, 2); ok {
		t.Fatal("cost 2 with 1 remaining should be rejected")
	}
	if b.Used(now) != 4 {
		t.Fatal("rejected reservation must not be counted")
	}
}

func TestMonthRollover(t *testing.T) {
	b := &budget.Budget{Limit: 1, Location: cph}
	b.Reserve(at(10, 31, 23, 59), 1)
	if ok, _ := b.Reserve(at(10, 31, 23, 59), 1); ok {
		t.Fatal("same month should be exhausted")
	}
	if ok, _ := b.Reserve(at(11, 1, 0, 0), 1); !ok {
		t.Fatal("new month should reset usage")
	}
}

func TestRolloverUsesLocalMonth(t *testing.T) {
	b := &budget.Budget{Limit: 1, Location: cph}
	b.Reserve(at(10, 31, 12, 0), 1)
	// 2026-10-31 23:30 UTC is already November 1st in Copenhagen.
	if ok, _ := b.Reserve(time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC), 1); !ok {
		t.Fatal("should roll over on local midnight")
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	now := at(10, 8, 9, 0)
	b1 := &budget.Budget{Limit: 10, Location: cph, StatePath: path}
	b1.Reserve(now, 4)

	b2 := &budget.Budget{Limit: 10, Location: cph, StatePath: path}
	if err := b2.Load(); err != nil {
		t.Fatal(err)
	}
	if b2.Used(now) != 4 {
		t.Fatalf("restored used = %d, want 4", b2.Used(now))
	}
	if b2.Used(at(11, 1, 0, 0)) != 0 {
		t.Fatal("restored state from an old month must reset")
	}
}

func TestLoadMissingFileIsFine(t *testing.T) {
	b := &budget.Budget{Limit: 1, StatePath: filepath.Join(t.TempDir(), "nope.json")}
	if err := b.Load(); err != nil {
		t.Fatal(err)
	}
	if err := (&budget.Budget{}).Load(); err != nil {
		t.Fatal("no state path should be a no-op")
	}
}

func TestLoadCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	os.WriteFile(path, []byte("{nope"), 0o644)
	b := &budget.Budget{Limit: 1, StatePath: path}
	if err := b.Load(); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveErrorReported(t *testing.T) {
	b := &budget.Budget{Limit: 5, StatePath: filepath.Join(t.TempDir(), "missing-dir", "b.json")}
	ok, err := b.Reserve(at(10, 8, 9, 0), 1)
	if !ok || err == nil {
		t.Fatalf("ok=%v err=%v; want reservation kept and save error returned", ok, err)
	}
}

func TestActive(t *testing.T) {
	cases := []struct {
		from, to, hour int
		want           bool
	}{
		{5, 1, 4, false},
		{5, 1, 5, true},
		{5, 1, 23, true},
		{5, 1, 0, true},
		{5, 1, 1, false},
		{6, 22, 12, true},
		{6, 22, 22, false},
		{0, 0, 3, true}, // equal means always
	}
	for _, tc := range cases {
		b := &budget.Budget{ActiveFrom: tc.from, ActiveTo: tc.to, Location: cph}
		if got := b.Active(at(10, 8, tc.hour, 30)); got != tc.want {
			t.Errorf("window %d-%d hour %d: got %v", tc.from, tc.to, tc.hour, got)
		}
	}
}

func TestNextDelayInactiveWaitsForWindow(t *testing.T) {
	b := &budget.Budget{Limit: 1000, ActiveFrom: 5, ActiveTo: 1, Location: cph}
	if got := b.NextDelay(at(10, 8, 3, 0), 1); got != 2*time.Hour {
		t.Fatalf("delay = %v, want 2h", got)
	}
}

func TestNextDelayExhaustedWaitsForNextMonth(t *testing.T) {
	b := &budget.Budget{Limit: 1, Location: cph}
	now := at(10, 31, 22, 0)
	b.Reserve(now, 1)
	if got := b.NextDelay(now, 1); got != 2*time.Hour {
		t.Fatalf("delay = %v, want 2h until November", got)
	}
}

func TestNextDelayPacing(t *testing.T) {
	// 1000 calls left, 10 days left, all-day window → one call per 864s.
	b := &budget.Budget{Limit: 1000, Location: cph}
	now := at(11, 21, 0, 0) // no DST change before December
	if got := b.NextDelay(now, 1); got != 864*time.Second {
		t.Fatalf("delay = %v, want 864s", got)
	}
	// Cost 2 per round doubles the gap.
	if got := b.NextDelay(now, 2); got != 1728*time.Second {
		t.Fatalf("delay = %v, want 1728s", got)
	}
	// A 12h window concentrates the same budget into half the time.
	b.ActiveFrom, b.ActiveTo = 6, 18
	if got := b.NextDelay(at(11, 21, 6, 0), 1); got >= 864*time.Second/2+time.Second || got <= 0 {
		t.Fatalf("12h window delay = %v, want ~<432s", got)
	}
}

func TestNextDelayRespectsMinInterval(t *testing.T) {
	b := &budget.Budget{Limit: 1_000_000, MinInterval: time.Minute, Location: cph}
	if got := b.NextDelay(at(10, 8, 9, 0), 1); got != time.Minute {
		t.Fatalf("delay = %v, want min interval", got)
	}
}

func TestDefaultLocationIsUTC(t *testing.T) {
	b := &budget.Budget{Limit: 1}
	b.Reserve(time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC), 1)
	if ok, _ := b.Reserve(time.Date(2026, 10, 31, 23, 45, 0, 0, time.UTC), 1); ok {
		t.Fatal("UTC month should still be October")
	}
}
