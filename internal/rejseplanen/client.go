package rejseplanen

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client fetches departure boards. BaseURL is the API root without a
// trailing endpoint, e.g. "https://www.rejseplanen.dk/api".
type Client struct {
	BaseURL  string
	AccessID string
	// Duration is the departure board window in minutes.
	Duration int
	// Products is the API's product bitmask (1 IC, 2 ICL, 4 regional,
	// 8 other train, 16 S-tog, ...). Zero sends no filter.
	Products int
	HTTP     *http.Client
	Location *time.Location
}

// DepartureBoard fetches upcoming departures for one stop.
func (c *Client) DepartureBoard(ctx context.Context, stopID string) ([]Departure, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: base url: %w", err)
	}
	u = u.JoinPath("departureBoard")
	q := u.Query()
	q.Set("accessId", c.AccessID)
	q.Set("id", stopID)
	q.Set("format", "json")
	if c.Products > 0 {
		q.Set("products", strconv.Itoa(c.Products))
	}
	if c.Duration > 0 {
		q.Set("duration", strconv.Itoa(c.Duration))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, fmt.Errorf("rejseplanen: http %d: %s", resp.StatusCode, snippet)
	}
	loc := c.Location
	if loc == nil {
		loc = time.UTC
	}
	return ParseDepartureBoard(body, stopID, loc)
}
