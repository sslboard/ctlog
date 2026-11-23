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
	result, err := e.fetchCertificates(ctx, domain, opts)
	if err != nil {
		return e.reportError(err)
	}
	if err = json.NewEncoder(e.stdout).Encode(result); err != nil {
		return e.reportError(err)
	}
	if !opts.quiet && string(result["truncated"]) == "true" {
		fmt.Fprintln(e.stderr, "More recent certificates may be available. Use --all with an active API plan.")
	}
	return ExitOK
}

func (e *env) fetchCertificates(ctx context.Context, domain string, opts options) (map[string]json.RawMessage, error) {
	after := ""
	seen := map[string]bool{}
	records := []json.RawMessage{}
	ids := map[string]bool{}
	for {
		result, err := e.certificatePage(ctx, domain, after, opts)
		if err != nil {
			return nil, err
		}
		page, next, err := decodeCertificatePage(result, ids)
		if err != nil {
			return nil, err
		}
		records = append(records, page...)
		if !opts.all || next == "" {
			result["certificates"], err = json.Marshal(records)
			return result, err
		}
		if seen[next] {
			return nil, fmt.Errorf("API repeated a certificate cursor")
		}
		seen[next] = true
		after = next
	}
}

// decodeCertificatePage validates records and keeps the first occurrence of each ID across pages.
func decodeCertificatePage(result map[string]json.RawMessage, ids map[string]bool) ([]json.RawMessage, string, error) {
	var page []json.RawMessage
	if err := json.Unmarshal(result["certificates"], &page); err != nil {
		return nil, "", err
	}
	records := []json.RawMessage{}
	for _, raw := range page {
		var cert struct {
			ID string `json:"certificate_id"`
		}
		if err := json.Unmarshal(raw, &cert); err != nil {
			return nil, "", err
		}
		if cert.ID == "" {
			return nil, "", fmt.Errorf("certificate missing ID")
		}
		if !ids[cert.ID] {
			records = append(records, raw)
			ids[cert.ID] = true
		}
	}
	var next string
	if raw := result["next"]; len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &next); err != nil {
			return nil, "", err
		}
	}
	return records, next, nil
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
