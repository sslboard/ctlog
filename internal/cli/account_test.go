package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sslboard/ctlog/internal/api"
	"github.com/sslboard/ctlog/internal/config"
)

const testKey = "sslb_live_0123456789abcdefghijklmnopqrstuv"

// slept records the waits ctlog asked for instead of sleeping.
var slept []time.Duration

func init() {
	sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	pollUnit = time.Millisecond
	hostname = func() (string, error) { return "test-box", nil }
}

// fakeAccountAPI is a small in-memory SSLBoard: 25 hostnames, paged by
// limit/after for keys on the "pro" tier, and a device login that is approved
// after `pendingPolls` polls.
type fakeAccountAPI struct {
	mu           sync.Mutex
	tier         string
	pendingPolls int
	deny         bool
	slowDownOnce bool
	// emptyPage blanks the hostnames of that page number (1-based), keeping next.
	emptyPage int
	pages     int
	// dropFirstPoll closes the connection without answering the first poll.
	dropFirstPoll bool
	polls         int
	requests      []*http.Request
	// limited answers 429 for a domain: n > 0 times, or always when -1.
	limited map[string]int
}

func (f *fakeAccountAPI) names(domain string) []string {
	names := []string{domain}
	for i := 0; i < 24; i++ {
		names = append(names, fmt.Sprintf("a%02d.%s", i, domain))
	}
	return names
}

