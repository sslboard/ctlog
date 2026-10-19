// Package cli implements the ctlog command line.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sslboard/ctlog/internal/api"
	"github.com/sslboard/ctlog/internal/query"
)

// Exit codes.
const (
	ExitOK          = 0
	ExitUsage       = 1
	ExitAPI         = 2
	ExitRateLimited = 3
)

// retryDelay is the backoff before retrying a 502/503. Tests shorten it.
var retryDelay = time.Second

const usage = `ctlog: list hostnames seen in Certificate Transparency logs, by SSLBoard.

Usage:
  ctlog [flags] <domain>
  ctlog find [flags] <domain>
  ctlog version

<domain> can be example.com, %.example.com or *.example.com. All three mean
"this name and everything below it". Internationalized names can be typed in
either form: bücher.example or xn--bcher-kva.example.

Flags:
  -o, --output FORMAT     text (default), json or crtsh
      --no-wildcards      exclude wildcard names (*.example.com), which are
                          listed by default
      --no-apex           exclude the queried name itself
      --unicode           print internationalized names as Unicode
                          (bücher.example) instead of punycode
                          (xn--bcher-kva.example), the default
  -q, --quiet             suppress notices on stderr
  -h, --help              show this help

Hostnames go to stdout and notices to stderr, so pipes and files only get hostnames:
  ctlog example.com > hostnames.txt

Without an account, ctlog returns up to 100 hostnames per search and allows
1 search per minute. See how a domain's hostnames are spread across its
subdomains at https://sslboard.com/subdomain/

Environment:
  CTLOG_API_URL   API base URL (default https://sslboard.com)

Exit codes: 0 ok, 1 usage or invalid domain, 2 API or network error,
3 rate limited.
`

type options struct {
	output      string
	noWildcards bool
	noApex      bool
	unicode     bool
	quiet       bool
	help        bool
	version     bool
}

// Run executes ctlog with args (without the program name) and returns the
// process exit code.
func Run(args []string, stdout, stderr io.Writer, getenv func(string) string, version string) int {
	opts, positional, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			io.WriteString(stdout, usage)
			return ExitOK
		}
		return usageError(stderr, err.Error())
	}
	if opts.help {
		io.WriteString(stdout, usage)
		return ExitOK
	}
	if opts.version {
		printVersion(stdout, version)
		return ExitOK
	}

	if len(positional) > 0 {
		switch positional[0] {
		case "version":
			if len(positional) > 1 {
				return usageError(stderr, "version takes no arguments")
			}
			printVersion(stdout, version)
			return ExitOK
		case "help":
			io.WriteString(stdout, usage)
			return ExitOK
		case "find":
			positional = positional[1:]
		}
	}

	switch len(positional) {
	case 0:
		return usageError(stderr, "missing domain")
	case 1:
	default:
		return usageError(stderr, "one domain per invocation (got "+strconv.Itoa(len(positional))+")")
	}

	switch opts.output {
	case "text", "json", "crtsh":
	default:
		return usageError(stderr, fmt.Sprintf("unknown output format %q (want text, json or crtsh)", opts.output))
	}

	domain, err := query.Normalize(positional[0])
	if err != nil {
		fmt.Fprintf(stderr, "ctlog: %v\n", err)
		return ExitUsage
	}

	baseURL := strings.TrimSpace(getenv("CTLOG_API_URL"))
	if baseURL == "" {
		baseURL = api.DefaultBaseURL
	}
	client := &api.Client{
		BaseURL:    baseURL,
		UserAgent:  userAgent(version),
		HTTPClient: &http.Client{Timeout: api.DefaultTimeout},
		RetryDelay: retryDelay,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res, err := client.Lookup(ctx, domain, api.Options{IncludeApex: !opts.noApex, ExcludeWildcards: opts.noWildcards})
	if err != nil {
		return reportError(stderr, err)
	}

	convert := query.ASCII
	if opts.unicode {
		convert = query.Unicode
	}
	for i, h := range res.Hostnames {
		res.Hostnames[i] = convert(h)
	}

	if err := writeResult(stdout, opts.output, res); err != nil {
		fmt.Fprintf(stderr, "ctlog: writing output: %v\n", err)
		return ExitAPI
	}
	if !opts.quiet {
		writeNotice(stderr, domain, res)
	}
	return ExitOK
}

