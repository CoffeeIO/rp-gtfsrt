package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
	"github.com/CoffeeIO/rp-gtfsrt/internal/scheduler"
	"github.com/CoffeeIO/rp-gtfsrt/internal/siri"
)

type serveConfig struct {
	common
	addr, baseURL, accessID, stopList, stopsFile, mode, journeys string
	source, siriURL                                              string
	products, window, maxJourneys, limit, activeFrom, activeTo   int
	concurrency, maxAlerts                                       int
	rate                                                         float64
	minInterval, boardInterval, journeyInterval, lookahead       time.Duration
	alertsInterval, renderInterval, reload, siriInterval         time.Duration
	assumeOnTime, verbose                                        bool
}

func parseServe(args []string) (*serveConfig, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	c := &serveConfig{}
	c.register(fs, "")
	fs.StringVar(&c.addr, "addr", env("addr", ":8080"), "HTTP listen address")
	fs.StringVar(&c.source, "source", env("source", "api"), "realtime source: api (Rejseplanen API) or siri (SIRI-ET document)")
	fs.StringVar(&c.siriURL, "siri-url", env("siri-url", ""), "siri source: URL of a SIRI-ET estimated timetable document")
	fs.DurationVar(&c.siriInterval, "siri-interval", envDuration("siri-interval", time.Minute), "siri source: fetch interval")
	fs.StringVar(&c.baseURL, "api-base", env("api-base", "https://www.rejseplanen.dk/api"), "Rejseplanen API base URL")
	fs.StringVar(&c.accessID, "access-id", env("access-id", ""), "Rejseplanen Labs accessId (or RP_ACCESS_ID)")
	fs.StringVar(&c.stopList, "stops", env("stops", ""), "comma-separated API stop ids whose departure boards are polled")
	fs.StringVar(&c.stopsFile, "stops-file", env("stops-file", ""), "file with one stop id per line (e.g. plan-stops output)")
	fs.StringVar(&c.mode, "mode", env("mode", "budget"), "pacing: budget (monthly cap, free key) or rate (fixed calls/second, operator)")
	fs.IntVar(&c.limit, "monthly-budget", envInt("monthly-budget", 20000), "budget mode: max API calls per month")
	fs.DurationVar(&c.minInterval, "min-interval", envDuration("min-interval", 15*time.Second), "budget mode: never call faster than this")
	fs.IntVar(&c.activeFrom, "active-from", envInt("active-from", 5), "budget mode: polling window start hour (local)")
	fs.IntVar(&c.activeTo, "active-to", envInt("active-to", 1), "budget mode: polling window end hour (local, exclusive)")
	fs.Float64Var(&c.rate, "rate", envFloat("rate", 10), "rate mode: API calls per second")
	fs.IntVar(&c.concurrency, "concurrency", envInt("concurrency", 4), "parallel API calls")
	fs.IntVar(&c.products, "products", envInt("products", 0), "departure board product bitmask (16 = S-tog, 0 = all)")
	fs.IntVar(&c.window, "window", envInt("window", 60), "departure board window, minutes")
	fs.IntVar(&c.maxJourneys, "max-journeys", envInt("max-journeys", -1), "departure board row limit (-1 = unlimited)")
	fs.DurationVar(&c.boardInterval, "board-interval", envDuration("board-interval", 2*time.Minute), "minimum time between polls of one stop")
	fs.StringVar(&c.journeys, "journeys", env("journeys", "cancelled"), "journeyDetail use: off, cancelled or all")
	fs.DurationVar(&c.journeyInterval, "journey-interval", envDuration("journey-interval", time.Minute), "refresh interval per journey")
	fs.DurationVar(&c.lookahead, "journey-lookahead", envDuration("journey-lookahead", 30*time.Minute), "refresh journeys departing within this")
	fs.DurationVar(&c.alertsInterval, "alerts-interval", envDuration("alerts-interval", 15*time.Minute), "traffic alert refresh (0 = off)")
	fs.IntVar(&c.maxAlerts, "max-alerts", envInt("max-alerts", 0), "himsearch result limit (0 = API default)")
	fs.DurationVar(&c.renderInterval, "render-interval", envDuration("render-interval", 5*time.Second), "re-render feeds at most this often")
	fs.DurationVar(&c.reload, "gtfs-reload", envDuration("gtfs-reload", 24*time.Hour), "how often to reload static GTFS")
	fs.BoolVar(&c.assumeOnTime, "assume-on-time", envBool("assume-on-time", false), "treat board rows without any prediction as on time")
	fs.BoolVar(&c.verbose, "v", envBool("verbose", false), "debug logging")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	switch c.source {
	case "api":
		if c.accessID == "" {
			return nil, errors.New("missing -access-id / RP_ACCESS_ID")
		}
	case "siri":
		if c.siriURL == "" {
			return nil, errors.New("missing -siri-url / RP_SIRI_URL")
		}
	default:
		return nil, fmt.Errorf("bad -source %q (want api or siri)", c.source)
	}
	switch c.mode {
	case "budget", "rate":
	default:
		return nil, fmt.Errorf("bad -mode %q (want budget or rate)", c.mode)
	}
	switch scheduler.JourneyMode(c.journeys) {
	case scheduler.JourneysOff, scheduler.JourneysCancelled, scheduler.JourneysAll:
	default:
		return nil, fmt.Errorf("bad -journeys %q (want off, cancelled or all)", c.journeys)
	}
	return c, nil
}

