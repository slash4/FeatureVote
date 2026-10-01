package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"FV_DATABASE_URL": "postgres://localhost/fv",
		"FV_HOST_SECRET":  strings.Repeat("h", 32),
		"FV_HOST_ISSUER":  "okokumo",
		"FV_ADMIN_TOKEN":  strings.Repeat("a", 32),
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.SubmitLimitPerDay != 5 || c.VoteLimitPerMinute != 30 ||
		c.ClockSkew != 30*time.Second || !c.CookieSecure || len(c.AllowedOrigins) != 0 || c.Audience != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadAudience(t *testing.T) {
	m := validEnv()
	m["FV_AUDIENCE"] = "  feedback.okokumo.com "
	c, err := Load(env(m))
	if err != nil || c.Audience != "feedback.okokumo.com" {
		t.Fatalf("audience = %q, %v", c.Audience, err)
	}
}

func TestLoadOverrides(t *testing.T) {
	m := validEnv()
	m["FV_ALLOWED_ORIGINS"] = " https://app.okokumo.com, https://okokumo.com ,,http://localhost:8090"
	m["FV_LISTEN_ADDR"] = "127.0.0.1:9000"
	m["FV_SUBMIT_LIMIT_PER_DAY"] = "7"
	m["FV_VOTE_LIMIT_PER_MINUTE"] = "3"
	m["FV_CLOCK_SKEW"] = "2m"
	m["FV_COOKIE_SECURE"] = "false"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://app.okokumo.com", "https://okokumo.com", "http://localhost:8090"}
	if strings.Join(c.AllowedOrigins, "|") != strings.Join(want, "|") {
		t.Fatalf("origins = %v", c.AllowedOrigins)
	}
	if c.ListenAddr != "127.0.0.1:9000" || c.SubmitLimitPerDay != 7 || c.VoteLimitPerMinute != 3 ||
		c.ClockSkew != 2*time.Minute || c.CookieSecure {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m map[string]string)
		wantErr string
	}{
		{"missing db", func(m map[string]string) { delete(m, "FV_DATABASE_URL") }, "FV_DATABASE_URL is required"},
		{"missing secret", func(m map[string]string) { delete(m, "FV_HOST_SECRET") }, "FV_HOST_SECRET is required"},
		{"short secret", func(m map[string]string) { m["FV_HOST_SECRET"] = "short" }, "FV_HOST_SECRET must be at least 32 bytes"},
		{"missing issuer", func(m map[string]string) { delete(m, "FV_HOST_ISSUER") }, "FV_HOST_ISSUER is required"},
		{"missing admin", func(m map[string]string) { delete(m, "FV_ADMIN_TOKEN") }, "FV_ADMIN_TOKEN is required"},
		{"short admin", func(m map[string]string) { m["FV_ADMIN_TOKEN"] = "x" }, "FV_ADMIN_TOKEN must be at least 32 bytes"},
		{"admin equals secret", func(m map[string]string) { m["FV_ADMIN_TOKEN"] = m["FV_HOST_SECRET"] }, "must differ"},
		{"bad origin path", func(m map[string]string) { m["FV_ALLOWED_ORIGINS"] = "https://a.com/x" }, "not an origin"},
		{"bad origin scheme", func(m map[string]string) { m["FV_ALLOWED_ORIGINS"] = "a.com" }, "not an origin"},
		{"bad submit limit", func(m map[string]string) { m["FV_SUBMIT_LIMIT_PER_DAY"] = "0" }, "FV_SUBMIT_LIMIT_PER_DAY"},
		{"bad vote limit", func(m map[string]string) { m["FV_VOTE_LIMIT_PER_MINUTE"] = "abc" }, "FV_VOTE_LIMIT_PER_MINUTE"},
		{"bad skew", func(m map[string]string) { m["FV_CLOCK_SKEW"] = "30" }, "FV_CLOCK_SKEW"},
		{"skew too large", func(m map[string]string) { m["FV_CLOCK_SKEW"] = "3m" }, "between 0 and 2m0s"},
		{"bad cookie secure", func(m map[string]string) { m["FV_COOKIE_SECURE"] = "maybe" }, "FV_COOKIE_SECURE"},
		{"audience too long", func(m map[string]string) { m["FV_AUDIENCE"] = strings.Repeat("a", 256) }, "FV_AUDIENCE must be at most 255 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validEnv()
			tc.mutate(m)
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestTrustedProxies(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(c.TrustedProxies) != "[127.0.0.0/8 ::1/128]" {
		t.Fatalf("default trusted proxies = %v", c.TrustedProxies)
	}
	m := validEnv()
	m["FV_TRUSTED_PROXIES"] = " 10.0.0.0/8, 192.168.1.5 ,fd00::/8"
	if c, err = Load(env(m)); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(c.TrustedProxies) != "[10.0.0.0/8 192.168.1.5/32 fd00::/8]" {
		t.Fatalf("trusted proxies = %v", c.TrustedProxies)
	}
	m["FV_TRUSTED_PROXIES"] = "none"
	if c, err = Load(env(m)); err != nil || len(c.TrustedProxies) != 0 {
		t.Fatalf("none => %v, %v", c.TrustedProxies, err)
	}
	m["FV_TRUSTED_PROXIES"] = "10.0.0.0/8,proxy.local"
	if _, err = Load(env(m)); err == nil || !strings.Contains(err.Error(), "FV_TRUSTED_PROXIES") {
		t.Fatalf("bad proxy err = %v", err)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"FV_DATABASE_URL", "FV_HOST_SECRET", "FV_HOST_ISSUER", "FV_ADMIN_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