func (f *fakeAccountAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	authed := r.Header.Get("Authorization") == "Bearer "+testKey
	if r.Header.Get("Authorization") != "" && !authed {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"Invalid or revoked API key"}`)
		return
	}

	switch r.URL.Path {
	case "/api/v1/hostnames":
		q := r.URL.Query()
		domain := q.Get("q")
		if n := f.limited[domain]; n != 0 {
			if n > 0 {
				f.limited[domain] = n - 1
			}
			w.Header().Set("Retry-After", "60")
			w.Header().Set("X-Tier", "pro")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":"Rate limited: 60 requests per minute per API key"}`)
			return
		}
		names := f.names(domain)
		tier := "free"
		if authed {
			tier = f.tier
		}
		limit := 100
		if tier == "pro" {
			limit = 10
		}
		start := 0
		if after := q.Get("after"); after != "" {
			for i, n := range names {
				if n == after {
					start = i + 1
				}
			}
		}
		page := names[start:min(start+limit, len(names))]
		f.pages++
		visible := page
		if f.pages == f.emptyPage {
			// What --no-wildcards does to a page of nothing but wildcards.
			visible = []string{}
		}
		next := any(nil)
		if tier == "pro" && start+limit < len(names) {
			next = page[len(page)-1]
		}
		total := len(names)
		if tier == "free" {
			// Pretend the domain is bigger than the free page.
			total = 12432
		}
		upgrade := any("https://sslboard.com/docs/ctlog/#full-results")
		if tier == "pro" {
			upgrade = nil
		}
		json.NewEncoder(w).Encode(map[string]any{
			"query": domain, "domain": domain, "hostnames": visible, "returned": len(visible),
			"total": total, "total_is_lower_bound": false,
			"truncated": next != nil || total > len(names), "limit": limit, "tier": tier,
			"next": next, "map_url": "https://sslboard.com/subdomain/" + domain, "upgrade_url": upgrade,
		})
	case "/api/v1/me":
		if !authed {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"Missing API key"}`)
			return
		}
		perRequest, paging := 100, false
		if f.tier == "pro" {
			perRequest, paging = 10000, true
		}
		json.NewEncoder(w).Encode(map[string]any{
			"email":  "hunter@example.com",
			"plan":   map[string]any{"tier": f.tier, "status": "active", "current_period_end": "2026-11-20T00:00:00.000Z", "cancel_at_period_end": false},
			"limits": map[string]any{"hostnames_per_request": perRequest, "paging": paging, "requests": "60 per minute per key"},
			"key":    map[string]any{"id": "k1", "name": "ctlog on test-box", "prefix": "sslb_live_0123"},
			"usage":  map[string]any{"searches_today": 3, "searches_30d": 120, "hostnames_30d": 45210},
		})
	case "/api/v1/device/code":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_name"] != "test-box" {
			w.WriteHeader(400)
			return
		}
		io.WriteString(w, `{"device_code":"dev-secret","user_code":"BCDF-GHJK","verification_uri":"https://sslboard.com/device/","verification_uri_complete":"https://sslboard.com/device/?code=BCDF-GHJK","expires_in":600,"interval":5}`)
	case "/api/v1/device/token":
		if f.dropFirstPoll {
			f.dropFirstPoll = false
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["device_code"] != "dev-secret" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"Unknown device code"}`)
			return
		}
		f.polls++
		switch {
		case f.slowDownOnce && f.polls == 1:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"slow_down","error_description":"Polling too fast."}`)
		case f.polls <= f.pendingPolls:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"authorization_pending","error_description":"Waiting."}`)
		case f.deny:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"access_denied","error_description":"Denied."}`)
		default:
			fmt.Fprintf(w, `{"api_key":%q,"email":"hunter@example.com","plan":{"tier":%q}}`, testKey, f.tier)
		}
	default:
		w.WriteHeader(404)
	}
}

type harness struct {
	t       *testing.T
	api     *fakeAccountAPI
	srv     *httptest.Server
	vars    map[string]string
	cfgPath string
}

func newHarness(t *testing.T, tier string) *harness {
	t.Helper()
	f := &fakeAccountAPI{tier: tier, limited: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	return &harness{
		t:       t,
		api:     f,
		srv:     srv,
		vars:    map[string]string{"CTLOG_API_URL": srv.URL, "XDG_CONFIG_HOME": dir},
		cfgPath: filepath.Join(dir, "ctlog", "config.json"),
	}
}

func (h *harness) run(stdin io.Reader, args ...string) result {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(args, stdin, &stdout, &stderr, func(k string) string { return h.vars[k] }, "v0.0.0-test")
	return result{code, stdout.String(), stderr.String()}
}

func (h *harness) saveKey() {
	h.t.Helper()
	if err := config.Save(h.cfgPath, config.Config{APIKey: testKey}); err != nil {
		h.t.Fatal(err)
	}
}

func TestAllPagesThroughWithSavedKey(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()

	r := h.run(nil, "example.com", "--all")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != 25 || lines[0] != "example.com" || lines[24] != "a23.example.com" {
		t.Errorf("stdout has %d lines: %q", len(lines), r.stdout)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q", r.stderr)
	}
	if len(h.api.requests) != 3 {
		t.Errorf("requests = %d, want 3 pages", len(h.api.requests))
	}
	if after := h.api.requests[1].URL.Query().Get("after"); after != "a08.example.com" {
		t.Errorf("second page after = %q", after)
	}
}

func TestAllContinuesPastAnEmptyPage(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	h.api.emptyPage = 2
	r := h.run(nil, "example.com", "--all")
	if r.code != ExitOK || len(strings.Split(strings.TrimSpace(r.stdout), "\n")) != 15 {
		t.Errorf("code = %d, stdout = %q, stderr = %q", r.code, r.stdout, r.stderr)
	}
}

func TestAllJSONReportsTheMergedResult(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	r := h.run(nil, "example.com", "--all", "-o", "json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	if got["returned"] != float64(25) || got["next"] != nil || got["truncated"] != false || len(got["hostnames"].([]any)) != 25 {
		t.Errorf("json = %v", got)
	}
}

func TestPaidPageWithoutAllSuggestsIt(t *testing.T) {
	h := newHarness(t, "pro")
	h.vars[config.EnvAPIKey] = testKey
	r := h.run(nil, "example.com")
	if r.code != ExitOK || r.stderr != "10 of 25 hostnames. Add --all to fetch the rest.\n" {
		t.Errorf("result = %+v", r)
	}
	if got := h.api.requests[0].Header.Get("Authorization"); got != "Bearer "+testKey {
		t.Errorf("Authorization = %q", got)
	}
}

func TestFreeNoticePointsToLogin(t *testing.T) {
	h := newHarness(t, "pro")
	r := h.run(nil, "example.com", "--all")
	want := "--all needs the hostname API plan, so this is the free first page.\n" +
		"25 of 12,432 hostnames (free limit). Run 'ctlog login' to get all of them: https://sslboard.com/docs/ctlog/#full-results\n"
	if r.code != ExitOK || r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
	if h.api.requests[0].Header.Get("Authorization") != "" {
		t.Error("anonymous request sent an Authorization header")
	}
}

func TestKeyWithoutPlanNotice(t *testing.T) {
	h := newHarness(t, "free")
	h.saveKey()
	r := h.run(nil, "example.com")
	want := "25 of 12,432 hostnames (free limit). Your account has no active plan. Subscribe to get all of them: " + h.srv.URL + "/account/api/\n"
	if r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
}

func TestRejectedKeyExitsFour(t *testing.T) {
	h := newHarness(t, "pro")
	h.vars[config.EnvAPIKey] = "sslb_live_revoked"
	r := h.run(nil, "example.com")
	if r.code != ExitAuth || r.stderr != "ctlog: API key rejected (revoked or invalid). Check the key in SSLBOARD_API_KEY.\n" {
		t.Errorf("result = %+v", r)
	}
}

func TestPaidRateLimitUsesServerMessage(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	h.api.limited["example.com"] = -1
	r := h.run(nil, "example.com")
	if r.code != ExitRateLimited || r.stderr != "Rate limited: 60 requests per minute per API key. Retry in 60s.\n" {
		t.Errorf("result = %+v", r)
	}
}

func TestBatchMergesAndDedupes(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	file := filepath.Join(t.TempDir(), "domains.txt")
	os.WriteFile(file, []byte("# targets\nexample.com\n\na00.example.com\nEXAMPLE.com\nexample.org\n"), 0o600)

	r := h.run(nil, "-f", file, "--all", "-q")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	// example.com (25) + a00.example.com's 25 names, all new but its apex + example.org (25).
	if len(lines) != 25+24+25 {
		t.Errorf("got %d lines", len(lines))
	}

	r = h.run(strings.NewReader("example.com\nexample.org\n"), "-f", "-", "-o", "json")
	var got struct {
		Domains []struct {
			Domain    string `json:"domain"`
			Returned  int    `json:"returned"`
			Truncated bool   `json:"truncated"`
		} `json:"domains"`
		Hostnames []string `json:"hostnames"`
		Returned  int      `json:"returned"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	if len(got.Domains) != 2 || got.Domains[0].Returned != 10 || !got.Domains[0].Truncated || got.Returned != 20 {
		t.Errorf("json = %+v", got)
	}
	if !strings.Contains(r.stderr, "example.com: 10 of 25 hostnames. Add --all to fetch the rest.") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestBatchStopsOnRateLimitWithPartialOutput(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	h.api.limited["example.org"] = -1
	r := h.run(strings.NewReader("example.com\nexample.org\nexample.net\n"), "-f", "-")
	if r.code != ExitRateLimited {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	if n := len(strings.Split(strings.TrimSpace(r.stdout), "\n")); n != 10 {
		t.Errorf("stdout lines = %d", n)
	}
	if !strings.Contains(r.stderr, "stopped at example.org (1 of 3 domains done)") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestPaidRunsWaitOutTheRateLimit(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	h.api.limited["example.org"] = 2
	slept = nil

	r := h.run(strings.NewReader("example.com\nexample.org\n"), "-f", "-", "--all")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	if n := len(strings.Split(strings.TrimSpace(r.stdout), "\n")); n != 50 {
		t.Errorf("stdout lines = %d, want 50", n)
	}
	if len(slept) != 2 || slept[0] != 60*time.Second {
		t.Errorf("slept = %v, want two 60s waits", slept)
	}
	if strings.Count(r.stderr, "Rate limit reached, waiting 60s...") != 2 {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestPacingGivesUpAfterRepeatedLimits(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()
	h.api.limited["example.com"] = -1
	slept = nil
	r := h.run(nil, "example.com", "--all", "-q")
	if r.code != ExitRateLimited || len(slept) != maxRateLimitWaits {
		t.Errorf("code = %d, waits = %d, stderr = %q", r.code, len(slept), r.stderr)
	}
}

func TestBatchRejectsBadInput(t *testing.T) {
	h := newHarness(t, "pro")
	cases := []struct {
		stdin string
		args  []string
		want  string
	}{
		{"example.com\ncom\n", []string{"-f", "-"}, "- line 2:"},
		{"# nothing\n", []string{"-f", "-"}, "no domains in -"},
		{"", []string{"example.com", "-f", "-"}, "either a domain or -f"},
		{"", []string{"-f", "/does/not/exist"}, "no such file"},
	}
	for _, c := range cases {
		r := h.run(strings.NewReader(c.stdin), c.args...)
		if r.code != ExitUsage || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: result = %+v, want %q", c.args, r, c.want)
		}
	}
	if len(h.api.requests) != 0 {
		t.Errorf("bad input still sent %d requests", len(h.api.requests))
	}
}

func TestDeviceLogin(t *testing.T) {
	h := newHarness(t, "free")
	h.api.pendingPolls = 2
	h.api.slowDownOnce = true
	var opened string
	openBrowser = func(url string) error { opened = url; return nil }
	t.Cleanup(func() { openBrowser = func(string) error { return nil } })

	r := h.run(nil, "login")
	if r.code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	if opened != "https://sslboard.com/device/?code=BCDF-GHJK" {
		t.Errorf("opened %q", opened)
	}
	for _, want := range []string{
		"To sign in, open https://sslboard.com/device/?code=BCDF-GHJK\nand check that the page shows the code BCDF-GHJK.",
		"Signed in as hunter@example.com.",
		"Free plan: 100 hostnames per search.",
	} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, r.stderr)
		}
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q", r.stdout)
	}
	cfg, err := config.Load(h.cfgPath)
	if err != nil || cfg.APIKey != testKey || cfg.Email != "hunter@example.com" {
		t.Errorf("config = %+v, %v", cfg, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(h.cfgPath); info.Mode().Perm() != 0o600 {
			t.Errorf("config mode = %o", info.Mode().Perm())
		}
	}
	if h.api.polls != 3 {
		t.Errorf("polls = %d, want 3 (slow_down, pending, approved)", h.api.polls)
	}

	status := h.run(nil, "status")
	for _, want := range []string{
		"Account: hunter@example.com",
		"Plan:    free (subscription active)",
		"Key:     ctlog on test-box (sslb_live_0123...), saved by ctlog login",
		"Usage:   3 searches today, 120 in the last 30 days (45,210 hostnames)",
	} {
		if !strings.Contains(status.stdout, want) {
			t.Errorf("status missing %q:\n%s", want, status.stdout)
		}
	}
}

func TestDeviceLoginSurvivesADroppedConnection(t *testing.T) {
	h := newHarness(t, "pro")
	h.api.dropFirstPoll = true
	r := h.run(nil, "login", "--no-browser")
	if r.code != ExitOK || !strings.Contains(r.stderr, "Signed in as hunter@example.com.") {
		t.Errorf("result = %+v", r)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	h := newHarness(t, "free")
	h.api.deny = true
	r := h.run(nil, "login", "--no-browser")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "denied in the browser") {
		t.Errorf("result = %+v", r)
	}
	if _, err := os.Stat(h.cfgPath); err == nil {
		t.Error("a denied login saved a config file")
	}
}

func TestLoginWithKey(t *testing.T) {
	h := newHarness(t, "pro")
	r := h.run(strings.NewReader(testKey+"\n"), "login", "--key", "-")
	if r.code != ExitOK || !strings.Contains(r.stderr, "Hostname API plan active") {
		t.Fatalf("result = %+v", r)
	}
	if cfg, _ := config.Load(h.cfgPath); cfg.APIKey != testKey {
		t.Errorf("config = %+v", cfg)
	}

	r = h.run(nil, "login", "--key", "sslb_live_wrong")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "that key was rejected") {
		t.Errorf("rejected key: %+v", r)
	}
	r = h.run(nil, "login", "--key", "not-a-key")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "API keys start with sslb_live_") {
		t.Errorf("bad prefix: %+v", r)
	}
}

