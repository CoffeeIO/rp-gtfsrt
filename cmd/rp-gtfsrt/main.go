// Command rp-gtfsrt bridges Rejseplanen live departures to a GTFS-Realtime
// TripUpdates feed matched against Rejseplanen's static GTFS.
//
//	rp-gtfsrt serve -gtfs-url https://www.rejseplanen.info/labs/GTFS.zip -hubs 000052887
//	rp-gtfsrt stops -gtfs GTFS.zip "København H"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // scratch/distroless images have no zoneinfo

	"github.com/CoffeeIO/rp-gtfsrt/internal/budget"
	"github.com/CoffeeIO/rp-gtfsrt/internal/feed"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/poller"
	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "stops":
		return stops(args)
	}
	return fmt.Errorf("unknown command %q (want serve or stops)", cmd)
}

// env returns the RP_<NAME> environment variable or def, so every flag can
// also be set from the environment (handy in Docker).
func env(name, def string) string {
	if v, ok := os.LookupEnv("RP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); ok {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(env(name, "")); err == nil {
		return v
	}
	return def
}

type common struct {
	gtfsPath, gtfsURL, cacheDir, routes, routeTypes, tz string
}

func (c *common) register(fs *flag.FlagSet) {
	fs.StringVar(&c.gtfsPath, "gtfs", env("gtfs", ""), "local GTFS zip (overrides -gtfs-url)")
	fs.StringVar(&c.gtfsURL, "gtfs-url", env("gtfs-url", "https://www.rejseplanen.info/labs/GTFS.zip"), "GTFS zip to download")
	fs.StringVar(&c.cacheDir, "cache-dir", env("cache-dir", "data"), "directory for downloaded GTFS and budget state")
	fs.StringVar(&c.routes, "routes", env("routes", "A,B,Bx,C,E,F,H"), "comma-separated route_short_names to load (empty = all)")
	fs.StringVar(&c.routeTypes, "route-types", env("route-types", ""), "comma-separated GTFS route_types to load (empty = all)")
	fs.StringVar(&c.tz, "tz", env("tz", "Europe/Copenhagen"), "feed time zone")
}

func (c *common) filter() (gtfs.Filter, error) {
	f := gtfs.Filter{RouteShortNames: map[string]bool{}, RouteTypes: map[int]bool{}}
	for _, r := range splitList(c.routes) {
		f.RouteShortNames[gtfs.NormalizeLine(r)] = true
	}
	for _, t := range splitList(c.routeTypes) {
		n, err := strconv.Atoi(t)
		if err != nil {
			return f, fmt.Errorf("bad route type %q", t)
		}
		f.RouteTypes[n] = true
	}
	return f, nil
}

func (c *common) load(ctx context.Context, loc *time.Location) (*gtfs.Schedule, error) {
	f, err := c.filter()
	if err != nil {
		return nil, err
	}
	path := c.gtfsPath
	if path == "" {
		if path, err = download(ctx, c.gtfsURL, c.cacheDir); err != nil {
			return nil, err
		}
	}
	return gtfs.LoadZip(path, f, loc)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseAliases(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range splitList(s) {
		api, gtfsID, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("bad stop alias %q (want api_id=gtfs_id)", pair)
		}
		out[strings.TrimSpace(api)] = strings.TrimSpace(gtfsID)
	}
	return out, nil
}

