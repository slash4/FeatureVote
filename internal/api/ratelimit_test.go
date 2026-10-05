package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/slash4/featurevote/internal/config"
)

func TestThrottleKey(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.7":         "198.51.100.7",
		"::ffff:198.51.100.7":  "198.51.100.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"fe80::1%eth0":         "fe80::/64",
		"fe80::1%25eth1":       "fe80::/64",
		"::1":                  "::/64",
		"not-an-ip":            "not-an-ip",
		"@":                    "@",
	} {
		if got := throttleKey(in); got != want {
			t.Errorf("throttleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientIPStripsZone(t *testing.T) {
	s := &Server{cfg: config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("fe80::/10")}}}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[fe80::1%eth0]:4242"
	r.Header.Set("X-Forwarded-For", "2001:db8::7%eth9")
	if got := s.clientIP(r); got != "2001:db8::7" {
		t.Fatalf("via trusted proxy: clientIP = %q, want 2001:db8::7", got)
	}
	s.cfg.TrustedProxies = nil
	if got := s.clientIP(r); got != "fe80::1" {
		t.Fatalf("direct: clientIP = %q, want fe80::1", got)
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestSlidingWindowCapFailsClosed(t *testing.T) {
	const max = 100
	l := newSlidingWindow("test", 5, time.Minute, max, quietLog())
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < max; i++ {
		if ok, _ := l.allow(fmt.Sprintf("k%d", i), t0.Add(time.Duration(i)*time.Millisecond)); !ok {
			t.Fatalf("key %d refused below the cap", i)
		}
	}
	now := t0.Add(2 * time.Second)
	ok, retry := l.allow("new", now)
	if ok {
		t.Fatal("new key admitted while full")
	}
	if retry <= 0 || retry > time.Minute {
		t.Fatalf("retry = %v, want (0, 1m]", retry)
	}
	// Tracked keys keep their normal limit while the table is full.
	for i := 0; i < 4; i++ {
		if ok, _ := l.allow("k7", now); !ok {
			t.Fatalf("tracked key refused at hit %d", i+2)
		}
	}
	if ok, _ := l.allow("k7", now); ok {
		t.Fatal("tracked key exceeded its limit")
	}
	if n := len(l.hits); n != max {
		t.Fatalf("len(hits) = %d, want %d", n, max)
	}
	// Once the window has passed, old keys are swept and new ones get in.
	if ok, _ := l.allow("new", t0.Add(61*time.Second)); !ok {
		t.Fatal("new key refused after the window")
	}
}

func TestSlidingWindowManyKeys(t *testing.T) {
	l := newSlidingWindow("test", 5, time.Minute, limiterMaxKeys, quietLog())
	t0 := time.Unix(1_800_000_000, 0)
	admitted := 0
	for i := 0; i < 5*limiterMaxKeys; i++ {
		// A /48 worth of distinct IPv6 /64s, a few per millisecond.
		ip := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, byte(i >> 8), byte(i), 15: 1}).String()
		if ok, _ := l.allow(throttleKey(ip), t0.Add(time.Duration(i)*time.Microsecond*100)); ok {
			admitted++
		}
		if len(l.hits) > limiterMaxKeys {
			t.Fatalf("after %d keys: len(hits) = %d > cap %d", i+1, len(l.hits), limiterMaxKeys)
		}
	}
	if admitted != limiterMaxKeys {
		t.Fatalf("admitted %d distinct keys within one window, want %d", admitted, limiterMaxKeys)
	}
	// Addresses inside one /64 share a bucket.
	l = newSlidingWindow("test", 5, time.Minute, limiterMaxKeys, quietLog())
	for i := 0; i < 5; i++ {
		ip := netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, 15: byte(i + 1)}).String()
		if ok, _ := l.allow(throttleKey(ip), t0); !ok {
			t.Fatalf("hit %d refused", i+1)
		}
	}
	if ok, _ := l.allow(throttleKey("2001:db8::abcd:ef01"), t0); ok {
		t.Fatal("6th address in the same /64 got a fresh bucket")
	}
	if len(l.hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(l.hits))
	}
}

func TestSlidingWindowUndo(t *testing.T) {
	l := newSlidingWindow("test", 2, time.Minute, 10, quietLog())
	t0 := time.Unix(1_800_000_000, 0)
	for i := 0; i < 10; i++ {
		if ok, _ := l.allow("k", t0); !ok {
			t.Fatalf("undone hit %d still counted", i+1)
		}
		l.undo("k", t0)
	}
	if len(l.hits) != 0 {
		t.Fatalf("undo left %d keys behind", len(l.hits))
	}
}
