package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newClient(url string) *Client {
	return &Client{BaseURL: url, UserAgent: "ctlog/test", RetryDelay: time.Millisecond}
}

func TestLookupSendsQueryAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/hostnames" {
			t.Errorf("path = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("q") != "example.com" || q.Get("include_apex") != "false" || q.Get("wildcards") != "exclude" || q.Get("format") != "json" {
			t.Errorf("query = %v", q)
		}
		if ua := r.Header.Get("User-Agent"); ua != "ctlog/test" {
			t.Errorf("User-Agent = %q", ua)
		}
		w.Write([]byte(`{"query":"example.com","domain":"example.com","hostnames":["a.example.com"],"returned":1,"total":7,"total_is_lower_bound":false,"truncated":true,"limit":100,"map_url":"https://sslboard.com/subdomain/example.com"}`))
	}))
	defer srv.Close()

	res, err := newClient(srv.URL+"/").Lookup(context.Background(), "example.com", Options{IncludeApex: false, ExcludeWildcards: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 7 || !res.Truncated || len(res.Hostnames) != 1 || res.MapURL == "" || len(res.Raw) == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestLookupRateLimitedIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"Rate limited: 1 search per minute without an account"}`))
	}))
	defer srv.Close()

	_, err := newClient(srv.URL).Lookup(context.Background(), "example.com", Options{IncludeApex: true})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || apiErr.RetryAfter != 42*time.Second {
		t.Fatalf("err = %#v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestLookupRetriesOnceOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"domain":"example.com","hostnames":["example.com"],"returned":1,"total":1}`))
	}))
	defer srv.Close()

	res, err := newClient(srv.URL).Lookup(context.Background(), "example.com", Options{IncludeApex: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || res.Total != 1 {
		t.Fatalf("calls = %d, res = %+v", calls.Load(), res)
	}
}

func TestLookupGivesUpAfterSecond502(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"error":"ct-storage unavailable"}`))
	}))
	defer srv.Close()

	_, err := newClient(srv.URL).Lookup(context.Background(), "example.com", Options{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Message != "ct-storage unavailable" {
		t.Fatalf("err = %#v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestLookupNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":"domain is a public suffix"}`))
	}))
	defer srv.Close()

	_, err := newClient(srv.URL).Lookup(context.Background(), "co.uk", Options{})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || calls.Load() != 1 {
		t.Fatalf("err = %#v, calls = %d", err, calls.Load())
	}
}

func TestLookupNonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>maintenance</html>"))
	}))
	defer srv.Close()

	if _, err := newClient(srv.URL).Lookup(context.Background(), "example.com", Options{}); err == nil {
		t.Fatal("expected error for non-JSON body")
	}
}

func TestInvalidBaseURL(t *testing.T) {
	for _, base := range []string{"", "sslboard.com", "ftp://sslboard.com"} {
		if _, err := newClient(base).Lookup(context.Background(), "example.com", Options{}); err == nil {
			t.Errorf("base %q: expected error", base)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"42":                            42 * time.Second,
		"-1":                            0,
		"soon":                          0,
		"Mon, 19 Oct 2026 12:00:30 GMT": 30 * time.Second,
		"Mon, 19 Oct 2026 11:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
