// Package api is a small client for the SSLBoard public API: hostname
// listings (GET /api/v1/hostnames), the account behind an API key
// (GET /api/v1/me) and the device login used by `ctlog login`.
package api

import (
	"bytes"
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
	// After resumes a paged listing after this hostname (the previous
	// Result.Next). Paid plans only.
	After string
	// Limit is the page size; 0 means the server default for the plan.
	Limit int
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
	// Tier is "free" or "pro". Empty from servers older than API keys.
	Tier string `json:"tier"`
	// Next is the After value for the next page, empty on the last page.
	Next       string `json:"next"`
	MapURL     string `json:"map_url"`
	UpgradeURL string `json:"upgrade_url"`

	// Raw is the response body exactly as received.
	Raw []byte `json:"-"`
}

// Error is a non-2xx answer from the API.
type Error struct {
	Status  int
	Message string
	// Code is a machine-readable error such as "authorization_pending", set
	// by the device login endpoints.
	Code string
	// Tier is the X-Tier header: "pro" on a paid key's rate limit.
	Tier string
	// RetryAfter is set from the Retry-After header, when present.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("API returned HTTP %d", e.Status)
}

// Client calls the API. The zero value is not usable; set BaseURL and
// UserAgent.
type Client struct {
	BaseURL   string
	UserAgent string
	// APIKey is sent as a bearer token when set.
	APIKey     string
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
	body, err := c.send(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, errors.New("unexpected response from API (not JSON)")
	}
	if res.Hostnames == nil {
		res.Hostnames = []string{}
	}
	res.Raw = body
	return &res, nil
}

func (c *Client) base() (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("invalid API base URL %q", c.BaseURL)
	}
	return base, nil
}

func (c *Client) url(path string) (string, error) {
	base, err := c.base()
	if err != nil {
		return "", err
	}
	base.Path += path
	return base.String(), nil
}

func (c *Client) endpoint(domain string, opts Options) (string, error) {
	base, err := c.base()
	if err != nil {
		return "", err
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
	if opts.After != "" {
		q.Set("after", opts.After)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	base.RawQuery = q.Encode()
	return base.String(), nil
}

// send performs the request, retrying once on 502/503, and returns the body
// of a 2xx answer. Other statuses become *Error.
func (c *Client) send(ctx context.Context, method, endpoint string, payload any) ([]byte, error) {
	var reqBody []byte
	if payload != nil {
		var err error
		if reqBody, err = json.Marshal(payload); err != nil {
			return nil, err
		}
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
		body, err := c.do(ctx, method, endpoint, reqBody)
		var apiErr *Error
		if errors.As(err, &apiErr) && (apiErr.Status == http.StatusBadGateway || apiErr.Status == http.StatusServiceUnavailable) {
			lastErr = err
			continue
		}
		return body, err
	}
	return nil, lastErr
}

func (c *Client) do(ctx context.Context, method, endpoint string, reqBody []byte) ([]byte, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		bodyReader = bytes.NewReader(reqBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

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

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		message, code := errorMessage(body)
		return nil, &Error{
			Status:     resp.StatusCode,
			Message:    message,
			Code:       code,
			Tier:       resp.Header.Get("X-Tier"),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return body, nil
}

// errorMessage extracts {"error": "..."} from an error body, if present.
// Device login errors are {"error": "<code>", "error_description": "..."}.
func errorMessage(body []byte) (message, code string) {
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return "", ""
	}
	if payload.ErrorDescription != "" {
		return payload.ErrorDescription, payload.Error
	}
	return payload.Error, ""
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
