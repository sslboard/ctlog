package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/sslboard/ctlog/internal/api"
	"github.com/sslboard/ctlog/internal/query"
)

func runCertificates(ctx context.Context, e *env, opts options, positional []string) int {
	if len(positional) != 1 || opts.file != "" || opts.noApex || opts.noWildcards || opts.unicode || opts.output == "crtsh" {
		return usageError(e.stderr, "certificates needs one domain and supports JSON output, --exact and --all")
	}
	domain, err := query.Normalize(positional[0])
	if err != nil {
		return usageError(e.stderr, err.Error())
	}
	after := ""
	seen := map[string]bool{}
	records := []json.RawMessage{}
	ids := map[string]bool{}
	var result map[string]json.RawMessage
	for {
		result, err = e.certificatePage(ctx, domain, after, opts)
		if err != nil {
			return e.reportError(err)
		}
		var page []json.RawMessage
		if err = json.Unmarshal(result["certificates"], &page); err != nil {
			return e.reportError(err)
		}
		for _, raw := range page {
			var cert struct {
				ID string `json:"certificate_id"`
			}
			if err = json.Unmarshal(raw, &cert); err != nil {
				return e.reportError(err)
			}
			if cert.ID == "" {
				return e.reportError(fmt.Errorf("certificate missing ID"))
			}
			if !ids[cert.ID] {
				records = append(records, raw)
				ids[cert.ID] = true
			}
		}
		var next string
		if raw := result["next"]; len(raw) > 0 && string(raw) != "null" {
			if err = json.Unmarshal(raw, &next); err != nil {
				return e.reportError(err)
			}
		}
		if !opts.all || next == "" {
			break
		}
		if seen[next] {
			return e.reportError(fmt.Errorf("API repeated a certificate cursor"))
		}
		seen[next] = true
		after = next
	}
	result["certificates"], _ = json.Marshal(records)
	if err = json.NewEncoder(e.stdout).Encode(result); err != nil {
		return e.reportError(err)
	}
	if !opts.quiet && string(result["truncated"]) == "true" {
		fmt.Fprintln(e.stderr, "More recent certificates may be available. Use --all with an active API plan.")
	}
	return ExitOK
}

func (e *env) certificatePage(ctx context.Context, domain, after string, opts options) (map[string]json.RawMessage, error) {
	for waits := 0; ; waits++ {
		res, err := e.client.Certificates(ctx, domain, after, opts.exact)
		var apiErr *api.Error
		if !opts.all || waits >= maxRateLimitWaits || !errors.As(err, &apiErr) ||
			apiErr.Status != http.StatusTooManyRequests || apiErr.Tier != "pro" {
			return res, err
		}
		delay := apiErr.RetryAfter
		if delay <= 0 || delay > maxRateLimitWait {
			delay = maxRateLimitWait
		}
		if !opts.quiet {
			fmt.Fprintf(e.stderr, "Rate limit reached, waiting %ds...\n", int(delay.Seconds()))
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}
