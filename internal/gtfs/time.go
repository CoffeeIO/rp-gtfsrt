package gtfs

import (
	"fmt"
	"strings"
	"time"
)

// ParseTime parses a GTFS HH:MM:SS time into seconds. Hours may exceed 23.
// An empty value returns -1.
func ParseTime(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return -1, nil
	}
	var h, m, sec int
	if n, err := fmt.Sscanf(v, "%d:%d:%d", &h, &m, &sec); err != nil || n != 3 || m > 59 || sec > 59 || h < 0 || m < 0 || sec < 0 {
		return 0, fmt.Errorf("gtfs: bad time %q", v)
	}
	return h*3600 + m*60 + sec, nil
}

// ServiceDayStart is the GTFS reference point for a service day: noon local
// time minus 12 hours. On DST change days this differs from midnight.
func ServiceDayStart(day time.Time) time.Time {
	noon := time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, day.Location())
	return noon.Add(-12 * time.Hour)
}
