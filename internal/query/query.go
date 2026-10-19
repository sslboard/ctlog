// Package query normalizes the domain a user types into the form the
// SSLBoard hostname API expects. The server is authoritative; these checks
// only exist to fail fast on input that can never succeed.
package query

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

// MaxLength is the longest DNS name the API accepts.
const MaxLength = 253

// commonPublicSuffixes lists multi-label public suffixes people are likely to
// type by mistake. Single-label suffixes (com, io, ...) are caught by the
// "needs a dot" rule. The server checks the full Public Suffix List.
var commonPublicSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true,
	"com.au": true, "net.au": true, "org.au": true,
	"co.nz": true, "co.za": true, "co.jp": true, "ne.jp": true, "co.kr": true,
	"co.in": true, "com.br": true, "com.cn": true, "com.mx": true,
	"com.tr": true, "com.tw": true, "com.sg": true, "com.hk": true,
}

// idnDots are the full stops IDNA treats as label separators, as typed by
// Chinese and Japanese input methods.
var idnDots = strings.NewReplacer("\u3002", ".", "\uff0e", ".", "\uff61", ".")

// Normalize turns user input such as "%.Example.com", "*.example.com" or
// "https://example.com/" into a bare lowercase domain ("example.com").
// Internationalized names such as "bücher.example" become punycode
// ("xn--bcher-kva.example").
func Normalize(input string) (string, error) {
	q := strings.ToLower(strings.TrimSpace(input))
	if q == "" {
		return "", errors.New("missing domain")
	}

	// Accept pasted URLs.
	if _, rest, ok := strings.Cut(q, "://"); ok {
		q = rest
	}
	if host, _, ok := strings.Cut(q, "/"); ok {
		q = host
	}

	q = idnDots.Replace(q)

	// crt.sh habits: %.example.com and *.example.com mean "everything below".
	q = strings.TrimPrefix(q, "%.")
	q = strings.TrimPrefix(q, "*.")
	q = strings.TrimSuffix(q, ".")

	if strings.ContainsAny(q, "%*") {
		return "", fmt.Errorf("invalid domain %q: wildcards are only allowed as a leading %%. or *.", input)
	}
	if q == "" {
		return "", errors.New("missing domain")
	}

	labels := strings.Split(q, ".")
	for i, label := range labels {
		if isASCII(label) {
			continue
		}
		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil {
			return "", fmt.Errorf("invalid domain %q: %q is not a valid internationalized label", input, label)
		}
		labels[i] = ascii
	}
	q = strings.Join(labels, ".")
	if len(q) > MaxLength {
		return "", fmt.Errorf("invalid domain: longer than %d characters", MaxLength)
	}
	for _, label := range labels {
		if err := checkLabel(label); err != nil {
			return "", fmt.Errorf("invalid domain %q: %w", input, err)
		}
	}
	if len(labels) < 2 || commonPublicSuffixes[q] {
		return "", fmt.Errorf("invalid domain %q: %s is a public suffix, query a domain below it (for example example.%s)", input, q, q)
	}
	return q, nil
}

func checkLabel(label string) error {
	if label == "" {
		return errors.New("empty label")
	}
	if len(label) > 63 {
		return errors.New("label longer than 63 characters")
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf("unexpected character %q", r)
		}
	}
	return nil
}
