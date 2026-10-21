// Package cli implements the ctlog command line.
package cli

import (
	"context"
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
	"github.com/sslboard/ctlog/internal/config"
)

// Exit codes.
const (
	ExitOK          = 0
	ExitUsage       = 1
	ExitAPI         = 2
	ExitRateLimited = 3
	// ExitAuth covers a rejected API key and a denied or expired login.
	ExitAuth = 4
)

// retryDelay is the backoff before retrying a 502/503. Tests shorten it.
var retryDelay = time.Second

const usage = `ctlog: list hostnames seen in Certificate Transparency logs, by SSLBoard.

Usage:
  ctlog [flags] <domain>
  ctlog find [flags] <domain>
  ctlog -f domains.txt [flags]
  ctlog login [--key KEY] [--no-browser]
  ctlog logout
  ctlog status
  ctlog version

<domain> can be example.com, %.example.com or *.example.com. All three mean
"this name and everything below it". Internationalized names can be typed in
either form: bücher.example or xn--bcher-kva.example.

Flags:
  -o, --output FORMAT     text (default), json or crtsh
      --all               fetch every page (needs the hostname API plan)
  -f, --file FILE         read domains from FILE, one per line ("-" for
                          stdin); results are merged and deduplicated
      --no-wildcards      exclude wildcard names (*.example.com), which are
                          listed by default
      --no-apex           exclude the queried name itself
      --unicode           print internationalized names as Unicode
                          (bücher.example) instead of punycode
                          (xn--bcher-kva.example), the default
  -q, --quiet             suppress notices on stderr
  -h, --help              show this help

Login flags:
      --key KEY           save an existing API key instead of signing in
                          in the browser ("-" reads it from stdin)
      --no-browser        print the login link without opening a browser

Hostnames go to stdout and notices to stderr, so pipes and files only get hostnames:
  ctlog example.com > hostnames.txt

Without an account, ctlog returns up to 100 hostnames per search and allows
1 search per minute. The hostname API plan returns every hostname, in pages of
up to 10,000 (use --all), at 60 requests per minute; --all and -f wait when
they reach that limit. Run 'ctlog login' to
sign in, then subscribe at https://sslboard.com/account/api/

Environment:
  SSLBOARD_API_KEY   API key, used instead of the one saved by ctlog login
  CTLOG_API_URL      API base URL (default https://sslboard.com)

Exit codes: 0 ok, 1 usage or invalid domain, 2 API or network error,
3 rate limited, 4 API key rejected or login not approved.
`

type options struct {
	output      string
	noWildcards bool
	noApex      bool
	unicode     bool
	quiet       bool
	help        bool
	version     bool
	all         bool
	file        string
	key         string
	noBrowser   bool
}

// env bundles what a command needs besides its options.
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	client         *api.Client
	keySource      config.Source
}

// Run executes ctlog with args (without the program name) and returns the
// process exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, version string) int {
	opts, positional, err := parseArgs(args)
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

	command := ""
	if len(positional) > 0 {
		switch positional[0] {
		case "version", "help", "login", "logout", "status":
			command = positional[0]
			positional = positional[1:]
		case "find":
			positional = positional[1:]
		}
	}

	switch command {
	case "version":
		if len(positional) > 0 {
			return usageError(stderr, "version takes no arguments")
		}
		printVersion(stdout, version)
		return ExitOK
	case "help":
		io.WriteString(stdout, usage)
		return ExitOK
	case "login", "logout", "status":
		if len(positional) > 0 {
			return usageError(stderr, command+" takes no arguments")
		}
	}
	if command != "login" && (opts.key != "" || opts.noBrowser) {
		return usageError(stderr, "--key and --no-browser only apply to ctlog login")
	}

	switch opts.output {
	case "text", "json", "crtsh":
	default:
		return usageError(stderr, fmt.Sprintf("unknown output format %q (want text, json or crtsh)", opts.output))
	}

	baseURL := strings.TrimSpace(getenv("CTLOG_API_URL"))
	if baseURL == "" {
		baseURL = api.DefaultBaseURL
	}
	e := &env{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		getenv: getenv,
		client: &api.Client{
			BaseURL:    baseURL,
			UserAgent:  userAgent(version),
			HTTPClient: &http.Client{Timeout: api.DefaultTimeout},
			RetryDelay: retryDelay,
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if command == "login" {
		return runLogin(ctx, e, opts)
	}
	if command == "logout" {
		return runLogout(e)
	}

	key, source, err := config.ResolveKey(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "ctlog: reading saved login: %v\n", err)
		return ExitUsage
	}
	e.client.APIKey = key
	e.keySource = source

	if command == "status" {
		return runStatus(ctx, e, opts)
	}
	return runLookup(ctx, e, opts, positional)
}

// parseArgs accepts flags before and after positional arguments, so both
// "ctlog -o json example.com" and "ctlog example.com -o json" work.
func parseArgs(args []string) (options, []string, error) {
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
	fs.BoolVar(&opts.all, "all", false, "")
	fs.StringVar(&opts.file, "f", "", "")
	fs.StringVar(&opts.file, "file", "", "")
	fs.StringVar(&opts.key, "key", "", "")
	fs.BoolVar(&opts.noBrowser, "no-browser", false, "")

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

func (e *env) accountURL() string {
	return strings.TrimRight(e.client.BaseURL, "/") + "/account/api/"
}

func (e *env) reportError(err error) int {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Status == http.StatusTooManyRequests:
			retry := "Retry in about a minute."
			if apiErr.RetryAfter > 0 {
				retry = fmt.Sprintf("Retry in %ds.", int(apiErr.RetryAfter.Round(time.Second).Seconds()))
			}
			if e.client.APIKey != "" && apiErr.Message != "" {
				fmt.Fprintf(e.stderr, "%s. %s\n", strings.TrimSuffix(apiErr.Message, "."), retry)
			} else {
				fmt.Fprintf(e.stderr, "Rate limited (1 search/minute without an account). %s\n", retry)
			}
			return ExitRateLimited
		case apiErr.Status == http.StatusUnauthorized:
			hint := "Run 'ctlog login' to sign in again."
			if e.keySource == config.SourceEnv {
				hint = "Check the key in " + config.EnvAPIKey + "."
			}
			fmt.Fprintf(e.stderr, "ctlog: API key rejected (revoked or invalid). %s\n", hint)
			return ExitAuth
		case apiErr.Status == http.StatusPaymentRequired:
			fmt.Fprintf(e.stderr, "ctlog: %v\n", apiErr)
			return ExitAuth
		case apiErr.Status == http.StatusBadRequest || apiErr.Status == http.StatusUnprocessableEntity:
			fmt.Fprintf(e.stderr, "ctlog: %v\n", apiErr)
			return ExitUsage
		default:
			if apiErr.Message != "" {
				fmt.Fprintf(e.stderr, "ctlog: API error (HTTP %d): %s\n", apiErr.Status, apiErr.Message)
			} else {
				fmt.Fprintf(e.stderr, "ctlog: API error (HTTP %d)\n", apiErr.Status)
			}
			return ExitAPI
		}
	}
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(e.stderr, "ctlog: interrupted")
		return ExitAPI
	}
	fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
	return ExitAPI
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