func (c *serveConfig) stops() ([]string, error) {
	list := splitList(c.stopList)
	if c.stopsFile != "" {
		more, err := readStopsFile(c.stopsFile)
		if err != nil {
			return nil, err
		}
		list = append(list, more...)
	}
	if len(list) == 0 {
		return nil, errors.New("missing -stops or -stops-file (see rp-gtfsrt plan-stops)")
	}
	return list, nil
}

func serve(args []string) error {
	c, err := parseServe(args)
	if err != nil {
		return err
	}
	var stopList []string
	if c.source == "api" {
		if stopList, err = c.stops(); err != nil {
			return err
		}
	}
	loc, err := c.location()
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if c.verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	reg := metrics.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	loadStart := time.Now()
	sched, err := c.load(ctx, loc)
	if err != nil {
		return err
	}
	logger.Info("gtfs loaded", "stats", sched.Stats(), "took", time.Since(loadStart).Round(time.Millisecond))
	reg.Set("rpgtfsrt_gtfs_loaded_timestamp_seconds", "When the static GTFS was last loaded.", float64(time.Now().Unix()))
	var current atomic.Pointer[gtfs.Schedule]
	current.Store(sched)
	go func() {
		t := time.NewTicker(c.reload)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s, err := c.load(ctx, loc)
				if err != nil {
					logger.Error("gtfs reload failed; keeping previous", "err", err)
					reg.Inc("rpgtfsrt_gtfs_reload_failures_total", "Failed static GTFS reloads.")
					continue
				}
				current.Store(s)
				reg.Set("rpgtfsrt_gtfs_loaded_timestamp_seconds", "When the static GTFS was last loaded.", float64(time.Now().Unix()))
				logger.Info("gtfs reloaded", "stats", s.Stats())
			}
		}
	}()

	// With -source siri the API is only used for alerts, and only with a key.
	useAPI := c.source == "api" || c.accessID != ""
	var pacer scheduler.Pacer
	var b *budget.Budget
	if useAPI && c.mode == "budget" {
		if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
			return err
		}
		b = &budget.Budget{
			Limit: c.limit, MinInterval: c.minInterval,
			ActiveFrom: c.activeFrom, ActiveTo: c.activeTo,
			Location: loc, StatePath: filepath.Join(c.cacheDir, "budget.json"),
		}
		if err := b.Load(); err != nil {
			return fmt.Errorf("budget state: %w", err)
		}
		pacer = &scheduler.BudgetPacer{Budget: b}
	} else {
		pacer = &scheduler.RatePacer{PerSecond: c.rate}
	}

	client := &rejseplanen.Client{
		BaseURL: c.baseURL, AccessID: c.accessID, Duration: c.window, Products: c.products,
		MaxJourneys: c.maxJourneys, MaxAlerts: c.maxAlerts, Location: loc,
		OnCall: func(endpoint string, err error, took time.Duration) {
			reg.Add("rpgtfsrt_api_seconds_total", "Time spent in API calls.", took.Seconds(), "endpoint", endpoint)
		},
	}
	store := feed.NewStore()
	alerts := &feed.AlertStore{}
	var poller *siri.Poller
	journeys := scheduler.JourneyMode(c.journeys)
	if c.source == "siri" {
		poller = &siri.Poller{URL: c.siriURL, Interval: c.siriInterval, Store: store, Metrics: reg, Logger: logger}
		// The API, if a key is given, only fetches traffic alerts.
		journeys = scheduler.JourneysOff
	}
	sch := &scheduler.Scheduler{
		API: client, Pacer: pacer, Store: store, Alerts: alerts, Metrics: reg, Logger: logger,
		Config: scheduler.Config{
			Stops: stopList, BoardInterval: c.boardInterval,
			JourneyMode: journeys, JourneyInterval: c.journeyInterval,
			JourneyLookahead: c.lookahead, AlertsInterval: c.alertsInterval,
			Concurrency: c.concurrency, AssumeOnTime: c.assumeOnTime,
		},
	}
	server := &feed.Server{
		Store: store, Alerts: alerts, Schedule: current.Load, MaxAge: c.renderInterval, Metrics: reg,
		Status: func() map[string]any {
			st := map[string]any{"scheduler": sch.Stats(), "mode": c.mode, "stops": len(stopList), "source": c.source}
			if poller != nil {
				st["siri"] = poller.Stats()
			}
			if b != nil {
				st["budget_used"], st["budget_limit"] = b.Used(time.Now()), b.Limit
			}
			return st
		},
	}
	srv := &http.Server{Addr: c.addr, ReadHeaderTimeout: 10 * time.Second, Handler: server.Handler()}
	if poller != nil {
		go poller.Run(ctx)
	}
	if useAPI {
		go sch.Run(ctx)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	logger.Info("serving", "addr", c.addr, "source", c.source, "mode", c.mode, "stops", len(stopList), "journeys", journeys)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
