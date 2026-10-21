// Command demo-export turns a directory of recorded feed snapshots into the
// compact JSON the static demo page (demo/) replays.
//
// The recorder saves, once a minute, <ts>.trip-updates.pb,
// <ts>.vehicle-positions.pb, <ts>.alerts.pb and <ts>.healthz.json, where
// <ts> is local time as 20060102T150405. The output holds the scheduled
// stop times of every trip seen in the feed, a timeline of each trip's
// realtime state (only when it changes), and per-minute feed statistics.
//
//	go run ./cmd/demo-export -gtfs data/gtfs.zip -snapshots rec/snapshots/20261022 -out demo/data/day.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	rt "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
)

const skipped = -1 << 20 // per-stop delay marker for a SKIPPED stop

type output struct {
	Date      string            `json:"date"`
	Generated string            `json:"generated"`
	Source    string            `json:"source"`
	Stations  []string          `json:"stations"` // board stations, display names
	Stops     [][3]any          `json:"stops"`    // [name, lat, lon]
	Routes    map[string][]int  `json:"routes"`   // route -> stop indexes of its longest trip
	Trips     []*trip           `json:"trips"`
	Frames    [][]int64         `json:"frames"` // [unix, trips, cancelled, vehicles, alerts, apiCalls]
	Alerts    []alert           `json:"alerts"`
	Samples   map[string]string `json:"samples"` // feed -> sample file name
}

type trip struct {
	ID       string    `json:"id"`
	Route    string    `json:"r"`
	Headsign string    `json:"h"`
	Base     int64     `json:"b"`  // unix time of the service day reference
	Stops    [][3]int  `json:"s"`  // [stop index, arrival, departure] seconds from Base
	States   [][]int64 `json:"st"` // [unix, cancelled 0/1, delay per stop...]
	Vehicle  [][3]any  `json:"v,omitempty"`
	LastSeen int64     `json:"l"`

	stopSeq []int32 `json:"-"`
	last    []int64 `json:"-"`
}

type alert struct {
	From   int64    `json:"f"`
	To     int64    `json:"t"`
	Header string   `json:"h"`
	Text   string   `json:"d"`
	Stops  []string `json:"s"`
}

