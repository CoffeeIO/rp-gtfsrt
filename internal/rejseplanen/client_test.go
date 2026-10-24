package rejseplanen_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/rejseplanen"
)

func TestClientRequest(t *testing.T) {
	var gotPath string
	var gotQuery map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = map[string]string{}
		for k := range r.URL.Query() {
			gotQuery[k] = r.URL.Query().Get(k)
		}
		w.Write([]byte(`{"Departure":[{"name":"A","time":"08:12:00","date":"2026-10-08","rtTime":"08:14:00"}]}`))
	}))
	defer srv.Close()

	c := &rejseplanen.Client{BaseURL: srv.URL + "/api", AccessID: "secret", Duration: 60, Products: 16, Location: cph}
	deps, err := c.DepartureBoard(context.Background(), "000052887")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/departureBoard" {
		t.Fatalf("path = %q", gotPath)
	}
	want := map[string]string{"accessId": "secret", "id": "000052887", "format": "json", "duration": "60", "products": "16"}
	for k, v := range want {
		if gotQuery[k] != v {
			t.Errorf("query %s = %q, want %q", k, gotQuery[k], v)
		}
	}
	if len(deps) != 1 || deps[0].StopID != "000052887" || deps[0].Delay() != 2*time.Minute {
		t.Fatalf("deps = %+v", deps)
	}
}

func TestClientOmitsZeroOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("duration") || r.URL.Query().Has("products") {
			t.Errorf("unexpected params: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"Departure":[]}`))
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k"}
	if _, err := c.DepartureBoard(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("quota exceeded ", 50), http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k"}
	_, err := c.DepartureBoard(context.Background(), "1")
	if err == nil || !strings.Contains(err.Error(), "http 429") || len(err.Error()) > 400 {
		t.Fatalf("err = %v", err)
	}
}

func TestClientAPIErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errorCode":"API_AUTH","errorText":"invalid accessId"}`))
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "bad"}
	if _, err := c.DepartureBoard(context.Background(), "1"); err == nil || !strings.Contains(err.Error(), "API_AUTH") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k"}
	if _, err := c.DepartureBoard(ctx, "1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestClientTransportErrorHidesAccessID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "secret", HTTP: &http.Client{Timeout: 50 * time.Millisecond}}
	_, err := c.DepartureBoard(context.Background(), "1")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "accessId=redacted") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientBadBaseURL(t *testing.T) {
	c := &rejseplanen.Client{BaseURL: "://nope", AccessID: "k"}
	if _, err := c.DepartureBoard(context.Background(), "1"); err == nil || !strings.Contains(err.Error(), "base url") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientDefaultsToUTC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Departure":[{"time":"08:12:00","date":"2026-10-08"}]}`))
	}))
	defer srv.Close()
	c := &rejseplanen.Client{BaseURL: srv.URL, AccessID: "k"}
	deps, err := c.DepartureBoard(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	if deps[0].Scheduled.Location() != time.UTC {
		t.Fatalf("loc = %v", deps[0].Scheduled.Location())
	}
}
