package feed

import (
	"encoding/json"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// StatusFunc returns extra fields for /healthz.
type StatusFunc func() map[string]any

// Handler serves:
//
//	/gtfs-rt/trip-updates            protobuf (application/x-protobuf)
//	/gtfs-rt/trip-updates?format=json  protojson, for humans
//	/healthz                         JSON status
func Handler(store *Store, now func() time.Time, status StatusFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gtfs-rt/trip-updates", func(w http.ResponseWriter, r *http.Request) {
		msg := store.Message(now())
		if r.URL.Query().Get("format") == "json" {
			data, err := protojson.MarshalOptions{Multiline: true}.Marshal(msg)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(data)
			return
		}
		data, err := proto.Marshal(msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(data)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{"ok": true, "entities": store.Len()}
		if status != nil {
			for k, v := range status() {
				body[k] = v
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	})
	return mux
}
