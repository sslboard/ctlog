package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/sslboard/ctlog/internal/api"
	"github.com/sslboard/ctlog/internal/config"
)

// keyPrefix is the start of every SSLBoard API key.
const keyPrefix = "sslb_live_"

// maxPollNetworkErrors is how many consecutive network failures a device
// login poll tolerates before giving up.
const maxPollNetworkErrors = 3

// pollUnit scales the server's polling interval. Tests shrink it.
var pollUnit = time.Second

// openBrowser opens url in the default browser. Tests replace it.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// hostname labels the key on the account page. Tests replace it.
var hostname = os.Hostname

func runLogin(ctx context.Context, e *env, opts options) int {
	path, err := config.Path(e.getenv)
	if err != nil {
		fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
		return ExitUsage
	}

	var key, email string
	var plan api.Plan
	if opts.key != "" {
		k, code := readKeyFlag(e, opts.key)
		if code != ExitOK {
			return code
		}
		e.client.APIKey = k
		me, err := e.client.Me(ctx)
		if err != nil {
			var apiErr *api.Error
			if errors.As(err, &apiErr) && apiErr.Status == 401 {
				fmt.Fprintf(e.stderr, "ctlog: that key was rejected (revoked or invalid). Create one at %s\n", e.accountURL())
				return ExitAuth
			}
			return e.reportError(err)
		}
		key, email, plan = k, me.Email, me.Plan
	} else {
		token, code := deviceLogin(ctx, e, opts)
		if code != ExitOK {
			return code
		}
		key, email, plan = token.APIKey, token.Email, token.Plan
	}

	if err := config.Save(path, config.Config{APIKey: key, Email: email}); err != nil {
		fmt.Fprintf(e.stderr, "ctlog: saving the key: %v\n", err)
		return ExitAPI
	}
	fmt.Fprintf(e.stderr, "Signed in as %s. The key is saved in %s.\n", email, path)
	if plan.Tier == "pro" {
		fmt.Fprintln(e.stderr, "Hostname API plan active: use --all to get every hostname.")
	} else {
		fmt.Fprintf(e.stderr, "Free plan: 100 hostnames per search. Subscribe for full results at %s\n", e.accountURL())
	}
	if strings.TrimSpace(e.getenv(config.EnvAPIKey)) != "" {
		fmt.Fprintf(e.stderr, "Note: %s is set and takes precedence over the saved key.\n", config.EnvAPIKey)
	}
	return ExitOK
}

