package query

import "testing"

func TestASCII(t *testing.T) {
	cases := map[string]string{
		"example.com":           "example.com",
		"www.münchen.de":        "www.xn--mnchen-3ya.de",
		"www.xn--mnchen-3ya.de": "www.xn--mnchen-3ya.de",
		"*.münchen.de":          "*.xn--mnchen-3ya.de",
		"_dmarc.bücher.example": "_dmarc.xn--bcher-kva.example",
		"bü cher.example":       "bü cher.example",
	}
	for in, want := range cases {
		if got := ASCII(in); got != want {
			t.Errorf("ASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnicode(t *testing.T) {
	cases := map[string]string{
		"example.com":                  "example.com",
		"www.xn--mnchen-3ya.de":        "www.münchen.de",
		"www.münchen.de":               "www.münchen.de",
		"*.xn--mnchen-3ya.de":          "*.münchen.de",
		"_dmarc.xn--bcher-kva.example": "_dmarc.bücher.example",
		"xn--a.example":                "xn--a.example",
	}
	for in, want := range cases {
		if got := Unicode(in); got != want {
			t.Errorf("Unicode(%q) = %q, want %q", in, got, want)
		}
	}
}