func main() {
	gtfsPath := flag.String("gtfs", "data/gtfs.zip", "static GTFS zip the feed was matched against")
	dir := flag.String("snapshots", "", "directory with recorded snapshots for one day")
	out := flag.String("out", "demo/data/day.json", "output JSON")
	routes := flag.String("routes", "A,B,Bx,C,E,F,H", "route_short_names to load")
	stations := flag.String("stations", "København H,Nørreport St.", "board stations shown first")
	samples := flag.String("samples", "", "if set, copy the snapshot nearest to this local time (15:04) into the output directory")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-snapshots is required")
		os.Exit(2)
	}
	loc, err := time.LoadLocation("Europe/Copenhagen")
	check(err)
	filter := gtfs.Filter{RouteShortNames: map[string]bool{}}
	for _, route := range strings.Split(*routes, ",") {
		filter.RouteShortNames[route] = true
	}
	sched, err := gtfs.LoadZip(*gtfsPath, filter, loc)
	check(err)

	timestamps := snapshotTimes(*dir)
	if len(timestamps) == 0 {
		check(fmt.Errorf("no snapshots in %s", *dir))
	}
	result := &output{
		Generated: time.Now().In(loc).Format(time.RFC3339),
		Source:    "Rejseplanen Labs API via rp-gtfsrt (budget mode, S-tog)",
		Stations:  strings.Split(*stations, ","),
		Routes:    map[string][]int{},
		Samples:   map[string]string{},
	}
	stopIndex := map[int32]int{}
	stopOf := func(i int32) int {
		if j, ok := stopIndex[i]; ok {
			return j
		}
		s := sched.Stops[i]
		stopIndex[i] = len(result.Stops)
		result.Stops = append(result.Stops, [3]any{s.Name, round5(s.Lat), round5(s.Lon)})
		return stopIndex[i]
	}
	trips := map[string]*trip{}
	alerts := map[string]*alert{}
	var first time.Time

	for _, ts := range timestamps {
		at, err := time.ParseInLocation("20060102T150405", ts, loc)
		check(err)
		if first.IsZero() {
			first = at
		}
		tu := readFeed(filepath.Join(*dir, ts+".trip-updates.pb"))
		vp := readFeed(filepath.Join(*dir, ts+".vehicle-positions.pb"))
		al := readFeed(filepath.Join(*dir, ts+".alerts.pb"))
		if tu == nil {
			continue
		}
		var cancelled int64
		for _, entity := range tu.GetEntity() {
			update := entity.GetTripUpdate()
			if update == nil {
				continue
			}
			key := update.GetTrip().GetTripId() + "/" + update.GetTrip().GetStartDate()
			tr := trips[key]
			if tr == nil {
				tr = newTrip(sched, update.GetTrip(), loc, stopOf)
				if tr == nil {
					continue
				}
				trips[key] = tr
			}
			state := tr.state(update)
			if state[1] == 1 {
				cancelled++
			}
			if !slices.Equal(state, tr.last) {
				tr.States = append(tr.States, append([]int64{at.Unix()}, state[1:]...))
				tr.last = state
			}
			tr.LastSeen = at.Unix()
		}
		var vehicles int64
		if vp != nil {
			for _, entity := range vp.GetEntity() {
				v := entity.GetVehicle()
				if v == nil || v.GetPosition() == nil {
					continue
				}
				vehicles++
				tr := trips[v.GetTrip().GetTripId()+"/"+v.GetTrip().GetStartDate()]
				if tr == nil {
					continue
				}
				lat, lon := round5(float64(v.GetPosition().GetLatitude())), round5(float64(v.GetPosition().GetLongitude()))
				if n := len(tr.Vehicle); n == 0 || tr.Vehicle[n-1][1] != lat || tr.Vehicle[n-1][2] != lon {
					tr.Vehicle = append(tr.Vehicle, [3]any{at.Unix(), lat, lon})
				}
			}
		}
		var activeAlerts int64
		if al != nil {
			for _, entity := range al.GetEntity() {
				a := entity.GetAlert()
				if a == nil {
					continue
				}
				activeAlerts++
				if existing := alerts[entity.GetId()]; existing != nil {
					existing.To = at.Unix()
					continue
				}
				na := &alert{From: at.Unix(), To: at.Unix(), Header: text(a.GetHeaderText()), Text: text(a.GetDescriptionText())}
				for _, informed := range a.GetInformedEntity() {
					if id := informed.GetStopId(); id != "" {
						for _, i := range sched.StopIndexes(id) {
							na.Stops = append(na.Stops, sched.Stops[i].Name)
						}
					}
				}
				na.Stops = uniq(na.Stops)
				alerts[entity.GetId()] = na
			}
		}
		result.Frames = append(result.Frames, []int64{at.Unix(), int64(len(tu.GetEntity())), cancelled, vehicles, activeAlerts, apiCalls(filepath.Join(*dir, ts+".healthz.json"))})
	}
	result.Date = first.Format("2006-01-02")

	for _, tr := range trips {
		result.Trips = append(result.Trips, tr)
		if len(tr.Stops) > len(result.Routes[tr.Route]) {
			line := make([]int, len(tr.Stops))
			for i, s := range tr.Stops {
				line[i] = s[0]
			}
			result.Routes[tr.Route] = line
		}
	}
	sort.Slice(result.Trips, func(i, j int) bool {
		return result.Trips[i].Base+int64(result.Trips[i].Stops[0][2]) < result.Trips[j].Base+int64(result.Trips[j].Stops[0][2])
	})
	for _, a := range alerts {
		result.Alerts = append(result.Alerts, *a)
	}
	sort.Slice(result.Alerts, func(i, j int) bool { return result.Alerts[i].From < result.Alerts[j].From })

	outDir := filepath.Dir(*out)
	check(os.MkdirAll(outDir, 0o755))
	if *samples != "" {
		ts := nearest(timestamps, first.Format("20060102")+"T"+strings.ReplaceAll(*samples, ":", "")+"00")
		for _, feed := range []string{"trip-updates", "vehicle-positions", "alerts"} {
			name := "sample-" + feed + ".pb"
			data, err := os.ReadFile(filepath.Join(*dir, ts+"."+feed+".pb"))
			check(err)
			check(os.WriteFile(filepath.Join(outDir, name), data, 0o644))
			result.Samples[feed] = name
		}
		result.Samples["at"] = ts
	}
	data, err := json.Marshal(result)
	check(err)
	check(os.WriteFile(*out, data, 0o644))
	fmt.Printf("%s: %d snapshots, %d trips, %d alerts, %d KB\n", *out, len(result.Frames), len(result.Trips), len(result.Alerts), len(data)/1024)
}

