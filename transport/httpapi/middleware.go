package httpapi

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baby-hero-404/aicli/conversation"
)

// originAllowed applies the origin policy. An Origin header, when present, must be allowed on
// every method. Without one: a POST is refused unless allowNoOrigin (browsers always send
// Origin on POST, so a missing one means a non-browser client); a GET is allowed because
// browsers omit Origin on same-origin GETs, unless Sec-Fetch-Site says cross-site.
//
// Without a configured allowlist, an Origin is allowed when its host equals the request's.
func originAllowed(r *http.Request, allowed []string, allowNoOrigin bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return allowNoOrigin
		}
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	if len(allowed) > 0 {
		return slices.Contains(allowed, origin)
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// window is a sliding-window rate limiter per principal.
type window struct {
	mu    sync.Mutex
	limit int
	every time.Duration
	hits  map[conversation.Principal][]time.Time
	calls int
}

func newWindow(limit int, every time.Duration) *window {
	return &window{limit: limit, every: every, hits: map[conversation.Principal][]time.Time{}}
}

// allow records a hit and reports whether it is within the limit; when it is not, retryAfter
// says how long until a slot frees up.
func (w *window) allow(p conversation.Principal, now time.Time) (ok bool, retryAfter time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.calls++; w.calls%1024 == 0 {
		w.sweep(now)
	}
	recent := w.prune(w.hits[p], now)
	if len(recent) >= w.limit {
		w.hits[p] = recent
		return false, recent[0].Add(w.every).Sub(now)
	}
	w.hits[p] = append(recent, now)
	return true, 0
}

func (w *window) prune(hits []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(hits) && now.Sub(hits[i]) >= w.every {
		i++
	}
	return hits[i:]
}

// sweep drops principals with no recent hits so the map cannot grow without bound.
func (w *window) sweep(now time.Time) {
	for p, hits := range w.hits {
		if recent := w.prune(hits, now); len(recent) == 0 {
			delete(w.hits, p)
		} else {
			w.hits[p] = recent
		}
	}
}

// counter limits concurrent things (open streams) per principal.
type counter struct {
	mu  sync.Mutex
	max int
	n   map[conversation.Principal]int
}

func newCounter(max int) *counter {
	return &counter{max: max, n: map[conversation.Principal]int{}}
}

func (c *counter) acquire(p conversation.Principal) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[p] >= c.max {
		return false
	}
	c.n[p]++
	return true
}

func (c *counter) release(p conversation.Principal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[p]--; c.n[p] <= 0 {
		delete(c.n, p)
	}
}
