package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func init() { retryDelay = time.Millisecond }

const truncatedBody = `{"query":"example.com","domain":"example.com","hostnames":["example.com","api.example.com"],"returned":2,"total":12432,"total_is_lower_bound":false,"truncated":true,"limit":100,"map_url":"https://sslboard.com/subdomain/example.com"}`

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, apiURL string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := func(k string) string {
		if k == "CTLOG_API_URL" {
			return apiURL
		}
		return ""
	}
	code := Run(args, nil, &stdout, &stderr, env, "v0.0.0-test")
	return result{code, stdout.String(), stderr.String()}
}

// fakeAPI serves body with status and records the last request.
func fakeAPI(t *testing.T, status int, body string, headers map[string]string) (*httptest.Server, *atomic.Pointer[http.Request], *atomic.Int32) {
	t.Helper()
	var last atomic.Pointer[http.Request]
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		last.Store(r)
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &last, &calls
}

func TestTextOutputAndTruncationNotice(t *testing.T) {
	srv, last, _ := fakeAPI(t, 200, truncatedBody, nil)
	r := run(t, srv.URL, "%.Example.com")

	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	if r.stdout != "example.com\napi.example.com\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	wantNotice := "2 of 12,432 hostnames (free limit). See how they're spread across subdomains: https://sslboard.com/subdomain/example.com\n"
	if r.stderr != wantNotice {
		t.Errorf("stderr = %q, want %q", r.stderr, wantNotice)
	}

	req := last.Load()
	q := req.URL.Query()
	if q.Get("q") != "example.com" || q.Get("include_apex") != "true" || q.Get("wildcards") != "include" {
		t.Errorf("query = %v", q)
	}
	if ua := req.Header.Get("User-Agent"); ua != "ctlog/v0.0.0-test (+https://github.com/sslboard/ctlog)" {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestFlagsAfterDomainAndFindAlias(t *testing.T) {
	srv, last, _ := fakeAPI(t, 200, truncatedBody, nil)
	r := run(t, srv.URL, "find", "*.example.com", "--no-apex", "--no-wildcards", "--quiet")
	if r.code != ExitOK || r.stderr != "" {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	q := last.Load().URL.Query()
	if q.Get("include_apex") != "false" || q.Get("wildcards") != "exclude" {
		t.Errorf("query = %v", q)
	}
}

func TestJSONOutputPassesThroughResponse(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, truncatedBody, nil)
	r := run(t, srv.URL, "example.com", "-o", "json")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, r.stdout)
	}
	json.Unmarshal([]byte(truncatedBody), &want)
	if got["total"] != want["total"] || got["map_url"] != want["map_url"] {
		t.Errorf("stdout = %s", r.stdout)
	}
}

