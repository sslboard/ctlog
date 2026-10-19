// Package api is a small client for the SSLBoard public hostname API
// (GET /api/v1/hostnames).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API origin.
const DefaultBaseURL = "https://sslboard.com"

// DefaultTimeout bounds each HTTP request.
const DefaultTimeout = 15 * time.Second

// maxBodyBytes guards against unexpectedly large responses.
const maxBodyBytes = 16 << 20

// Options are the lookup parameters besides the domain.
type Options struct {
	IncludeApex bool
	// ExcludeWildcards drops *.example.com style entries. By default they are
	// returned as stored, since many sites only have a wildcard certificate.
	ExcludeWildcards bool
}

// Result is the JSON body of a successful lookup.
type Result struct {
	Query             string   `json:"query"`
	Domain            string   `json:"domain"`
	Hostnames         []string `json:"hostnames"`
	Returned          int      `json:"returned"`
	Total             int64    `json:"total"`
	TotalIsLowerBound bool     `json:"total_is_lower_bound"`
	Truncated         bool     `json:"truncated"`
	Limit             int      `json:"limit"`
	MapURL            string   `json:"map_url"`

	// Raw is the response body exactly as received.
	Raw []byte `json:"-"`
}

// Error is a non-2xx answer from the API.
type Error struct {
	Status  int
	Message string
	// RetryAfter is set from the Retry-After header, when present.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("API returned HTTP %d", e.Status)
}

// Client calls the hostname API. The zero value is not usable; set BaseURL
// and UserAgent.
type Client struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
	// RetryDelay is the backoff before the single retry on 502/503.
	RetryDelay time.Duration
}

// Lookup fetches the hostnames for domain. A 502 or 503 is retried once;
// every other failure, including 429, is returned immediately.
func (c *Client) Lookup(ctx context.Context, domain string, opts Options) (*Result, error) {
	endpoint, err := c.endpoint(domain, opts)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.RetryDelay):
			}
		}
		res, err := c.do(ctx, endpoint)
		var apiErr *Error
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadGateway || apiErr.Status == http.StatusServiceUnavailable) {
			lastErr = err
			continue
		}
		return res, err
	}
	return nil, lastErr
}

func (c *Client) endpoint(domain string, opts Options) (string, error) {
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("invalid API base URL %q", c.BaseURL)
	}
	base.Path += "/api/v1/hostnames"

	wildcards := "include"
	if opts.ExcludeWildcards {
		wildcards = "exclude"
	}
	q := url.Values{}
	q.Set("q", domain)
	q.Set("include_apex", strconv.FormatBool(opts.IncludeApex))
	q.Set("wildcards", wildcards)
	q.Set("format", "json")
	base.RawQuery = q.Encode()
	return base.String(), nil
}

func (c *Client) do(ctx context.Context, endpoint string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &Error{
			Status:     resp.StatusCode,
			Message:    errorMessage(body),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}

	var res Result
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("unexpected response from API (HTTP %d, not JSON)", resp.StatusCode)
	}
	if res.Hostnames == nil {
		res.Hostnames = []string{}
	}
	res.Raw = body
	return &res, nil
}

// errorMessage extracts {"error": "..."} from an error body, if present.
func errorMessage(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		return payload.Error
	}
	return ""
}

// parseRetryAfter accepts delay-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now).Round(time.Second)
	}
	return 0
}
