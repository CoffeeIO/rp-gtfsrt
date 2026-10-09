package gtfs_test

import (
	"testing"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs"
	"github.com/CoffeeIO/rp-gtfsrt/internal/gtfs/gtfstest"
)

func TestParseTime(t *testing.T) {
	ok := map[string]int{
		"00:00:00": 0,
		"08:12:30": 8*3600 + 12*60 + 30,
		"24:30:00": 24*3600 + 30*60,
		"25:05:00": 25*3600 + 5*60,
		" 7:05:00": 7*3600 + 5*60,
		"":         -1,
	}
	for in, want := range ok {
		got, err := gtfs.ParseTime(in)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"8:12", "08:60:00", "08:00:60", "-1:00:00", "ab:cd:ef"} {
		if _, err := gtfs.ParseTime(bad); err == nil {
			t.Errorf("ParseTime(%q) should fail", bad)
		}
	}
}

func TestServiceDayStart(t *testing.T) {
	cases := []struct {
		name string
		day  time.Time
		want time.Time
	}{
		{"normal day is midnight", at(2026, 10, 8, 15, 0), at(2026, 10, 8, 0, 0)},
		// Spring forward 2026-03-29: noon CEST - 12h = 23:00 CET the day before.
		{"spring forward", at(2026, 3, 29, 9, 0), time.Date(2026, 3, 28, 23, 0, 0, 0, cph)},
		// Fall back 2026-10-25: noon CET - 12h = 01:00 CEST, an hour after midnight.
		{"fall back", at(2026, 10, 25, 9, 0), time.Date(2026, 10, 25, 1, 0, 0, 0, cph)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gtfs.ServiceDayStart(tc.day); !got.Equal(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCandidatesOnDSTDay(t *testing.T) {
	s := load(t, gtfsFixtureWeekdayEveryDay(), stogFilter())
	// On spring-forward Sunday a GTFS 08:12:00 departure is 08:12 wall clock.
	ms := s.Candidates("8600695", at(2026, 3, 29, 8, 40), true)
	if len(ms) != 1 || ms[0].Trip.ID != "B1" {
		t.Fatalf("matches = %v", ids(ms))
	}
	if got := s.ScheduledDeparture(ms[0], 0); !got.Equal(at(2026, 3, 29, 8, 12)) {
		t.Fatalf("ScheduledAt = %v", got)
	}
}

func gtfsFixtureWeekdayEveryDay() map[string]string {
	return gtfstest.With(map[string]string{
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"WD,1,1,1,1,1,1,1,20260101,20261231\n",
	})
}