func TestLogoutAndStatus(t *testing.T) {
	h := newHarness(t, "pro")
	h.saveKey()

	status := h.run(nil, "status")
	if !strings.Contains(status.stdout, "Plan:    hostname API (renews 2026-11-20)") ||
		!strings.Contains(status.stdout, "Limits:  10,000 hostnames per request, --all pages through the rest, 60 per minute per key") {
		t.Errorf("status = %q", status.stdout)
	}
	js := h.run(nil, "status", "-o", "json")
	if !strings.HasPrefix(js.stdout, `{"email":"hunter@example.com"`) {
		t.Errorf("status json = %q", js.stdout)
	}

	r := h.run(nil, "logout")
	if r.code != ExitOK || !strings.HasPrefix(r.stderr, "Signed out.") {
		t.Errorf("logout = %+v", r)
	}
	if _, err := os.Stat(h.cfgPath); err == nil {
		t.Error("config file still exists")
	}
	if r := h.run(nil, "logout"); r.stderr != "Not signed in.\n" {
		t.Errorf("second logout = %+v", r)
	}

	r = h.run(nil, "status")
	if r.code != ExitOK || !strings.HasPrefix(r.stdout, "Not signed in.") {
		t.Errorf("status after logout = %+v", r)
	}
	if len(h.api.requests) != 2 {
		t.Errorf("requests = %d, want 2 (status after logout is offline)", len(h.api.requests))
	}
}