// parseArgs accepts flags before and after positional arguments, so both
// "ctlog -o json example.com" and "ctlog example.com -o json" work.
func parseArgs(args []string, stderr io.Writer) (options, []string, error) {
	opts := options{output: "text"}
	fs := flag.NewFlagSet("ctlog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.output, "o", opts.output, "")
	fs.StringVar(&opts.output, "output", opts.output, "")
	fs.BoolVar(&opts.noWildcards, "no-wildcards", false, "")
	fs.BoolVar(&opts.noApex, "no-apex", false, "")
	fs.BoolVar(&opts.unicode, "unicode", false, "")
	fs.BoolVar(&opts.quiet, "q", false, "")
	fs.BoolVar(&opts.quiet, "quiet", false, "")
	fs.BoolVar(&opts.help, "help", false, "")
	fs.BoolVar(&opts.version, "version", false, "")

	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return opts, nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		// fs.Parse consumes a "--" terminator; everything after it is positional.
		if len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
			positional = append(positional, rest...)
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return opts, positional, nil
}

func usageError(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "ctlog: %s\nRun 'ctlog --help' for usage.\n", msg)
	return ExitUsage
}

func reportError(stderr io.Writer, err error) int {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusTooManyRequests:
			retry := "Retry in about a minute."
			if apiErr.RetryAfter > 0 {
				retry = fmt.Sprintf("Retry in %ds.", int(apiErr.RetryAfter.Round(time.Second).Seconds()))
			}
			fmt.Fprintf(stderr, "Rate limited (1 search/minute without an account). %s\n", retry)
			return ExitRateLimited
		case apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnprocessableEntity:
			fmt.Fprintf(stderr, "ctlog: %v\n", apiErr)
			return ExitUsage
		default:
			if apiErr.Message != "" {
				fmt.Fprintf(stderr, "ctlog: API error (HTTP %d): %s\n", apiErr.Status, apiErr.Message)
			} else {
				fmt.Fprintf(stderr, "ctlog: API error (HTTP %d)\n", apiErr.Status)
			}
			return ExitAPI
		}
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "ctlog: interrupted")
		return ExitAPI
	}
	fmt.Fprintf(stderr, "ctlog: %v\n", err)
	return ExitAPI
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
		raw, err := replaceHostnames(res.Raw, res.Hostnames)
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

// replaceHostnames returns the JSON object raw with its "hostnames" member
// set to hostnames. Other members, and the member order, are kept as received.
func replaceHostnames(raw []byte, hostnames []string) ([]byte, error) {
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
		if key == "hostnames" {
			if value, err = json.Marshal(hostnames); err != nil {
				return nil, err
			}
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

func writeNotice(stderr io.Writer, domain string, res *api.Result) {
	returned := res.Returned
	if returned == 0 {
		returned = len(res.Hostnames)
	}
	if returned == 0 && !res.Truncated {
		fmt.Fprintf(stderr, "No hostnames found for %s.\n", domain)
		return
	}
	if !res.Truncated {
		return
	}
	total := formatCount(res.Total)
	if res.TotalIsLowerBound {
		total += "+"
	}
	msg := fmt.Sprintf("%s of %s hostnames (free limit).", formatCount(int64(returned)), total)
	if res.MapURL != "" {
		msg += " See how they're spread across subdomains: " + res.MapURL
	}
	fmt.Fprintln(stderr, msg)
}

// formatCount renders 12432 as "12,432".
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func userAgent(version string) string {
	return "ctlog/" + version + " (+https://github.com/sslboard/ctlog)"
}

func printVersion(w io.Writer, version string) {
	fmt.Fprintf(w, "ctlog %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
}
