// Command fvpending is the moderation-queue watchdog: it reports suggestions
// that have been pending moderation longer than a threshold and, when
// FV_MODERATION_PING_URL is set, pings a healthchecks.io-compatible cron check
// (success on the base URL, failure on <url>/fail with the report as body).
//
//	FV_DATABASE_URL=postgres://... [FV_MODERATION_PING_URL=https://app.okokumo.com/ping/<token>] \
//	  fvpending [-older-than 48h] [-admin-url https://feedback.okokumo.com/admin] [-timeout 30s]
//
// Exit codes: 0 nothing stale, 1 stale suggestions found, 2 the check could
// not run or the ping failed. The ping URL holds a secret token: it is only
// read from the environment and never printed (errors show scheme+host).
// Output carries idea ids and ages only, never titles, bodies or authors.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/slash4/featurevote/internal/store"
)

const (
	maxListed      = 20
	maxBody        = 10 << 10 // the check stores at most 10 KB of a /fail body
	attemptTimeout = 10 * time.Second
)

// pingBackoff is the wait before each retry; len+1 attempts in total.
// Tests shorten it.
var pingBackoff = []time.Duration{2 * time.Second, 4 * time.Second}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, env func(string) string, stdout, stderr io.Writer) int {
	// The ping URL is validated first: once it is known, every later failure
	// (bad flags, DB down) is reported to the check instead of staying silent.
	var pingURL *url.URL
	if raw := env("FV_MODERATION_PING_URL"); raw != "" {
		u, err := parsePingURL(raw)
		if err != nil {
			fmt.Fprintln(stderr, "fvpending: FV_MODERATION_PING_URL: "+err.Error())
			return 2
		}
		pingURL = u
	}
	client := newPingClient()
	cantRun := func(err error) int {
		msg := "fvpending: check could not run: " + err.Error()
		fmt.Fprintln(stderr, msg)
		if pingURL != nil {
			if perr := ping(ctx, client, pingURL.String(), true, msg, pingBackoff); perr != nil {
				fmt.Fprintln(stderr, "fvpending: "+perr.Error())
			}
		}
		return 2
	}

	fs := flag.NewFlagSet("fvpending", flag.ContinueOnError)
	fs.SetOutput(stderr)
	olderThan := fs.Duration("older-than", 48*time.Hour, "report suggestions pending longer than this (> 0)")
	adminURL := fs.String("admin-url", "", "admin page URL printed in the report (empty = omitted)")
	timeout := fs.Duration("timeout", 30*time.Second, "database connect + query timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return cantRun(fmt.Errorf("bad arguments: %w", err))
	}
	if fs.NArg() > 0 {
		return cantRun(fmt.Errorf("unexpected arguments: %q", fs.Args()))
	}
	if *olderThan <= 0 {
		return cantRun(fmt.Errorf("-older-than must be > 0, got %s", *olderThan))
	}
	if *timeout <= 0 {
		return cantRun(fmt.Errorf("-timeout must be > 0, got %s", *timeout))
	}
	dsn := env("FV_DATABASE_URL")
	if dsn == "" {
		return cantRun(errors.New("FV_DATABASE_URL must be set"))
	}

	stale, pendingTotal, err := query(ctx, dsn, *olderThan, *timeout)
	if err != nil {
		return cantRun(err)
	}

	report := formatReport(stale, pendingTotal, *olderThan, strings.TrimSpace(*adminURL))
	fmt.Fprint(stdout, report)
	code := 0
	if len(stale) > 0 {
		code = 1
	}
	if pingURL != nil {
		if err := ping(ctx, client, pingURL.String(), code == 1, report, pingBackoff); err != nil {
			fmt.Fprintln(stderr, "fvpending: "+err.Error())
			return 2
		}
	}
	return code
}

// query loads the stale pending ideas and, when there are none, the total
// pending count. Errors never contain the database password.
func query(ctx context.Context, dsn string, olderThan, timeout time.Duration) ([]store.PendingIdea, int, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Parse errors may echo parts of the DSN; keep the message fixed.
		return nil, 0, errors.New("FV_DATABASE_URL is not a valid Postgres connection string")
	}
	password := cfg.ConnConfig.Password
	cfg.MaxConns = 1
	cfg.ConnConfig.ConnectTimeout = timeout
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.ConnConfig.RuntimeParams["application_name"] = "fvpending"

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stale, n, err := func() ([]store.PendingIdea, int, error) {
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, 0, fmt.Errorf("connect: %w", err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			return nil, 0, fmt.Errorf("connect: %w", err)
		}
		st := store.New(pool)
		stale, err := st.PendingOlderThan(ctx, olderThan)
		if err != nil {
			return nil, 0, fmt.Errorf("query pending ideas: %w", err)
		}
		if len(stale) > 0 {
			return stale, 0, nil
		}
		n, err := st.PendingCount(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("count pending ideas: %w", err)
		}
		return stale, n, nil
	}()
	if err != nil && password != "" && strings.Contains(err.Error(), password) {
		// pgx does not include the password in its errors (tested); this is
		// a backstop in case a future version does.
		err = errors.New(strings.ReplaceAll(err.Error(), password, "xxxxx"))
	}
	return stale, n, err
}

