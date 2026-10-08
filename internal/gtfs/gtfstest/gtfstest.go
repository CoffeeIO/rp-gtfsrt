// Package gtfstest builds small GTFS feeds for tests.
package gtfstest

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Fixture is a tiny S-tog network. Thursday 2026-10-08 is the reference day.
//
//	A1  A  weekday  Hundige 07:50 → København H 08:12 → Nørreport 08:15 → Hillerød 08:50
//	A2  A  weekday  Hillerød 07:30 → Nørreport 08:09 → København H 08:12 → Hundige 08:35
//	A3  A  weekday  København H 24:30 → Hillerød 25:05 (night trip, no headsign)
//	B1  B  weekday  København H 08:12 → Høje Taastrup 08:40
//	W1  A  weekend  København H 08:12 → Hillerød 08:50
//	X1  A  extra    København H 09:00 → Hillerød 09:38 (only via calendar_dates)
//	BUS 1A (route_type 3) at København H 08:12, filtered out by default
//
// København H is a parent station (8600626) with platform child 8600626-1.
// Weekday service is removed on Friday 2026-10-09.
var Fixture = map[string]string{
	"agency.txt": "agency_id,agency_name,agency_url,agency_timezone\n" +
		"DSB,DSB S-tog,https://www.dsb.dk,Europe/Copenhagen\n",
	"stops.txt": "\uFEFFstop_id,stop_code,stop_name,location_type,parent_station\n" +
		"8600626,,København H,1,\n" +
		"8600626-1,,København H spor 9,0,8600626\n" +
		"8600646,8646,Nørreport St.,0,\n" +
		"8600669,,Hillerød St.,0,\n" +
		"8600601,,Hundige St.,0,\n" +
		"8600695,,Høje Taastrup St.,0,\n",
	"routes.txt": "route_id,agency_id,route_short_name,route_long_name,route_type\n" +
		"rA,DSB,A,,109\n" +
		"rB,DSB,B,,109\n" +
		"bus1A,DSB,1A,,3\n",
	"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
		"WD,1,1,1,1,1,0,0,20260101,20261231\n" +
		"WE,0,0,0,0,0,1,1,20260101,20261231\n",
	"calendar_dates.txt": "service_id,date,exception_type\n" +
		"WD,20261009,2\n" +
		"XS,20261008,1\n",
	"trips.txt": "route_id,service_id,trip_id,trip_headsign\n" +
		"rA,WD,A1,Hillerød\n" +
		"rA,WD,A2,Hundige\n" +
		"rA,WD,A3,\n" +
		"rB,WD,B1,Høje Taastrup\n" +
		"rA,WE,W1,Hillerød\n" +
		"rA,XS,X1,Hillerød\n" +
		"bus1A,WD,BUS,Centrum\n",
	// Deliberately out of sequence order to test sorting.
	"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence\n" +
		"A1,08:50:00,08:50:00,8600669,4\n" +
		"A1,07:50:00,07:50:00,8600601,1\n" +
		"A1,08:11:00,08:12:00,8600626-1,2\n" +
		"A1,08:15:00,08:15:30,8600646,3\n" +
		"A2,07:30:00,07:30:00,8600669,1\n" +
		"A2,08:09:00,08:09:00,8600646,2\n" +
		"A2,08:12:00,08:12:00,8600626-1,3\n" +
		"A2,08:35:00,08:35:00,8600601,4\n" +
		"A3,24:30:00,24:30:00,8600626-1,1\n" +
		"A3,25:05:00,25:05:00,8600669,2\n" +
		"B1,08:12:00,08:12:00,8600626-1,1\n" +
		"B1,08:40:00,08:40:00,8600695,2\n" +
		"W1,08:12:00,08:12:00,8600626-1,1\n" +
		"W1,08:50:00,08:50:00,8600669,2\n" +
		"X1,09:00:00,09:00:00,8600626-1,1\n" +
		"X1,09:38:00,09:38:00,8600669,2\n" +
		"BUS,08:12:00,08:12:00,8600626,1\n" +
		"BUS,08:30:00,08:30:00,8600646,2\n",
}

// Bytes zips files.
func Bytes(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Reader zips files and returns a reader over them.
func Reader(t testing.TB, files map[string]string) *zip.Reader {
	t.Helper()
	data := Bytes(t, files)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return zr
}

// WriteFile zips files into a temp dir and returns the path.
func WriteFile(t testing.TB, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gtfs.zip")
	if err := os.WriteFile(path, Bytes(t, files), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// With returns a copy of Fixture with some files replaced (empty value deletes).
func With(overrides map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range Fixture {
		out[k] = v
	}
	for k, v := range overrides {
		if v == "" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}