func TestCrtshOutputShape(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, truncatedBody, nil)
	r := run(t, srv.URL, "--output=crtsh", "example.com")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &entries); err != nil {
		t.Fatalf("stdout is not a JSON array: %v", err)
	}
	if len(entries) != 2 || entries[1]["name_value"] != "api.example.com" || entries[1]["common_name"] != "api.example.com" {
		t.Errorf("entries = %v", entries)
	}
	for _, k := range []string{"issuer_name", "not_before", "not_after", "id"} {
		if v, ok := entries[0][k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
}

// idnBody mixes the Unicode and punycode forms the API may return.
const idnBody = `{"query":"xn--mnchen-3ya.de","domain":"xn--mnchen-3ya.de","hostnames":["münchen.de","*.xn--mnchen-3ya.de","www.münchen.de"],"returned":3,"total":3,"total_is_lower_bound":false,"truncated":false,"limit":100,"map_url":"https://sslboard.com/subdomain/xn--mnchen-3ya.de"}`

func TestUnicodeQuerySentAsPunycode(t *testing.T) {
	srv, last, _ := fakeAPI(t, 200, idnBody, nil)
	r := run(t, srv.URL, "%.München.de")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	if q := last.Load().URL.Query().Get("q"); q != "xn--mnchen-3ya.de" {
		t.Errorf("q = %q", q)
	}
}

func TestIDNOutputIsPunycodeByDefault(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, idnBody, nil)

	r := run(t, srv.URL, "münchen.de")
	if want := "xn--mnchen-3ya.de\n*.xn--mnchen-3ya.de\nwww.xn--mnchen-3ya.de\n"; r.stdout != want {
		t.Errorf("text stdout = %q, want %q", r.stdout, want)
	}

	r = run(t, srv.URL, "münchen.de", "-o", "crtsh")
	var entries []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &entries); err != nil {
		t.Fatalf("crtsh stdout is not JSON: %v", err)
	}
	if entries[2]["name_value"] != "www.xn--mnchen-3ya.de" || entries[2]["common_name"] != "www.xn--mnchen-3ya.de" {
		t.Errorf("crtsh entries = %v", entries)
	}

	r = run(t, srv.URL, "münchen.de", "-o", "json")
	var got struct {
		Hostnames []string `json:"hostnames"`
		MapURL    string   `json:"map_url"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("json stdout is not JSON: %v", err)
	}
	if strings.Join(got.Hostnames, " ") != "xn--mnchen-3ya.de *.xn--mnchen-3ya.de www.xn--mnchen-3ya.de" || got.MapURL == "" {
		t.Errorf("json stdout = %s", r.stdout)
	}
	if !strings.HasPrefix(r.stdout, "{\n  \"query\"") {
		t.Errorf("json member order changed: %s", r.stdout)
	}
}

func TestUnicodeFlag(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, idnBody, nil)
	r := run(t, srv.URL, "xn--mnchen-3ya.de", "--unicode")
	if want := "münchen.de\n*.münchen.de\nwww.münchen.de\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}

func TestLowerBoundNotice(t *testing.T) {
	body := `{"domain":"example.com","hostnames":["a.example.com"],"returned":100,"total":500000,"total_is_lower_bound":true,"truncated":true,"map_url":"https://sslboard.com/subdomain/example.com"}`
	srv, _, _ := fakeAPI(t, 200, body, nil)
	r := run(t, srv.URL, "example.com")
	if !strings.HasPrefix(r.stderr, "100 of 500,000+ hostnames (free limit).") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestNoResults(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, `{"domain":"example.com","hostnames":[],"returned":0,"total":0,"truncated":false}`, nil)
	r := run(t, srv.URL, "example.com")
	if r.code != ExitOK || r.stdout != "" || r.stderr != "No hostnames found for example.com.\n" {
		t.Errorf("result = %+v", r)
	}
	r = run(t, srv.URL, "example.com", "-q")
	if r.code != ExitOK || r.stderr != "" {
		t.Errorf("quiet result = %+v", r)
	}
}

func TestNotTruncatedHasNoNotice(t *testing.T) {
	srv, _, _ := fakeAPI(t, 200, `{"domain":"example.com","hostnames":["example.com"],"returned":1,"total":1,"truncated":false}`, nil)
	r := run(t, srv.URL, "example.com")
	if r.code != ExitOK || r.stdout != "example.com\n" || r.stderr != "" {
		t.Errorf("result = %+v", r)
	}
}

func TestRateLimited(t *testing.T) {
	srv, _, calls := fakeAPI(t, 429, `{"error":"Rate limited: 1 search per minute without an account"}`, map[string]string{"Retry-After": "42"})
	r := run(t, srv.URL, "example.com")
	if r.code != ExitRateLimited {
		t.Fatalf("code = %d", r.code)
	}
	if r.stderr != "Rate limited (1 search/minute without an account). Retry in 42s.\n" {
		t.Errorf("stderr = %q", r.stderr)
	}
	if r.stdout != "" || calls.Load() != 1 {
		t.Errorf("stdout = %q, calls = %d", r.stdout, calls.Load())
	}
}

func TestRateLimitedWithoutRetryAfter(t *testing.T) {
	srv, _, _ := fakeAPI(t, 429, ``, nil)
	r := run(t, srv.URL, "example.com")
	if r.code != ExitRateLimited || !strings.Contains(r.stderr, "Retry in about a minute.") {
		t.Errorf("result = %+v", r)
	}
}

func TestServerValidationErrorExitsOne(t *testing.T) {
	srv, _, _ := fakeAPI(t, 422, `{"error":"github.io is not available for hostname lookups"}`, nil)
	r := run(t, srv.URL, "github.io")
	if r.code != ExitUsage || r.stderr != "ctlog: github.io is not available for hostname lookups\n" {
		t.Errorf("result = %+v", r)
	}
}

func TestUpstreamErrorExitsTwoAfterRetry(t *testing.T) {
	srv, _, calls := fakeAPI(t, 503, `{"error":"ct-storage unavailable"}`, nil)
	r := run(t, srv.URL, "example.com")
	if r.code != ExitAPI || calls.Load() != 2 {
		t.Fatalf("code = %d, calls = %d", r.code, calls.Load())
	}
	if r.stderr != "ctlog: API error (HTTP 503): ct-storage unavailable\n" {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestNetworkErrorExitsTwo(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(srv.URL)
	srv.Close()
	r := run(t, "http://"+u.Host, "example.com")
	if r.code != ExitAPI || r.stderr == "" {
		t.Errorf("result = %+v", r)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "missing domain"},
		{[]string{"find"}, "missing domain"},
		{[]string{"a.com", "b.com"}, "one domain per invocation"},
		{[]string{"example.com", "-o", "xml"}, "unknown output format"},
		{[]string{"example.com", "--bogus"}, "flag provided but not defined"},
		{[]string{"com"}, "public suffix"},
		{[]string{"ex%ample.com"}, "wildcards are only allowed"},
		{[]string{"version", "extra"}, "no arguments"},
	}
	for _, c := range cases {
		// No server: these must fail before any request.
		r := run(t, "http://127.0.0.1:1", c.args...)
		if r.code != ExitUsage || !strings.Contains(r.stderr, c.want) || r.stdout != "" {
			t.Errorf("args %v: result = %+v, want stderr containing %q", c.args, r, c.want)
		}
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}} {
		r := run(t, "", args...)
		if r.code != ExitOK || !strings.HasPrefix(r.stdout, "ctlog: list hostnames") {
			t.Errorf("args %v: result = %+v", args, r)
		}
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		r := run(t, "", args...)
		if r.code != ExitOK || !strings.HasPrefix(r.stdout, "ctlog v0.0.0-test (") {
			t.Errorf("args %v: result = %+v", args, r)
		}
	}
}

func TestDoubleDashTerminatesFlags(t *testing.T) {
	srv, last, _ := fakeAPI(t, 200, truncatedBody, nil)
	r := run(t, srv.URL, "-q", "--", "example.com")
	if r.code != ExitOK || last.Load().URL.Query().Get("q") != "example.com" {
		t.Errorf("result = %+v", r)
	}
}

func TestFormatCount(t *testing.T) {
	cases := map[int64]string{0: "0", 7: "7", 100: "100", 1000: "1,000", 12432: "12,432", 500000: "500,000", 1234567: "1,234,567"}
	for n, want := range cases {
		if got := formatCount(n); got != want {
			t.Errorf("formatCount(%d) = %q, want %q", n, got, want)
		}
	}
}
