package api

import (
	"sync"
	"time"
)

// slidingWindow is an in-memory per-key sliding-window rate limiter. It is
// per-process, which is fine because FeatureVote runs one instance per product.
type slidingWindow struct {
	limit  int
	window time.Duration

	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
}

func newSlidingWindow(limit int, window time.Duration) *slidingWindow {
	return &slidingWindow{limit: limit, window: window, hits: map[string][]time.Time{}}
}

// allow records a hit for key at now if under the limit. Otherwise it returns
// false and how long until the oldest hit in the window ages out.
func (l *slidingWindow) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-l.window)
	if now.Sub(l.lastSweep) > l.window {
		for k, ts := range l.hits {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
		l.lastSweep = now
	}

	ts := l.hits[key]
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
