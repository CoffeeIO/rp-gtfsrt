package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/CoffeeIO/rp-gtfsrt/internal/coverage"
)

// planStops prints discovery stations (one per line: id, trips, name) whose
// departure boards together see every trip active on the given day.
func planStops(args []string, out, info io.Writer) error {
	fs := flag.NewFlagSet("plan-stops", flag.ContinueOnError)
	var c common
	c.register(fs, "")
	var (
		date       = fs.String("date", "", "service day YYYY-MM-DD (default today)")
		firstStops = fs.Int("first-stops", 3, "a trip is covered if one of its first N stops is chosen")
		limit      = fs.Int("limit", 0, "print at most N stations (0 = all)")
		outPath    = fs.String("out", "", "write the stations to this file instead of stdout")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}
	loc, err := c.location()
	if err != nil {
		return err
	}
	day := time.Now().In(loc)
	if *date != "" {
		if day, err = time.ParseInLocation("2006-01-02", *date, loc); err != nil {
			return fmt.Errorf("bad -date: %w", err)
		}
	}
	sched, err := c.load(context.Background(), loc)
	if err != nil {
		return err
	}
	plan := coverage.Plan(sched, day.Format("20060102"), *firstStops)
	sum := coverage.Summarize(plan)
	fmt.Fprintf(info, "# %d stations cover %d trips on %s (first %d stops)\n", sum.Stations, sum.Trips, day.Format("2006-01-02"), *firstStops)
	for _, share := range []int{50, 80, 90, 95, 99, 100} {
		if n, ok := sum.Top[share]; ok {
			fmt.Fprintf(info, "#   %3d%% of trips: %d stations\n", share, n)
		}
	}
	for i, st := range plan {
		if *limit > 0 && i == *limit {
			break
		}
		fmt.Fprintf(out, "%s\t%d\t%s\n", st.ID, st.Trips, st.Name)
	}
	return nil
}
