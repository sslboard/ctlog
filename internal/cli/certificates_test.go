package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sslboard/ctlog/internal/api"
)

func TestCertificatesFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, message string
		calls               int
	}{
		{"missing array", `{}`, "invalid certificate response", 1},
		{"null array", `{"certificates":null}`, "invalid certificate response", 1},
		{"wrong array type", `{"certificates":{}}`, "invalid certificate response", 1},
		{"missing ID", `{"certificates":[{}]}`, "certificate missing ID", 1},
		{"null certificate", `{"certificates":[null]}`, "certificate missing ID", 1},
		{"wrong ID type", `{"certificates":[{"certificate_id":123}]}`, "cannot unmarshal", 1},
		{"wrong cursor type", `{"certificates":[],"next":123}`, "cannot unmarshal", 1},
		{"repeated cursor", `{"certificates":[{"certificate_id":"a"}],"next":"same"}`, "repeated a certificate cursor", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > tc.calls {
					t.Error("unexpected extra request")
					io.WriteString(w, `{"certificates":[]}`)
					return
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			e := &env{stdout: &out, stderr: &stderr, client: &api.Client{BaseURL: server.URL}}
			code := runCertificates(context.Background(), e, options{all: true}, []string{"example.com"})
			if code != ExitAPI || calls != tc.calls || out.Len() != 0 || !strings.Contains(stderr.String(), tc.message) {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out.String(), stderr.String())
			}
		})
	}
}

func TestCertificatesOutputFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"certificates":[{"certificate_id":"a"}]}`)
	}))
	defer server.Close()
	reader, writer := io.Pipe()
	reader.Close()
	defer writer.Close()
	var stderr bytes.Buffer
	e := &env{stdout: writer, stderr: &stderr, client: &api.Client{BaseURL: server.URL}}
	if code := runCertificates(context.Background(), e, options{}, []string{"example.com"}); code != ExitAPI || !strings.Contains(stderr.String(), io.ErrClosedPipe.Error()) {
		t.Fatalf("code=%d err=%s", code, stderr.String())
	}
}

func TestCertificatesSinglePage(t *testing.T) {
	for _, tc := range []struct {
		name, body, want   string
		all, quiet, notice bool
	}{
		{"first page only", `{"certificates":[{"certificate_id":"a","extra":42},{"certificate_id":"a"}],"next":"more","truncated":true}`, `{"certificates":[{"certificate_id":"a","extra":42}],"next":"more","truncated":true}`, false, false, true},
		{"quiet", `{"certificates":[],"next":"more","truncated":true}`, `{"certificates":[],"next":"more","truncated":true}`, false, true, false},
		{"missing cursor", `{"certificates":[],"extra":42}`, `{"certificates":[],"extra":42}`, true, false, false},
		{"null cursor", `{"certificates":[],"next":null}`, `{"certificates":[],"next":null}`, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 1 {
					t.Error("unexpected extra request")
					io.WriteString(w, `{"certificates":[]}`)
					return
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var out, stderr bytes.Buffer
			e := &env{stdout: &out, stderr: &stderr, client: &api.Client{BaseURL: server.URL}}
			code := runCertificates(context.Background(), e, options{all: tc.all, quiet: tc.quiet}, []string{"example.com"})
			if code != ExitOK || calls != 1 || strings.TrimSpace(out.String()) != tc.want {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out.String(), stderr.String())
			}
			if got := strings.Contains(stderr.String(), "More recent certificates"); got != tc.notice {
				t.Fatalf("notice=%v want=%v stderr=%s", got, tc.notice, stderr.String())
			}
		})
	}
}

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
	if code != ExitOK || calls != 2 || strings.TrimSpace(out.String()) != `{"certificates":[{"certificate_id":"a"},{"certificate_id":"b"}],"next":null}` {
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
