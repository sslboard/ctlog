package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sslboard/ctlog/internal/api"
	"github.com/sslboard/ctlog/internal/query"
)

// A paid-plan rate limit is waited out at most maxRateLimitWaits times in a
// row, maxRateLimitWait each, before ctlog gives up.
const (
	maxRateLimitWaits = 5
	maxRateLimitWait  = 60 * time.Second
)

// sleep waits for d or until ctx is done. Tests replace it.
var sleep = func(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// maxBatchDomains bounds -f input, so a wrong file (a hostname list instead
// of a domain list) fails fast instead of running for hours.
const maxBatchDomains = 10_000

func runLookup(ctx context.Context, e *env, opts options, positional []string) int {
	if opts.file != "" {
		if len(positional) > 0 {
			return usageError(e.stderr, "use either a domain or -f, not both")
		}
		domains, code := readDomainFile(e, opts.file)
		if code != ExitOK {
			return code
		}
		return runBatch(ctx, e, opts, domains)
	}

	switch len(positional) {
	case 0:
		return usageError(e.stderr, "missing domain")
	case 1:
	default:
		return usageError(e.stderr, "one domain per invocation (got "+strconv.Itoa(len(positional))+"); use -f for a list")
	}

	domain, err := query.Normalize(positional[0])
	if err != nil {
		fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
		return ExitUsage
	}

	res, err := e.fetch(ctx, domain, opts, opts.all)
	if res != nil {
		convertHostnames(res.Hostnames, opts.unicode)
		if werr := writeResult(e.stdout, opts.output, res); werr != nil {
			fmt.Fprintf(e.stderr, "ctlog: writing output: %v\n", werr)
			return ExitAPI
		}
	}
	if err != nil {
		if res != nil {
			fmt.Fprintf(e.stderr, "ctlog: stopped after %s hostnames.\n", formatCount(int64(len(res.Hostnames))))
		}
		return e.reportError(err)
	}
	if !opts.quiet {
		e.writeNotice("", domain, res, opts.all)
	}
	return ExitOK
}

// fetch looks domain up and, with --all, follows Next until the last page.
// On a failure after the first page it returns what it has with the error.
// pace waits out paid-plan rate limits instead of failing (--all and -f).
func (e *env) fetch(ctx context.Context, domain string, opts options, pace bool) (*api.Result, error) {
	lookup := api.Options{IncludeApex: !opts.noApex, ExcludeWildcards: opts.noWildcards}
	res, err := e.lookup(ctx, domain, lookup, pace, opts.quiet)
	if err != nil {
		return nil, err
	}
	if !opts.all || res.Next == "" {
		return res, nil
	}
	var failure error
	for res.Next != "" {
		lookup.After = res.Next
		page, err := e.lookup(ctx, domain, lookup, pace, opts.quiet)
		if err != nil {
			failure = err
			break
		}
		// A server that hands back the same cursor would loop forever. An
		// empty page is fine: --no-wildcards can filter a whole page out.
		if page.Next == lookup.After {
			page.Next = ""
		}
		res.Hostnames = append(res.Hostnames, page.Hostnames...)
		res.Next = page.Next
		res.Truncated = page.Truncated
		res.Total = page.Total
		res.TotalIsLowerBound = page.TotalIsLowerBound
	}
	res.Returned = len(res.Hostnames)
	raw, err := replaceMembers(res.Raw, map[string]any{
		"returned":             res.Returned,
		"next":                 nilIfEmpty(res.Next),
		"truncated":            res.Truncated,
		"total":                res.Total,
		"total_is_lower_bound": res.TotalIsLowerBound,
	})
	if err != nil {
		return nil, err
	}
	res.Raw = raw
	return res, failure
}

// lookup calls the API. With pace, a paid-plan 429 is waited out (its
// Retry-After, capped) and retried, so long --all and -f runs finish on their
// own at the plan's rate. Free-tier 429s and single lookups never wait.
func (e *env) lookup(ctx context.Context, domain string, opts api.Options, pace, quiet bool) (*api.Result, error) {
	for waits := 0; ; waits++ {
		res, err := e.client.Lookup(ctx, domain, opts)
		var apiErr *api.Error
		if !pace || waits >= maxRateLimitWaits || !errors.As(err, &apiErr) ||
			apiErr.Status != http.StatusTooManyRequests || apiErr.Tier != "pro" {
			return res, err
		}
		delay := apiErr.RetryAfter
		if delay <= 0 || delay > maxRateLimitWait {
			delay = maxRateLimitWait
		}
		if !quiet {
			fmt.Fprintf(e.stderr, "Rate limit reached, waiting %ds...\n", int(delay.Seconds()))
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func convertHostnames(hostnames []string, unicode bool) {
	convert := query.ASCII
	if unicode {
		convert = query.Unicode
	}
	for i, h := range hostnames {
		hostnames[i] = convert(h)
	}
}

func readDomainFile(e *env, path string) ([]string, int) {
	var r io.Reader
	if path == "-" {
		if e.stdin == nil {
			return nil, usageError(e.stderr, "no stdin to read domains from")
		}
		r = e.stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
			return nil, ExitUsage
		}
		defer f.Close()
		r = f
	}

	var domains []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(r)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		domain, err := query.Normalize(text)
		if err != nil {
			fmt.Fprintf(e.stderr, "ctlog: %s line %d: %v\n", path, line, err)
			return nil, ExitUsage
		}
		if seen[domain] {
			continue
		}
		seen[domain] = true
		domains = append(domains, domain)
		if len(domains) > maxBatchDomains {
			fmt.Fprintf(e.stderr, "ctlog: %s has more than %s domains\n", path, formatCount(maxBatchDomains))
			return nil, ExitUsage
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(e.stderr, "ctlog: reading %s: %v\n", path, err)
		return nil, ExitUsage
	}
	if len(domains) == 0 {
		return nil, usageError(e.stderr, "no domains in "+path)
	}
	return domains, ExitOK
}

// batchDomain is one entry of the -o json "domains" list in batch mode.
type batchDomain struct {
	Domain            string `json:"domain"`
	Returned          int    `json:"returned"`
	Total             int64  `json:"total"`
	TotalIsLowerBound bool   `json:"total_is_lower_bound"`
	Truncated         bool   `json:"truncated"`
}

func runBatch(ctx context.Context, e *env, opts options, domains []string) int {
	merged := []string{}
	seen := map[string]bool{}
	summaries := []batchDomain{}
	var failure error

	for i, domain := range domains {
		res, err := e.fetch(ctx, domain, opts, true)
		if res != nil {
			convertHostnames(res.Hostnames, opts.unicode)
			for _, h := range res.Hostnames {
				if !seen[h] {
					seen[h] = true
					merged = append(merged, h)
				}
			}
			summaries = append(summaries, batchDomain{
				Domain:            domain,
				Returned:          len(res.Hostnames),
				Total:             res.Total,
				TotalIsLowerBound: res.TotalIsLowerBound,
				Truncated:         res.Truncated,
			})
			if err == nil && !opts.quiet {
				e.writeNotice(domain+": ", domain, res, opts.all && i == 0)
			}
		}
		if err != nil {
			failure = fmt.Errorf("%s: %w", domain, err)
			if !opts.quiet {
				fmt.Fprintf(e.stderr, "ctlog: stopped at %s (%d of %d domains done).\n", domain, i, len(domains))
			}
			break
		}
	}

	if err := writeBatch(e.stdout, opts.output, summaries, merged); err != nil {
		fmt.Fprintf(e.stderr, "ctlog: writing output: %v\n", err)
		return ExitAPI
	}
	if failure != nil {
		return e.reportError(failure)
	}
	return ExitOK
}

func writeBatch(w io.Writer, format string, summaries []batchDomain, hostnames []string) error {
	if format != "json" {
		return writeResult(w, format, &api.Result{Hostnames: hostnames})
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(struct {
		Domains   []batchDomain `json:"domains"`
		Hostnames []string      `json:"hostnames"`
		Returned  int           `json:"returned"`
	}{summaries, hostnames, len(hostnames)}); err != nil {
		return err
	}
	return bw.Flush()
}

// crtshEntry mirrors the crt.sh JSON shape. Only name_value and common_name
// are populated: SSLBoard serves hostnames, not certificates.
type crtshEntry struct {
	NameValue  string  `json:"name_value"`
	CommonName string  `json:"common_name"`
	IssuerName *string `json:"issuer_name"`
	NotBefore  *string `json:"not_before"`
	NotAfter   *string `json:"not_after"`
	ID         *int64  `json:"id"`
}

func writeResult(w io.Writer, format string, res *api.Result) error {
	bw := bufio.NewWriter(w)
	switch format {
	case "json":
		raw, err := replaceMembers(res.Raw, map[string]any{"hostnames": res.Hostnames})
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		if _, err := bw.Write(buf.Bytes()); err != nil {
			return err
		}
	case "crtsh":
		entries := make([]crtshEntry, 0, len(res.Hostnames))
		for _, h := range res.Hostnames {
			entries = append(entries, crtshEntry{NameValue: h, CommonName: h})
		}
		enc := json.NewEncoder(bw)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			return err
		}
	default:
		for _, h := range res.Hostnames {
			bw.WriteString(h)
			bw.WriteByte('\n')
		}
	}
	return bw.Flush()
}

// replaceMembers returns the JSON object raw with the given members set.
// Other members, and the member order, are kept as received.
func replaceMembers(raw []byte, members map[string]any) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("unexpected response from API (not a JSON object)")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if replacement, ok := members[key]; ok {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(replacement); err != nil {
				return nil, err
			}
			value = bytes.TrimRight(buf.Bytes(), "\n")
		}
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		k, _ := json.Marshal(key)
		out.Write(k)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// writeNotice explains a partial result on stderr. prefix labels the domain
// in batch mode. allOnFree adds the one-off "--all needs a plan" hint.
func (e *env) writeNotice(prefix, domain string, res *api.Result, allOnFree bool) {
	returned := res.Returned
	if returned == 0 {
		returned = len(res.Hostnames)
	}
	if returned == 0 && !res.Truncated {
		fmt.Fprintf(e.stderr, "%sNo hostnames found for %s.\n", prefix, domain)
		return
	}
	if !res.Truncated {
		return
	}
	total := formatCount(res.Total)
	if res.TotalIsLowerBound {
		total += "+"
	}
	counts := fmt.Sprintf("%s of %s hostnames", formatCount(int64(returned)), total)

	if res.Tier == "pro" {
		if res.Next != "" {
			fmt.Fprintf(e.stderr, "%s%s. Add --all to fetch the rest.\n", prefix, counts)
		}
		return
	}

	if allOnFree && res.Tier == "free" {
		fmt.Fprintln(e.stderr, "--all needs the hostname API plan, so this is the free first page.")
	}
	msg := prefix + counts + " (free limit)."
	switch {
	case e.client.APIKey != "" && res.Tier == "free":
		msg += " Your account has no active plan. Subscribe to get all of them: " + e.accountURL()
	case res.UpgradeURL != "":
		msg += " Run 'ctlog login' to get all of them: " + res.UpgradeURL
	case res.MapURL != "":
		msg += " See how they're spread across subdomains: " + res.MapURL
	}
	fmt.Fprintln(e.stderr, msg)
}
