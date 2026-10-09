package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
)

func TestSplitList(t *testing.T) {
	got := splitList(" A, B,,Bx ,")
	if !reflect.DeepEqual(got, []string{"A", "B", "Bx"}) {
		t.Fatalf("got %v", got)
	}
	if splitList("") != nil {
		t.Fatal("empty should be nil")
	}
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("RP_ACCESS_ID", "k")
	t.Setenv("RP_WINDOW", "45")
	t.Setenv("RP_PRODUCTS", "x")
	t.Setenv("RP_RATE", "2.5")
	t.Setenv("RP_BOARD_INTERVAL", "90s")
	t.Setenv("RP_ASSUME_ON_TIME", "true")
	if env("access-id", "d") != "k" || env("missing", "d") != "d" {
		t.Fatal("env")
	}
	if envInt("window", 1) != 45 || envInt("products", 16) != 16 {
		t.Fatal("envInt")
	}
	if envFloat("rate", 1) != 2.5 || envFloat("missing", 3) != 3 {
		t.Fatal("envFloat")
	}
	if envDuration("board-interval", 0) != 90*time.Second || envDuration("missing", time.Second) != time.Second {
		t.Fatal("envDuration")
	}
	if !envBool("assume-on-time", false) || envBool("missing", true) != true {
		t.Fatal("envBool")
	}
}

func TestFilter(t *testing.T) {
	c := common{routes: "a,Bx", routeTypes: "109, 2"}
	f, err := c.filter()
	if err != nil {
		t.Fatal(err)
	}
	if !f.RouteShortNames["A"] || !f.RouteShortNames["BX"] || !f.RouteTypes[109] || !f.RouteTypes[2] {
		t.Fatalf("filter = %+v", f)
	}
	if _, err := (&common{routeTypes: "bus"}).filter(); err == nil {
		t.Fatal("expected error")
	}
}

func TestRunDispatch(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v", err)
	}
}

func TestServeValidation(t *testing.T) {
	os.Unsetenv("RP_ACCESS_ID")
	os.Unsetenv("RP_STOPS")
	cases := map[string][]string{
		"access-id":         {"serve"},
		"-mode":             {"-access-id", "k", "-mode", "fast"},
		"-journeys":         {"-access-id", "k", "-journeys", "some"},
		"-stops":            {"-access-id", "k"},
		"no such file":      {"-access-id", "k", "-stops-file", "/nonexistent"},
		"unknown time zone": {"-access-id", "k", "-stops", "1", "-tz", "Mars/Olympus"},
	}
	for want, args := range cases {
		if err := run(args); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestStopsFromFlagAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stops.txt")
	os.WriteFile(path, []byte("# plan\n8600626\t120\tKøbenhavn H\n\n  8600646 Nørreport\n"), 0o644)
	c, err := parseServe([]string{"-access-id", "k", "-stops", "1,2", "-stops-file", path})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.stops()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "1,2,8600626,8600646" {
		t.Fatalf("stops = %v", got)
	}
}

func TestServeDefaults(t *testing.T) {
	c, err := parseServe([]string{"-access-id", "k"})
	if err != nil {
		t.Fatal(err)
	}
	if c.mode != "budget" || c.limit != 20000 || c.journeys != "cancelled" || c.maxJourneys != -1 || c.routes != "" {
		t.Fatalf("defaults = %+v", c)
	}
}

func TestStopsCommand(t *testing.T) {
	path := gtfstest.WriteFile(t, gtfstest.Fixture)
	var out bytes.Buffer
	if err := stops([]string{"-gtfs", path, "nørreport"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "8600646") || !strings.Contains(out.String(), "code=8646") {
		t.Fatalf("output = %q", out.String())
	}
	if err := stops([]string{"-gtfs", path}, &out); err == nil {
		t.Fatal("missing query should fail")
	}
}

func TestPlanStopsCommand(t *testing.T) {
	path := gtfstest.WriteFile(t, gtfstest.Fixture)
	var out, info bytes.Buffer
	if err := planStops([]string{"-gtfs", path, "-date", "2026-10-08", "-first-stops", "1"}, &out, &info); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "8600626\t4\tKøbenhavn H") {
		t.Fatalf("plan = %q", out.String())
	}
	if !strings.Contains(info.String(), "3 stations cover 6 trips") || !strings.Contains(info.String(), "100% of trips: 3 stations") {
		t.Fatalf("summary = %q", info.String())
	}
	out.Reset()
	planStops([]string{"-gtfs", path, "-date", "2026-10-08", "-limit", "1"}, &out, &info)
	if strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("limit = %q", out.String())
	}
	if err := planStops([]string{"-gtfs", path, "-date", "08/10/2026"}, &out, &info); err == nil {
		t.Fatal("bad date should fail")
	}
}

func TestPlanOutputIsAStopsFile(t *testing.T) {
	gtfsPath := gtfstest.WriteFile(t, gtfstest.Fixture)
	var out, info bytes.Buffer
	if err := planStops([]string{"-gtfs", gtfsPath, "-date", "2026-10-08"}, &out, &info); err != nil {
		t.Fatal(err)
	}
	stopsPath := filepath.Join(t.TempDir(), "stops.txt")
	os.WriteFile(stopsPath, out.Bytes(), 0o644)
	got, err := readStopsFile(stopsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "8600626" {
		t.Fatalf("stops = %v", got)
	}
}

func TestDownload(t *testing.T) {
	data := gtfstest.Bytes(t, gtfstest.Fixture)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "cache")
	path, err := download(context.Background(), srv.URL+"/GTFS.zip", dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if len(got) != len(data) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(data))
	}
	if _, err := download(context.Background(), srv.URL+"/missing", dir); err == nil {
		t.Fatal("404 should fail")
	}
	if again, _ := os.ReadFile(path); len(again) != len(data) {
		t.Fatal("cached gtfs was overwritten by a failed download")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestLoadViaURL(t *testing.T) {
	data := gtfstest.Bytes(t, gtfstest.Fixture)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer srv.Close()
	c := common{gtfsURL: srv.URL, cacheDir: t.TempDir(), routes: "A,B"}
	s, err := c.load(context.Background(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Trips) != 6 {
		t.Fatalf("trips = %d", len(s.Trips))
	}
}

func TestPlanStopsOut(t *testing.T) {
	path := gtfstest.WriteFile(t, gtfstest.Fixture)
	outPath := filepath.Join(t.TempDir(), "stops.txt")
	var stdout, info bytes.Buffer
	if err := planStops([]string{"-gtfs", path, "-date", "2026-10-08", "-out", outPath}, &stdout, &info); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatal("-out should not write to stdout")
	}
	got, err := readStopsFile(outPath)
	if err != nil || len(got) == 0 {
		t.Fatalf("stops file = %v, %v", got, err)
	}
	if err := planStops([]string{"-gtfs", path, "-out", "/nonexistent/dir/x"}, &stdout, &info); err == nil {
		t.Fatal("unwritable -out should fail")
	}
}
