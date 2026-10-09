package feed

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/metrics"
	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Feed names, also used as URL path segments under /gtfs-rt/.
const (
	TripUpdates      = "trip-updates"
	VehiclePositions = "vehicle-positions"
	Alerts           = "alerts"
)

type rendered struct {
	msg     *rt.FeedMessage
	pb, gz  []byte
	etag    string
	lastMod time.Time
}

// Server renders the store on demand (at most once per MaxAge) and serves
// the three feeds plus health and metrics.
type Server struct {
	Store    *Store
	Alerts   *AlertStore
	Schedule func() *gtfs.Schedule
	Now      func() time.Time
	MaxAge   time.Duration
	Metrics  *metrics.Registry
	// Status adds fields to /healthz.
	Status func() map[string]any

	mu         sync.Mutex
	renderedAt time.Time
	feeds      map[string]*rendered
	stats      RenderStats
	alertStats AlertStats
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// refresh re-renders when the cached rendering is older than MaxAge.
func (s *Server) refresh() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.feeds != nil && now.Sub(s.renderedAt) < s.MaxAge {
		return nil
	}
	sched := s.Schedule()
	start := time.Now()
	snap := s.Store.Render(sched, now)
	alerts, alertStats := s.Alerts.Render(sched, now)
	feeds := map[string]*rendered{}
	for name, msg := range map[string]*rt.FeedMessage{
		TripUpdates: snap.TripUpdates, VehiclePositions: snap.VehiclePositions, Alerts: alerts,
	} {
		r, err := encode(msg, now)
		if err != nil {
			return err
		}
		feeds[name] = r
	}
	s.feeds, s.renderedAt, s.stats, s.alertStats = feeds, now, snap.Stats, alertStats
	if m := s.Metrics; m != nil {
		m.Set("rpgtfsrt_render_seconds", "Duration of the last feed render.", time.Since(start).Seconds())
		m.Set("rpgtfsrt_journeys", "Journeys held in the store.", float64(snap.Stats.Journeys))
		for _, o := range []string{"matched", "no_candidate", "ambiguous"} {
			m.Set("rpgtfsrt_journeys_by_outcome", "Stored journeys by match outcome at the last render.", float64(snap.Stats.Outcomes[o]), "outcome", o)
		}
		m.Set("rpgtfsrt_feed_entities", "Entities in each feed.", float64(snap.Stats.TripUpdates), "feed", TripUpdates)
		m.Set("rpgtfsrt_feed_entities", "Entities in each feed.", float64(snap.Stats.VehiclePositions), "feed", VehiclePositions)
		m.Set("rpgtfsrt_feed_entities", "Entities in each feed.", float64(alertStats.Published), "feed", Alerts)
		m.Set("rpgtfsrt_trips_cancelled", "Cancelled trips in the TripUpdates feed.", float64(snap.Stats.Cancelled))
		m.Set("rpgtfsrt_alerts_unresolved", "Active alerts left out because no affected stop is in the GTFS.", float64(alertStats.NoEntities))
		m.Set("rpgtfsrt_feed_timestamp_seconds", "Header timestamp of the served feeds.", float64(now.Unix()))
	}
	return nil
}

func encode(msg *rt.FeedMessage, now time.Time) (*rendered, error) {
	pb, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(pb)
	if err := zw.Close(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(pb)
	return &rendered{
		msg: msg, pb: pb, gz: buf.Bytes(),
		etag:    `"` + hex.EncodeToString(sum[:8]) + `"`,
		lastMod: now,
	}, nil
}

func (s *Server) feed(name string) (*rendered, error) {
	if err := s.refresh(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.feeds[name], nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, name := range []string{TripUpdates, VehiclePositions, Alerts} {
		mux.HandleFunc("GET /gtfs-rt/"+name, func(w http.ResponseWriter, r *http.Request) {
			s.serveFeed(w, r, name)
		})
	}
	mux.HandleFunc("GET /healthz", s.serveHealth)
	if s.Metrics != nil {
		mux.Handle("GET /metrics", s.Metrics.Handler())
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "rp-gtfsrt: Rejseplanen → GTFS-Realtime\n\n"+
			"/gtfs-rt/trip-updates\n/gtfs-rt/vehicle-positions\n/gtfs-rt/alerts\n"+
			"  (append ?format=json for a readable view)\n/healthz\n/metrics\n")
	})
	return mux
}

func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request, name string) {
	f, err := s.feed(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.Metrics != nil {
		s.Metrics.Inc("rpgtfsrt_feed_requests_total", "Feed requests served.", "feed", name)
	}
	if r.URL.Query().Get("format") == "json" {
		data, err := protojson.MarshalOptions{Multiline: true}.Marshal(f.msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
		return
	}
	w.Header().Set("ETag", f.etag)
	w.Header().Set("Last-Modified", f.lastMod.UTC().Format(http.TimeFormat))
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(s.MaxAge.Seconds())))
	w.Header().Set("Vary", "Accept-Encoding")
	if match := r.Header.Get("If-None-Match"); match != "" && match == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(f.gz)
		return
	}
	w.Write(f.pb)
}

func (s *Server) serveHealth(w http.ResponseWriter, _ *http.Request) {
	if err := s.refresh(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	body := map[string]any{
		"ok":          true,
		"rendered_at": s.renderedAt,
		"journeys":    s.stats,
		"alerts":      s.alertStats,
		"schedule":    s.Schedule().Stats(),
	}
	s.mu.Unlock()
	if s.Status != nil {
		for k, v := range s.Status() {
			body[k] = v
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}
