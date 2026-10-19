package query

import (
	"strings"

	"golang.org/x/net/idna"
)

// ASCII returns hostname with every non-ASCII label in punycode (xn--) form,
// the form certificates carry. Other labels, including "*" and "_dmarc", are
// kept as is, and so is any label idna rejects.
func ASCII(hostname string) string {
	return mapLabels(hostname, func(label string) (string, error) {
		if isASCII(label) {
			return label, nil
		}
		return idna.Lookup.ToASCII(label)
	})
}

// Unicode returns hostname with every punycode (xn--) label decoded, for
// display. Labels that fail to decode are kept as is.
func Unicode(hostname string) string {
	return mapLabels(hostname, func(label string) (string, error) {
		if !strings.HasPrefix(label, "xn--") {
			return label, nil
		}
		return idna.Display.ToUnicode(label)
	})
}

func mapLabels(hostname string, convert func(string) (string, error)) string {
	labels := strings.Split(hostname, ".")
	for i, label := range labels {
		if converted, err := convert(label); err == nil {
			labels[i] = converted
		}
	}
	return strings.Join(labels, ".")
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 127 {
			return false
		}
	}
	return true
}
