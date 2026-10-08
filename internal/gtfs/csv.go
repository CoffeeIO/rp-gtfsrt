package gtfs

import (
	"encoding/csv"
	"errors"
	"io"
	"strings"
)

// eachRow streams a GTFS CSV file, handing each row to fn keyed by header.
func eachRow(r io.Reader, fn func(row map[string]string) error) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	cols := make([]string, len(header))
	for i, h := range header {
		cols[i] = strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF))))
	}
	row := make(map[string]string, len(cols))
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clear(row)
		for i, c := range cols {
			if i < len(rec) {
				row[c] = strings.TrimSpace(rec[i])
			}
		}
		if err := fn(row); err != nil {
			return err
		}
	}
}
