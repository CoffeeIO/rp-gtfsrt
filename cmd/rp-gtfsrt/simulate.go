package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/simulator"
)

// simulate serves a fake Rejseplanen API generated from GTFS, for load
// tests and demos without an API key: point serve's -api-base at it.
func simulate(args []string) error {
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	var c common
	c.register(fs, "")
	var (
		addr       = fs.String("addr", env("sim-addr", ":8090"), "HTTP listen address")
		delayRate  = fs.Float64("delay-rate", 0.3, "share of journeys running late")
		cancelRate = fs.Float64("cancel-rate", 0.01, "share of journeys cancelled")
		seed       = fs.Uint64("seed", 1, "random seed for delays")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	loc, err := c.location()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sched, err := c.load(ctx, loc)
	if err != nil {
		return err
	}
	sim := &simulator.Simulator{Schedule: sched, DelayRate: *delayRate, CancelRate: *cancelRate, Seed: *seed}
	srv := &http.Server{Addr: *addr, Handler: sim.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	slog.Info("simulated Rejseplanen API", "addr", *addr, "stats", sched.Stats(), "api_base", "http://localhost"+*addr+"/api")
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
