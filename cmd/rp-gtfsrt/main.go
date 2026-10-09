// Command rp-gtfsrt bridges Rejseplanen realtime data to GTFS-Realtime
// TripUpdates, VehiclePositions and Alerts feeds, matched against
// Rejseplanen's static GTFS.
//
//	rp-gtfsrt serve      run the bridge
//	rp-gtfsrt plan-stops choose discovery stations covering every trip
//	rp-gtfsrt stops      search stop ids by name
//	rp-gtfsrt simulate   run a fake Rejseplanen API generated from GTFS
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // scratch/distroless images have no zoneinfo

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
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
	case "plan-stops":
		return planStops(args, os.Stdout, os.Stderr)
	case "stops":
		return stops(args, os.Stdout)
	case "simulate":
		return simulate(args)
	}
	return fmt.Errorf("unknown command %q (want serve, plan-stops, stops or simulate)", cmd)
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

func envFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(env(name, ""), 64); err == nil {
		return v
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(name, "")); err == nil {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	if v, err := strconv.ParseBool(env(name, "")); err == nil {
		return v
	}
	return def
}

// common holds the static GTFS options shared by all commands.
type common struct {
	gtfsPath, gtfsURL, cacheDir, routes, routeTypes, tz string
}

func (c *common) register(fs *flag.FlagSet, defaultRoutes string) {
	fs.StringVar(&c.gtfsPath, "gtfs", env("gtfs", ""), "local GTFS zip (overrides -gtfs-url)")
	fs.StringVar(&c.gtfsURL, "gtfs-url", env("gtfs-url", "https://www.rejseplanen.info/labs/GTFS.zip"), "GTFS zip to download")
	fs.StringVar(&c.cacheDir, "cache-dir", env("cache-dir", "data"), "directory for downloaded GTFS and budget state")
	fs.StringVar(&c.routes, "routes", env("routes", defaultRoutes), "comma-separated route_short_names to load (empty = all)")
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

func (c *common) location() (*time.Location, error) {
	return time.LoadLocation(c.tz)
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

// readStopsFile reads stop ids, one per line; text after the first tab or
// space and lines starting with # are ignored (plan-stops output works as is).
func readStopsFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out, sc.Err()
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
		os.Remove(tmp.Name())
		return "", err
	}
	return dest, os.Rename(tmp.Name(), dest)
}

func stops(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stops", flag.ContinueOnError)
	var c common
	c.register(fs, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: rp-gtfsrt stops [flags] <name query>")
	}
	loc, err := c.location()
	if err != nil {
		return err
	}
	sched, err := c.load(context.Background(), loc)
	if err != nil {
		return err
	}
	for _, s := range sched.FindStops(strings.Join(fs.Args(), " ")) {
		fmt.Fprintf(out, "%-14s api=%-10s code=%-10s parent=%-12s %s\n", s.ID, gtfs.StopKey(s.ID), s.Code, s.Parent, s.Name)
	}
	return nil
}
