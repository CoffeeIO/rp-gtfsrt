// Command gtfs-subset writes a GTFS subset containing only routes of one route_type, plus the
// trips, stop_times, stops (and parents), calendars and agencies they use.
// It keeps validators fast on the national feed:
//
//	go run ./cmd/gtfs-subset GTFS.zip stog.zip 109
package main

import (
	"archive/zip"
	"encoding/csv"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: gtfs-subset <in.zip> <out.zip> <route_type>")
		os.Exit(2)
	}
	in, out, routeType := os.Args[1], os.Args[2], os.Args[3]
	zr, err := zip.OpenReader(in)
	check(err)
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	keep := map[string]map[string]bool{"route": {}, "trip": {}, "stop": {}, "service": {}, "agency": {}}
	// pass 1: collect ids
	scan(files["routes.txt"], func(h map[string]int, r []string) bool {
		if r[h["route_type"]] == routeType {
			keep["route"][r[h["route_id"]]] = true
			keep["agency"][r[h["agency_id"]]] = true
			return true
		}
		return false
	}, nil)
	scan(files["trips.txt"], func(h map[string]int, r []string) bool {
		if keep["route"][r[h["route_id"]]] {
			keep["trip"][r[h["trip_id"]]] = true
			keep["service"][r[h["service_id"]]] = true
			return true
		}
		return false
	}, nil)
	scan(files["stop_times.txt"], func(h map[string]int, r []string) bool {
		if keep["trip"][r[h["trip_id"]]] {
			keep["stop"][r[h["stop_id"]]] = true
			return true
		}
		return false
	}, nil)
	parents := map[string]bool{}
	scan(files["stops.txt"], func(h map[string]int, r []string) bool {
		if keep["stop"][r[h["stop_id"]]] && r[h["parent_station"]] != "" {
			parents[r[h["parent_station"]]] = true
		}
		return false
	}, nil)
	for p := range parents {
		keep["stop"][p] = true
	}
	// pass 2: write
	of, err := os.Create(out)
	check(err)
	zw := zip.NewWriter(of)
	filters := map[string]func(h map[string]int, r []string) bool{
		"agency.txt":         func(h map[string]int, r []string) bool { return keep["agency"][r[h["agency_id"]]] },
		"routes.txt":         func(h map[string]int, r []string) bool { return keep["route"][r[h["route_id"]]] },
		"trips.txt":          func(h map[string]int, r []string) bool { return keep["trip"][r[h["trip_id"]]] },
		"stop_times.txt":     func(h map[string]int, r []string) bool { return keep["trip"][r[h["trip_id"]]] },
		"stops.txt":          func(h map[string]int, r []string) bool { return keep["stop"][r[h["stop_id"]]] },
		"calendar.txt":       func(h map[string]int, r []string) bool { return keep["service"][r[h["service_id"]]] },
		"calendar_dates.txt": func(h map[string]int, r []string) bool { return keep["service"][r[h["service_id"]]] },
	}
	for name, fn := range filters {
		f, ok := files[name]
		if !ok {
			continue
		}
		w, err := zw.Create(name)
		check(err)
		cw := csv.NewWriter(w)
		n := scan(f, fn, cw)
		cw.Flush()
		fmt.Println(name, n)
	}
	check(zw.Close())
	check(of.Close())
}

func scan(f *zip.File, keep func(map[string]int, []string) bool, out *csv.Writer) int {
	rc, err := f.Open()
	check(err)
	defer rc.Close()
	cr := csv.NewReader(rc)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	check(err)
	h := map[string]int{}
	for i, c := range header {
		if i == 0 && len(c) >= 3 && c[:3] == "\xef\xbb\xbf" {
			c = c[3:]
		}
		h[c] = i
	}
	if out != nil {
		out.Write(header)
	}
	n := 0
	for {
		r, err := cr.Read()
		if err == io.EOF {
			return n
		}
		check(err)
		for len(r) < len(header) {
			r = append(r, "")
		}
		if keep(h, r) {
			n++
			if out != nil {
				out.Write(r)
			}
		}
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