func download(ctx context.Context, url, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "gtfs.zip")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return "", fmt.Errorf("download gtfs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download gtfs: http %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "gtfs-*.zip")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return dest, os.Rename(tmp.Name(), dest)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var c common
	c.register(fs)
	var (
		addr         = fs.String("addr", env("addr", ":8080"), "HTTP listen address")
		baseURL      = fs.String("api-base", env("api-base", "https://www.rejseplanen.dk/api"), "Rejseplanen API base URL")
		accessID     = fs.String("access-id", env("access-id", ""), "Rejseplanen Labs accessId (or RP_ACCESS_ID)")
		hubs         = fs.String("hubs", env("hubs", ""), "comma-separated API stop ids to poll")
		aliases      = fs.String("stop-aliases", env("stop-aliases", ""), "comma-separated api_id=gtfs_stop_id overrides")
		products     = fs.Int("products", envInt("products", 16), "API product bitmask (16 = S-tog)")
		duration     = fs.Int("window", envInt("window", 60), "departure board window, minutes")
		limit        = fs.Int("monthly-budget", envInt("monthly-budget", 45000), "max API calls per month")
		minInterval  = fs.Duration("min-interval", 60*time.Second, "never poll faster than this")
		activeFrom   = fs.Int("active-from", envInt("active-from", 5), "polling window start hour (local)")
		activeTo     = fs.Int("active-to", envInt("active-to", 1), "polling window end hour (local, exclusive)")
		reload       = fs.Duration("gtfs-reload", 24*time.Hour, "how often to reload static GTFS")
		assumeOnTime = fs.Bool("assume-on-time", env("assume-on-time", "") == "true", "treat departures without rtTime as on time")
		verbose      = fs.Bool("v", false, "debug logging")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *accessID == "" {
		return errors.New("missing -access-id / RP_ACCESS_ID")
	}
	hubList := splitList(*hubs)
	if len(hubList) == 0 {
		return errors.New("missing -hubs / RP_HUBS")
	}
	stopAliases, err := parseAliases(*aliases)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(c.tz)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sched, err := c.load(ctx, loc)
	if err != nil {
		return err
	}
	logger.Info("gtfs loaded", "routes", len(sched.Routes), "trips", len(sched.Trips))
	var current atomic.Pointer[gtfs.Schedule]
	current.Store(sched)
	go func() {
		t := time.NewTicker(*reload)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s, err := c.load(ctx, loc); err != nil {
					logger.Error("gtfs reload failed; keeping previous", "err", err)
				} else {
					current.Store(s)
					logger.Info("gtfs reloaded", "trips", len(s.Trips))
				}
			}
		}
	}()

	b := &budget.Budget{
		Limit: *limit, MinInterval: *minInterval,
		ActiveFrom: *activeFrom, ActiveTo: *activeTo,
		Location: loc, StatePath: filepath.Join(c.cacheDir, "budget.json"),
	}
	if err := os.MkdirAll(c.cacheDir, 0o755); err != nil {
		return err
	}
	if err := b.Load(); err != nil {
		return fmt.Errorf("budget state: %w", err)
	}
	store := feed.NewStore()
	p := &poller.Poller{
		Source: &rejseplanen.Client{
			BaseURL: *baseURL, AccessID: *accessID, Duration: *duration,
			Products: *products, Location: loc,
		},
		Schedule:     current.Load,
		StopAliases:  stopAliases,
		Store:        store,
		Budget:       b,
		Hubs:         hubList,
		AssumeOnTime: *assumeOnTime,
		Logger:       logger,
	}
	srv := &http.Server{
		Addr:              *addr,
		ReadHeaderTimeout: 10 * time.Second,
		Handler: feed.Handler(store, time.Now, func() map[string]any {
			return map[string]any{"poller": p.Stats(), "budget_used": b.Used(time.Now()), "budget_limit": b.Limit}
		}),
	}
	go p.Run(ctx)
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	logger.Info("serving", "addr", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func stops(args []string) error {
	fs := flag.NewFlagSet("stops", flag.ContinueOnError)
	var c common
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rp-gtfsrt stops [flags] <name query>")
	}
	loc, err := time.LoadLocation(c.tz)
	if err != nil {
		return err
	}
	sched, err := c.load(context.Background(), loc)
	if err != nil {
		return err
	}
	for _, s := range sched.FindStops(strings.Join(fs.Args(), " ")) {
		fmt.Printf("%-14s code=%-10s parent=%-12s %s\n", s.ID, s.Code, s.Parent, s.Name)
	}
	return nil
}
