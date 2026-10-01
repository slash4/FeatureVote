// Package config parses and validates the FeatureVote service configuration
// from environment variables. Invalid configuration fails fast at boot.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the validated service configuration.
type Config struct {
	DatabaseURL string
	HostSecret  string
	HostIssuer  string
	// Audience, when set, is the aud every host token must carry
	// (FV_AUDIENCE). Empty disables the check.
	Audience           string
	AdminToken         string
	AllowedOrigins     []string
	ListenAddr         string
	SubmitLimitPerDay  int
	VoteLimitPerMinute int
	ClockSkew          time.Duration
	CookieSecure       bool
	// TrustedProxies are the peers whose X-Forwarded-For is believed when
	// deriving the client IP (admin login throttle). Default: loopback.
	TrustedProxies []netip.Prefix
}

const (
	minSecretLen   = 32
	maxClockSkew   = 2 * time.Minute
	maxAudienceLen = 255
)

// Load reads the configuration through getenv (usually os.Getenv) and
// validates it. All problems are reported together in one error.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		DatabaseURL:        strings.TrimSpace(getenv("FV_DATABASE_URL")),
		HostSecret:         getenv("FV_HOST_SECRET"),
		HostIssuer:         strings.TrimSpace(getenv("FV_HOST_ISSUER")),
		Audience:           strings.TrimSpace(getenv("FV_AUDIENCE")),
		AdminToken:         getenv("FV_ADMIN_TOKEN"),
		ListenAddr:         ":8080",
		SubmitLimitPerDay:  5,
		VoteLimitPerMinute: 30,
		ClockSkew:          30 * time.Second,
		CookieSecure:       true,
	}
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.DatabaseURL == "" {
		add("FV_DATABASE_URL is required")
	}
	if c.HostSecret == "" {
		add("FV_HOST_SECRET is required")
	} else if len(c.HostSecret) < minSecretLen {
		add("FV_HOST_SECRET must be at least %d bytes (got %d)", minSecretLen, len(c.HostSecret))
	}
	if c.HostIssuer == "" {
		add("FV_HOST_ISSUER is required (e.g. \"okokumo\")")
	}
	if len(c.Audience) > maxAudienceLen {
		add("FV_AUDIENCE must be at most %d bytes (got %d)", maxAudienceLen, len(c.Audience))
	}
	if c.AdminToken == "" {
		add("FV_ADMIN_TOKEN is required")
	} else if len(c.AdminToken) < minSecretLen {
		add("FV_ADMIN_TOKEN must be at least %d bytes (got %d)", minSecretLen, len(c.AdminToken))
	}
	if c.AdminToken != "" && c.AdminToken == c.HostSecret {
		add("FV_ADMIN_TOKEN must differ from FV_HOST_SECRET")
	}

	if v := strings.TrimSpace(getenv("FV_ALLOWED_ORIGINS")); v != "" {
		for _, o := range strings.Split(v, ",") {
			o = strings.TrimSpace(o)
			if o == "" {
				continue
			}
			if err := validateOrigin(o); err != nil {
				add("FV_ALLOWED_ORIGINS: %v", err)
				continue
			}
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	if v := strings.TrimSpace(getenv("FV_LISTEN_ADDR")); v != "" {
		c.ListenAddr = v
	}
	if v := strings.TrimSpace(getenv("FV_SUBMIT_LIMIT_PER_DAY")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			add("FV_SUBMIT_LIMIT_PER_DAY must be a positive integer (got %q)", v)
		} else {
			c.SubmitLimitPerDay = n
		}
	}
	if v := strings.TrimSpace(getenv("FV_VOTE_LIMIT_PER_MINUTE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			add("FV_VOTE_LIMIT_PER_MINUTE must be a positive integer (got %q)", v)
		} else {
			c.VoteLimitPerMinute = n
		}
	}
	if v := strings.TrimSpace(getenv("FV_CLOCK_SKEW")); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			add("FV_CLOCK_SKEW must be a Go duration like \"30s\" (got %q)", v)
		case d < 0 || d > maxClockSkew:
			add("FV_CLOCK_SKEW must be between 0 and %s (got %s)", maxClockSkew, d)
		default:
			c.ClockSkew = d
		}
	}
	if v := strings.TrimSpace(getenv("FV_COOKIE_SECURE")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			add("FV_COOKIE_SECURE must be true or false (got %q)", v)
		} else {
			c.CookieSecure = b
		}
	}
	c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	if v := strings.TrimSpace(getenv("FV_TRUSTED_PROXIES")); v != "" {
		c.TrustedProxies = nil
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" || (p == "none" && v == "none") {
				continue
			}
			pfx, err := netip.ParsePrefix(p)
			if err != nil {
				if a, aerr := netip.ParseAddr(p); aerr == nil {
					pfx, err = a.Prefix(a.BitLen())
				}
			}
			if err != nil {
				add("FV_TRUSTED_PROXIES: %q is not an IP or CIDR", p)
				continue
			}
			c.TrustedProxies = append(c.TrustedProxies, pfx.Masked())
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return c, nil
}

// validateOrigin accepts exact origins like "https://app.example.com" or
// "http://localhost:8090" (scheme + host [+ port], nothing else).
func validateOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q is not an origin (expected scheme://host[:port])", o)
	}
	return nil
}
