package query

import (
	"strings"
	"testing"
)

func TestNormalizeAccepts(t *testing.T) {
	cases := map[string]string{
		"example.com":                 "example.com",
		"  Example.COM  ":             "example.com",
		"%.example.com":               "example.com",
		"*.example.com":               "example.com",
		"example.com.":                "example.com",
		"https://www.example.com/a?b": "www.example.com",
		"http://example.com":          "example.com",
		"_dmarc.example.com":          "_dmarc.example.com",
		"xn--bcher-kva.example":       "xn--bcher-kva.example",
		"api.example.co.uk":           "api.example.co.uk",
		"github.io":                   "github.io",
		"user.github.io":              "user.github.io",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("Normalize(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	cases := map[string]string{
		"":                                "missing domain",
		"%.":                              "missing domain",
		"com":                             "public suffix",
		"localhost":                       "public suffix",
		"co.uk":                           "public suffix",
		"%.co.uk":                         "public suffix",
		"ex%ample.com":                    "wildcards",
		"api.*.example.com":               "wildcards",
		"%example.com":                    "wildcards",
		"exa mple.com":                    "unexpected character",
		"a..example.com":                  "empty label",
		"bücher.example":                  "punycode",
		strings.Repeat("a", 64) + ".com":  "63 characters",
		strings.Repeat("a.", 127) + "com": "longer than 253",
	}
	for in, want := range cases {
		_, err := Normalize(in)
		if err == nil {
			t.Errorf("Normalize(%q) succeeded, want error containing %q", in, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Normalize(%q) error = %q, want it to contain %q", in, err, want)
		}
	}
}