func formatReport(stale []store.PendingIdea, pendingTotal int, olderThan time.Duration, adminURL string) string {
	var b strings.Builder
	threshold := shortDuration(olderThan)
	if len(stale) == 0 {
		fmt.Fprintf(&b, "OK: no suggestion has been waiting for moderation longer than %s.\n", threshold)
		fmt.Fprintf(&b, "pending (any age): %d\n", pendingTotal)
		return b.String()
	}
	noun := "suggestions"
	if len(stale) == 1 {
		noun = "suggestion"
	}
	fmt.Fprintf(&b, "%d %s waiting for moderation longer than %s:\n", len(stale), noun, threshold)
	for i, p := range stale {
		if i == maxListed {
			fmt.Fprintf(&b, "  ... and %d more\n", len(stale)-maxListed)
			break
		}
		fmt.Fprintf(&b, "  #%d  pending %s  (submitted %s)\n", p.ID, formatAge(p.Age), p.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
	}
	if adminURL != "" {
		fmt.Fprintf(&b, "Moderate at %s\n", adminURL)
	}
	return b.String()
}

// shortDuration is d.String() without the zero tails people do not write:
// 48h0m0s -> 48h, 1h30m0s -> 1h30m, 2m0s -> 2m (10s and 1h0m5s are unchanged).
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// formatAge renders d as "Xd Yh", or "Yh Zm" below one day (both truncated).
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 24*time.Hour {
		days := d / (24 * time.Hour)
		return fmt.Sprintf("%dd %dh", days, (d-days*24*time.Hour)/time.Hour)
	}
	return fmt.Sprintf("%dh %dm", d/time.Hour, (d%time.Hour)/time.Minute)
}

// parsePingURL validates the check's base ping URL. Errors never contain the
// URL itself (it holds the check's secret token).
func parsePingURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("must be an absolute http or https URL")
	}
	if u.Host == "" || u.Opaque != "" {
		return nil, errors.New("must be an absolute URL with a host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return nil, errors.New("must not have a query or fragment")
	}
	p := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(p, "/fail") || strings.HasSuffix(p, "/start") {
		return nil, errors.New("must be the check's base URL, not its /fail or /start endpoint")
	}
	u.Path = p
	u.RawPath = ""
	return u, nil
}

// redact reduces a URL to scheme+host.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<ping url>"
	}
	return u.Scheme + "://" + u.Host
}

// ping POSTs body to base (or base+"/fail"), retrying after each backoff
// entry on network errors and non-2xx responses. The returned error has the
// URL reduced to scheme+host.
func ping(ctx context.Context, client *http.Client, base string, fail bool, body string, backoff []time.Duration) error {
	target := base
	if fail {
		target = strings.TrimRight(base, "/") + "/fail"
	}
	if len(body) > maxBody {
		// Byte cut can split a rune; Postgres TEXT on the receiving side
		// rejects invalid UTF-8 and would lose the whole ping.
		body = strings.ToValidUTF8(body[:maxBody], "")
	}
	attempts := len(backoff) + 1
	last := pingOnce(ctx, client, target, body)
	for i := 0; last != nil && i < len(backoff); i++ {
		t := time.NewTimer(backoff[i])
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("ping to %s interrupted: %s", redact(base), scrub(last.Error(), base))
		case <-t.C:
		}
		last = pingOnce(ctx, client, target, body)
	}
	if last == nil {
		return nil
	}
	kind := "success"
	if fail {
		kind = "failure"
	}
	return fmt.Errorf("%s ping to %s failed after %d attempts: %s", kind, redact(base), attempts, scrub(last.Error(), base))
}

// newPingClient returns the client used for pings. It never follows
// redirects: a 3xx would otherwise re-send the /fail body (the report) to
// wherever the Location points. The 3xx is returned as-is, so pingOnce treats
// it as a non-2xx failure.
func newPingClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func pingOnce(ctx context.Context, client *http.Client, target, body string) error {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return errors.New("cannot build request")
	}
	req.Header.Set("User-Agent", "fvpending")
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return uerr.Err // drop the URL
		}
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	return nil
}

// scrub removes any trace of the ping URL's path (the token) from msg.
func scrub(msg, base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return msg
	}
	msg = strings.ReplaceAll(msg, base, redact(base))
	for _, seg := range strings.Split(u.Path, "/") {
		if len(seg) >= 6 { // short segments ("ping") are not secret and would mangle the message
			msg = strings.ReplaceAll(msg, seg, "<redacted>")
		}
	}
	return msg
}