func readKeyFlag(e *env, value string) (string, int) {
	key := value
	if value == "-" {
		if e.stdin == nil {
			return "", usageError(e.stderr, "no stdin to read the key from")
		}
		line, err := bufio.NewReader(e.stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", usageError(e.stderr, "no key on stdin")
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, keyPrefix) {
		return "", usageError(e.stderr, "API keys start with "+keyPrefix)
	}
	return key, ExitOK
}

func deviceLogin(ctx context.Context, e *env, opts options) (*api.DeviceToken, int) {
	name, _ := hostname()
	code, err := e.client.StartDeviceLogin(ctx, name)
	if err != nil {
		return nil, e.reportError(err)
	}

	link := code.VerificationURIComplete
	if link == "" {
		link = code.VerificationURI
	}
	fmt.Fprintf(e.stderr, "To sign in, open %s\nand check that the page shows the code %s.\n", link, code.UserCode)
	if !opts.noBrowser {
		if err := openBrowser(link); err != nil {
			fmt.Fprintln(e.stderr, "(Could not open a browser. Copy the link above.)")
		}
	}
	fmt.Fprintln(e.stderr, "Waiting for approval...")

	interval := time.Duration(max(code.Interval, 1)) * pollUnit
	deadline := time.Now().Add(time.Duration(max(code.ExpiresIn, 60)) * pollUnit)
	networkErrors := 0
	for {
		select {
		case <-ctx.Done():
			return nil, e.reportError(ctx.Err())
		case <-time.After(interval):
		}

		token, err := e.client.PollDeviceToken(ctx, code.DeviceCode)
		if err == nil {
			return token, ExitOK
		}
		var apiErr *api.Error
		if !errors.As(err, &apiErr) {
			// The poll interval is close to common keep-alive timeouts, so a
			// reused connection can be reset under us. Keep polling through
			// a few of those instead of abandoning a login the user may be
			// approving right now.
			if networkErrors++; networkErrors <= maxPollNetworkErrors && ctx.Err() == nil {
				continue
			}
			return nil, e.reportError(err)
		}
		networkErrors = 0
		if apiErr.Code == "" {
			return nil, e.reportError(err)
		}
		switch apiErr.Code {
		case api.DeviceAuthorizationPending:
		case api.DeviceSlowDown:
			// RFC 8628 §3.5: add 5 seconds on every slow_down.
			interval += 5 * pollUnit
		case api.DeviceAccessDenied:
			fmt.Fprintln(e.stderr, "ctlog: the login was denied in the browser.")
			return nil, ExitAuth
		case api.DeviceExpiredToken:
			fmt.Fprintln(e.stderr, "ctlog: the code expired before it was approved. Run 'ctlog login' again.")
			return nil, ExitAuth
		default:
			fmt.Fprintf(e.stderr, "ctlog: login failed: %s\n", apiErr.Message)
			return nil, ExitAuth
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(e.stderr, "ctlog: the code expired before it was approved. Run 'ctlog login' again.")
			return nil, ExitAuth
		}
	}
}

func runLogout(e *env) int {
	path, err := config.Path(e.getenv)
	if err != nil {
		fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
		return ExitUsage
	}
	cfg, err := config.Load(path)
	if err == nil && cfg.APIKey == "" {
		fmt.Fprintln(e.stderr, "Not signed in.")
	} else {
		if err := config.Remove(path); err != nil {
			fmt.Fprintf(e.stderr, "ctlog: %v\n", err)
			return ExitAPI
		}
		fmt.Fprintf(e.stderr, "Signed out. The key still works until you revoke it at %s\n", e.accountURL())
	}
	if strings.TrimSpace(e.getenv(config.EnvAPIKey)) != "" {
		fmt.Fprintf(e.stderr, "%s is still set, so ctlog keeps using that key.\n", config.EnvAPIKey)
	}
	return ExitOK
}

func runStatus(ctx context.Context, e *env, opts options) int {
	if e.client.APIKey == "" {
		if opts.output == "json" {
			fmt.Fprintln(e.stdout, `{"signed_in":false,"plan":{"tier":"free"}}`)
			return ExitOK
		}
		fmt.Fprintln(e.stdout, "Not signed in.")
		fmt.Fprintln(e.stdout, "Plan:    free (100 hostnames per search, 1 search per minute)")
		fmt.Fprintln(e.stdout, "Run 'ctlog login' to sign in.")
		return ExitOK
	}

	me, err := e.client.Me(ctx)
	if err != nil {
		return e.reportError(err)
	}
	if opts.output == "json" {
		e.stdout.Write(me.Raw)
		fmt.Fprintln(e.stdout)
		return ExitOK
	}

	source := "saved by ctlog login"
	if e.keySource == config.SourceEnv {
		source = "from " + config.EnvAPIKey
	}
	fmt.Fprintf(e.stdout, "Account: %s\n", me.Email)
	fmt.Fprintf(e.stdout, "Plan:    %s\n", describePlan(me.Plan))
	if me.Limits.HostnamesPerRequest > 0 {
		paging := ""
		if me.Limits.Paging {
			paging = ", --all pages through the rest"
		}
		fmt.Fprintf(e.stdout, "Limits:  %s hostnames per request%s, %s\n",
			formatCount(int64(me.Limits.HostnamesPerRequest)), paging, me.Limits.Requests)
	}
	fmt.Fprintf(e.stdout, "Key:     %s (%s...), %s\n", me.Key.Name, me.Key.Prefix, source)
	fmt.Fprintf(e.stdout, "Usage:   %s searches today, %s in the last 30 days (%s hostnames)\n",
		formatCount(me.Usage.SearchesToday), formatCount(me.Usage.Searches30d), formatCount(me.Usage.Hostnames30d))
	if me.UpgradeURL != "" && me.Plan.Tier != "pro" {
		fmt.Fprintf(e.stdout, "Get every hostname with the hostname API plan: %s\n", e.accountURL())
	}
	return ExitOK
}

func describePlan(plan api.Plan) string {
	date := plan.CurrentPeriodEnd
	if len(date) >= 10 {
		date = date[:10]
	}
	if plan.Tier == "pro" {
		switch {
		case plan.Status == "past_due":
			return "hostname API (payment failed, Stripe is retrying the card)"
		case plan.CancelAtPeriodEnd && date != "":
			return "hostname API (cancelled, full results until " + date + ")"
		case date != "":
			return "hostname API (renews " + date + ")"
		default:
			return "hostname API"
		}
	}
	if plan.Status != "" {
		return "free (subscription " + plan.Status + ")"
	}
	return "free"
}
