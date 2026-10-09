package e2e

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/coverage"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	"github.com/CoffeeIO/rp-gtfsrt/internal/scheduler"
	"github.com/CoffeeIO/rp-gtfsrt/internal/simulator"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

// TestWriteConformanceSnapshots runs the pipeline against the simulator for
// several simulated minutes and writes every feed iteration as protobuf,
// one directory per feed type, plus the GTFS it was matched against, for MobilityData's GTFS-Realtime
// validator (see .github/workflows/conformance.yml). It only runs when
// RP_SNAPSHOT_DIR is set. RP_GTFS_ZIP and RP_SNAPSHOT_START (RFC 3339)
// select a real feed and time; the default is the test fixture.
func TestWriteConformanceSnapshots(t *testing.T) {
	dir := os.Getenv("RP_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("RP_SNAPSHOT_DIR not set")
	}
	gtfsPath := os.Getenv("RP_GTFS_ZIP")
	if gtfsPath == "" {
		gtfsPath = gtfstest.WriteFile(t, gtfstest.Fixture)
	}
	now := time.Date(2026, 10, 8, 7, 45, 0, 0, cph)
	if v := os.Getenv("RP_SNAPSHOT_START"); v != "" {
		var err error
		if now, err = time.Parse(time.RFC3339, v); err != nil {
			t.Fatal(err)
		}
		now = now.In(cph)
	}
	sched, err := gtfs.LoadZip(gtfsPath, gtfs.Filter{}, cph)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"TripUpdates", "VehiclePositions", "Alerts"} {
		if err := os.MkdirAll(filepath.Join(dir, kind), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(gtfsPath)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "gtfs.zip"), data, 0o644)

	clock := func() time.Time { return now }
	sim := &simulator.Simulator{Schedule: sched, Now: clock, DelayRate: 0.4, CancelRate: 0.02, Seed: 1}
	api := httptest.NewServer(sim.Handler())
	defer api.Close()
	var stops []string
	for _, st := range coverage.Plan(sched, now.Format("20060102"), 3) {
		stops = append(stops, st.ID)
	}
	store, alerts := feed.NewStore(), &feed.AlertStore{}
	sch := &scheduler.Scheduler{
		API:   &rejseplanen.Client{BaseURL: api.URL + "/api", AccessID: "test", Duration: 60, MaxJourneys: -1, Location: cph},
		Pacer: &scheduler.RatePacer{}, Store: store, Alerts: alerts, Now: clock,
		Config: scheduler.Config{
			Stops: stops, BoardInterval: 5 * time.Minute, JourneyMode: scheduler.JourneysAll,
			JourneyInterval: time.Minute, JourneyLookahead: time.Hour, AlertsInterval: time.Hour,
		},
	}
	iterations := 6
	for i := 0; i < iterations; i++ {
		sch.RefreshTracked()
		for {
			task, _ := sch.Next(now)
			if task == nil {
				break
			}
			sch.Execute(context.Background(), task)
		}
		snap := store.Render(sched, now)
		alertMsg, _ := alerts.Render(sched, now)
		stamp := now.UTC().Format("2006-01-02T15-04-05Z")
		for name, msg := range map[string]*rt.FeedMessage{
			"TripUpdates": snap.TripUpdates, "VehiclePositions": snap.VehiclePositions, "Alerts": alertMsg,
		} {
			pb, err := proto.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name, fmt.Sprintf("%s-%s.pb", name, stamp))
			if err := os.WriteFile(path, pb, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s: %d trip updates, %d vehicles, %d alerts", stamp, len(snap.TripUpdates.Entity), len(snap.VehiclePositions.Entity), len(alertMsg.Entity))
		if i == iterations-1 && len(snap.TripUpdates.Entity) == 0 {
			t.Fatal("no trip updates produced; nothing to validate")
		}
		now = now.Add(time.Minute)
	}
}
