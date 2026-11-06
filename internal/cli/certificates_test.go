package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertificatesPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/certificates" || r.URL.Query().Get("q") != "example.com" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		if calls == 1 {
			w.Write([]byte(`{"certificates":[{"certificate_id":"a"}],"next":"opaque"}`))
		} else {
			if r.URL.Query().Get("after") != "opaque" {
				t.Error("cursor not forwarded")
			}
			w.Write([]byte(`{"certificates":[{"certificate_id":"a"},{"certificate_id":"b"}],"next":null}`))
		}
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	code := Run([]string{"certificates", "example.com", "--all"}, strings.NewReader(""), &out, &stderr,
		func(key string) string {
			if key == "CTLOG_API_URL" {
				return server.URL
			}
			if key == "SSLBOARD_API_KEY" {
				return "test-key"
			}
			return ""
		}, "test")
	if code != 0 || calls != 2 || strings.Count(out.String(), `"certificate_id":"a"`) != 1 {
		t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out.String(), stderr.String())
	}
}

func TestCertificatesEmptyPageAndPaidRateLimit(t *testing.T) {
	originalSleep := sleep
	defer func() { sleep = originalSleep }()
	waits := 0
	sleep = func(ctx context.Context, delay time.Duration) error { waits++; return nil }
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("include_subdomains") != "false" {
			t.Error("exact query not forwarded")
		}
		switch calls {
		case 1:
			w.Write([]byte(`{"certificates":[],"next":"opaque"}`))
		case 2:
			w.Header().Set("X-Tier", "pro")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limited"}`))
		default:
			w.Write([]byte(`{"certificates":[{"certificate_id":"a"}],"next":null}`))
		}
	}))
	defer server.Close()
	var out, stderr bytes.Buffer
	code := Run([]string{"certificates", "example.com", "--all", "--exact"}, strings.NewReader(""), &out, &stderr,
		func(key string) string {
			if key == "CTLOG_API_URL" {
				return server.URL
			}
			if key == "SSLBOARD_API_KEY" {
				return "test"
			}
			return ""
		}, "test")
	if code != 0 || waits != 1 || calls != 3 || !strings.Contains(out.String(), `"certificate_id":"a"`) {
		t.Fatalf("code=%d waits=%d calls=%d output=%s err=%s", code, waits, calls, out.String(), stderr.String())
	}
}