func newTrip(sched *gtfs.Schedule, desc *rt.TripDescriptor, loc *time.Location, stopOf func(int32) int) *trip {
	t, ok := sched.TripByID(desc.GetTripId())
	if !ok {
		return nil
	}
	day, err := time.ParseInLocation("20060102", desc.GetStartDate(), loc)
	if err != nil {
		return nil
	}
	tr := &trip{
		ID:       t.ID,
		Route:    sched.RouteOf(t).ShortName,
		Headsign: sched.LastStopName(t),
		Base:     gtfs.ServiceDayStart(day).Unix(),
	}
	for _, st := range sched.StopTimes(t) {
		tr.Stops = append(tr.Stops, [3]int{stopOf(st.Stop), int(st.Arrival), int(st.Departure)})
		tr.stopSeq = append(tr.stopSeq, st.Sequence)
	}
	if len(tr.Stops) == 0 {
		return nil
	}
	return tr
}

// state is [0, cancelled, delay at each stop], with GTFS-RT delay
// propagation applied so the page does not have to.
func (tr *trip) state(update *rt.TripUpdate) []int64 {
	state := make([]int64, 2+len(tr.Stops))
	if update.GetTrip().GetScheduleRelationship() == rt.TripDescriptor_CANCELED {
		state[1] = 1
		return state
	}
	bySeq := map[uint32]*rt.TripUpdate_StopTimeUpdate{}
	for _, stu := range update.GetStopTimeUpdate() {
		bySeq[stu.GetStopSequence()] = stu
	}
	var delay int64
	for i, seq := range tr.stopSeq {
		if stu := bySeq[uint32(seq)]; stu != nil {
			if stu.GetScheduleRelationship() == rt.TripUpdate_StopTimeUpdate_SKIPPED {
				state[2+i] = skipped
				continue
			}
			if ev := stu.GetDeparture(); ev != nil {
				delay = int64(ev.GetDelay())
			} else if ev := stu.GetArrival(); ev != nil {
				delay = int64(ev.GetDelay())
			}
		}
		state[2+i] = delay
	}
	return state
}

func snapshotTimes(dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.trip-updates.pb"))
	check(err)
	var out []string
	for _, m := range matches {
		out = append(out, strings.TrimSuffix(filepath.Base(m), ".trip-updates.pb"))
	}
	sort.Strings(out)
	return out
}

func nearest(sorted []string, want string) string {
	i := sort.SearchStrings(sorted, want)
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func readFeed(path string) *rt.FeedMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	msg := &rt.FeedMessage{}
	if proto.Unmarshal(data, msg) != nil {
		return nil
	}
	return msg
}

func apiCalls(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var health struct {
		BudgetUsed int64 `json:"budget_used"`
	}
	json.Unmarshal(data, &health)
	return health.BudgetUsed
}

func text(ts *rt.TranslatedString) string {
	for _, tr := range ts.GetTranslation() {
		return tr.GetText()
	}
	return ""
}

func uniq(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func round5(v float64) float64 {
	return float64(int64(v*1e5+0.5)) / 1e5
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
