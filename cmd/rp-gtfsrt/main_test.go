package main

import (
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

func TestParseAliases(t *testing.T) {
	got, err := parseAliases("000052887=8600626, 1 = 2")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"000052887": "8600626", "1": "2"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := parseAliases("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("RP_ACCESS_ID", "k")
	t.Setenv("RP_WINDOW", "45")
	t.Setenv("RP_PRODUCTS", "x")
	if env("access-id", "d") != "k" || env("missing", "d") != "d" {
		t.Fatal("env")
	}
	if envInt("window", 1) != 45 || envInt("products", 16) != 16 {
		t.Fatal("envInt")
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
	t.Setenv("RP_ACCESS_ID", "")
	os.Unsetenv("RP_ACCESS_ID")
	os.Unsetenv("RP_HUBS")
	if err := run([]string{"serve"}); err == nil || !strings.Contains(err.Error(), "access-id") {
		t.Fatalf("err = %v", err)
	}
	if err := run([]string{"-access-id", "k"}); err == nil || !strings.Contains(err.Error(), "hubs") {
		t.Fatalf("default command should be serve; err = %v", err)
	}
	if err := run([]string{"-access-id", "k", "-hubs", "1", "-stop-aliases", "bad"}); err == nil {
		t.Fatal("bad aliases should fail")
	}
	if err := run([]string{"-access-id", "k", "-hubs", "1", "-tz", "Mars/Olympus"}); err == nil {
		t.Fatal("bad tz should fail")
	}
}

func TestStopsCommand(t *testing.T) {
	path := gtfstest.WriteFile(t, gtfstest.Fixture)
	out := captureStdout(t, func() {
		if err := run([]string{"stops", "-gtfs", path, "nørreport"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "8600646") || !strings.Contains(out, "code=8646") {
		t.Fatalf("output = %q", out)
	}
	if err := run([]string{"stops", "-gtfs", path}); err == nil {
		t.Fatal("missing query should fail")
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
	// A failed download must not clobber the cached file.
	if again, _ := os.ReadFile(path); len(again) != len(data) {
		t.Fatal("cached gtfs was overwritten")
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

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	w.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := r.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return buf.String()
}
