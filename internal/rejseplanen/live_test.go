package rejseplanen_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

// TestLiveDepartureBoard hits the real API. It runs only when RP_ACCESS_ID is
// set, spends one call, and checks the decoder still understands the live
// response shape.
//
//	RP_ACCESS_ID=... RP_LIVE_STOP=8600626 go test ./internal/rejseplanen -run Live -v
func TestLiveDepartureBoard(t *testing.T) {
	key := os.Getenv("RP_ACCESS_ID")
	if key == "" {
		t.Skip("RP_ACCESS_ID not set")
	}
	stop := os.Getenv("RP_LIVE_STOP")
	if stop == "" {
		t.Skip("RP_LIVE_STOP not set")
	}
	base := os.Getenv("RP_API_BASE")
	if base == "" {
		base = "https://www.rejseplanen.dk/api"
	}
	c := &rejseplanen.Client{BaseURL: base, AccessID: key, Products: 16, Duration: 30, Location: cph}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	deps, err := c.DepartureBoard(ctx, stop)
	if err != nil {
		t.Fatal(err)
	}
	withRT := 0
	for _, d := range deps {
		if d.Line == "" || d.Scheduled.IsZero() {
			t.Errorf("incomplete departure: %+v", d)
		}
		if d.Realtime != nil {
			withRT++
		}
		t.Logf("%-3s %s rt=%v delay=%v stop=%s dir=%s cancelled=%v",
			d.Line, d.Scheduled.Format("15:04"), d.Realtime != nil, d.Delay(), d.StopID, d.Direction, d.Cancelled)
	}
	t.Logf("%d departures, %d with realtime", len(deps), withRT)
}