func TestLoginFlagsOutsideLogin(t *testing.T) {
	h := newHarness(t, "pro")
	for _, args := range [][]string{{"example.com", "--key", testKey}, {"status", "--no-browser"}, {"status", "extra"}} {
		if r := h.run(nil, args...); r.code != ExitUsage {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

func TestDescribePlan(t *testing.T) {
	cases := []struct {
		tier, status, end string
		cancel            bool
		want              string
	}{
		{"pro", "active", "2026-11-20T00:00:00Z", false, "hostname API (renews 2026-11-20)"},
		{"pro", "active", "2026-11-20T00:00:00Z", true, "hostname API (cancelled, full results until 2026-11-20)"},
		{"pro", "past_due", "", false, "hostname API (payment failed, Stripe is retrying the card)"},
		{"free", "canceled", "", false, "free (subscription canceled)"},
		{"free", "", "", false, "free"},
	}
	for _, c := range cases {
		got := describePlan(planOf(c.tier, c.status, c.end, c.cancel))
		if got != c.want {
			t.Errorf("describePlan(%v) = %q, want %q", c, got, c.want)
		}
	}
}

func planOf(tier, status, end string, cancel bool) (p api.Plan) {
	p.Tier, p.Status, p.CurrentPeriodEnd, p.CancelAtPeriodEnd = tier, status, end, cancel
	return p
}
