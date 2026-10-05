package api

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// clientIP returns the request's client address, without any IPv6 zone.
// X-Forwarded-For is only believed when the direct peer is a trusted proxy;
// then the rightmost hop that is not itself a trusted proxy is the client
// (entries to its left are client-controlled and ignored).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap().WithZone("")
	if !s.trustedProxy(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break // malformed hop: stop trusting the chain here
		}
		a = a.Unmap().WithZone("")
		if !s.trustedProxy(a) {
			return a.String()
		}
		peer = a
	}
	return peer.String()
}

// throttleKey maps a client IP to its rate-limit bucket. An IPv6 client
// usually controls a whole /64 (SLAAC, privacy addresses), so it gets one
// bucket per /64 instead of 2^64 of them. IPv4 addresses are their own key.
func throttleKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

func (s *Server) trustedProxy(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// limiterMaxKeys caps how many keys one slidingWindow tracks, so a client
// with many addresses cannot grow the map without bound.
const limiterMaxKeys = 10000

// slidingWindow is an in-memory per-key sliding-window rate limiter. It is
// per-process, which is fine because FeatureVote runs one instance per product.
//
// It tracks at most maxKeys keys. When full, it first drops expired keys; if
// it is still full, a new key is refused (fails closed) until one expires.
// Keys already tracked keep their normal limit. Evicting instead would let a
// client cycling through maxKeys+1 addresses reset its own buckets forever.
type slidingWindow struct {
	name    string
	limit   int
	window  time.Duration
	maxKeys int
	log     *slog.Logger

	mu         sync.Mutex
	hits       map[string][]time.Time
	lastSweep  time.Time
	nextFree   time.Time // earliest expiry of a tracked key, as of lastSweep
	lastFullAt time.Time // last "full" warning; logged at most once per window
}

func newSlidingWindow(name string, limit int, window time.Duration, maxKeys int, log *slog.Logger) *slidingWindow {
	if log == nil {
		log = slog.Default()
	}
	return &slidingWindow{name: name, limit: limit, window: window, maxKeys: maxKeys, log: log, hits: map[string][]time.Time{}}
}

// sweep drops keys whose newest hit has left the window. Caller holds mu.
func (l *slidingWindow) sweep(now time.Time) {
	cutoff := now.Add(-l.window)
	l.nextFree = time.Time{}
	for k, ts := range l.hits {
		if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
			delete(l.hits, k)
			continue
		}
		if free := ts[len(ts)-1].Add(l.window); l.nextFree.IsZero() || free.Before(l.nextFree) {
			l.nextFree = free
		}
	}
	l.lastSweep = now
}

// allow records a hit for key at now if under the limit. Otherwise it returns
// false and how long until the oldest hit in the window ages out (or, when
// the key table is full, roughly until a tracked key expires).
func (l *slidingWindow) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) > l.window {
		l.sweep(now)
	}
	ts, tracked := l.hits[key]
	if !tracked && len(l.hits) >= l.maxKeys {
		// Full: sweep early rather than waiting a window, but at most once a
		// second so a flood of new keys does not rescan the map per request.
		if now.Sub(l.lastSweep) >= time.Second {
			l.sweep(now)
		}
		if len(l.hits) >= l.maxKeys {
			if l.lastFullAt.IsZero() || now.Sub(l.lastFullAt) > l.window {
				l.log.Warn("rate limiter full; refusing new clients", "limiter", l.name, "keys", len(l.hits))
				l.lastFullAt = now
			}
			retry := l.nextFree.Sub(now)
			if retry <= 0 {
				retry = time.Second
			}
			return false, retry
		}
	}

	cutoff := now.Add(-l.window)
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	ts = ts[i:]
	if len(ts) >= l.limit {
		l.hits[key] = ts
		return false, ts[0].Add(l.window).Sub(now)
	}
	l.hits[key] = append(ts, now)
	return true, 0
}

// undo takes back the hit allow recorded for key at at. Callers that only
// count failures reserve a slot with allow before checking (so concurrent
// guesses cannot overshoot the limit) and give it back on success.
func (l *slidingWindow) undo(key string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := l.hits[key]
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Equal(at) {
			ts = append(ts[:i], ts[i+1:]...)
			if len(ts) == 0 {
				delete(l.hits, key)
			} else {
				l.hits[key] = ts
			}
			return
		}
	}
}
