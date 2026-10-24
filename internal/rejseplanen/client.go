package rejseplanen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client calls the Rejseplanen Labs API. BaseURL is the API root, e.g.
// "https://www.rejseplanen.dk/api".
type Client struct {
	BaseURL  string
	AccessID string
	// Duration is the departure board window in minutes.
	Duration int
	// Products is the product bitmask for departure boards (1 IC, 2 ICL,
	// 4 regional, 8 other train, 16 S-tog, 32 bus, ...). Zero sends none.
	Products int
	// MaxJourneys limits departure board rows; -1 means no limit. Zero
	// leaves the API default, which may truncate busy stations.
	MaxJourneys int
	// MaxAlerts limits himsearch results (maxNum). Zero uses the API default.
	MaxAlerts int
	HTTP      *http.Client
	Location  *time.Location
	// OnCall, if set, is called after every API request (for metrics).
	OnCall func(endpoint string, err error, took time.Duration)
}

// HTTPError is a non-200 API response.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("rejseplanen: http %d: %s", e.Status, e.Body)
}

// Temporary reports whether retrying later may succeed.
func (e *HTTPError) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// IsTemporary reports whether err is worth retrying.
func IsTemporary(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Temporary()
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

func (c *Client) loc() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values) (body []byte, err error) {
	start := time.Now()
	if c.OnCall != nil {
		defer func() { c.OnCall(endpoint, err, time.Since(start)) }()
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: base url: %w", err)
	}
	u = u.JoinPath(endpoint)
	params.Set("accessId", c.AccessID)
	params.Set("format", "json")
	u.RawQuery = params.Encode()

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
		// *url.Error carries the request URL; keep the accessId out of logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			params.Set("accessId", "redacted")
			u.RawQuery = params.Encode()
			ue.URL = u.String()
		}
		return nil, fmt.Errorf("rejseplanen: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("rejseplanen: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := body
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(snippet)}
	}
	return body, nil
}

// DepartureBoard fetches upcoming departures for one stop.
func (c *Client) DepartureBoard(ctx context.Context, stopID string) ([]Departure, error) {
	q := url.Values{"id": {stopID}}
	if c.Products > 0 {
		q.Set("products", strconv.Itoa(c.Products))
	}
	if c.Duration > 0 {
		q.Set("duration", strconv.Itoa(c.Duration))
	}
	if c.MaxJourneys != 0 {
		q.Set("maxJourneys", strconv.Itoa(c.MaxJourneys))
	}
	body, err := c.get(ctx, "departureBoard", q)
	if err != nil {
		return nil, err
	}
	return ParseDepartureBoard(body, stopID, c.loc())
}

// JourneyDetail fetches the full stop list of a journey.
func (c *Client) JourneyDetail(ctx context.Context, ref string) (Journey, error) {
	body, err := c.get(ctx, "journeyDetail", url.Values{"id": {ref}})
	if err != nil {
		return Journey{}, err
	}
	j, err := ParseJourneyDetail(body, c.loc())
	if err == nil && j.Ref == "" {
		j.Ref = ref
	}
	return j, err
}

// Alerts fetches current traffic information messages.
func (c *Client) Alerts(ctx context.Context) ([]Alert, error) {
	q := url.Values{}
	if c.MaxAlerts > 0 {
		q.Set("maxNum", strconv.Itoa(c.MaxAlerts))
	}
	body, err := c.get(ctx, "himsearch", q)
	if err != nil {
		return nil, err
	}
	return ParseAlerts(body, c.loc())
}
