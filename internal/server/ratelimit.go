package server

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/alexedwards/argon2id"
	"golang.org/x/time/rate"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

const (
	// loginEvery and loginBurst allow five login attempts per client at once, then one every 12 s.
	loginEvery = 12 * time.Second
	loginBurst = 5
	// limiterIdle is how long a client must be quiet before its limiter is dropped. By then its
	// bucket is full again, so dropping it changes nothing.
	limiterIdle = loginBurst * loginEvery
	// limiterSweep is how often idle limiters are dropped.
	limiterSweep = time.Minute
	// maxLimiters caps the limiter map; at the cap the least recently seen client is dropped.
	maxLimiters = 10_000
	// hashSlots bounds concurrent argon2id work. Each hash takes 64 MiB and every core.
	hashSlots = 4
)

// loginLimiter is a token bucket for login attempts per client.
//
// A client is the host of the connection's remote address: one IPv4 address, or one IPv6 /64,
// since a single host usually controls a whole /64. No proxy header is trusted: behind Traefik
// every client shares the proxy's address, so the limit is effectively global there. Trusting
// X-Forwarded-For needs a configured proxy allowlist, which M2 does not have.
type loginLimiter struct {
	now func() time.Time
	max int

	mu        sync.Mutex
	byClient  map[string]*limiterEntry
	lastSweep time.Time
}

type limiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, max: maxLimiters, byClient: map[string]*limiterEntry{}}
}

// limiterKey returns the client a remote address belongs to: the IPv4 address, or the IPv6 /64.
func limiterKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// allow consumes one attempt for the client at remoteAddr and reports whether it may proceed.
func (l *loginLimiter) allow(remoteAddr string) bool {
	key := limiterKey(remoteAddr)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) >= limiterSweep {
		for k, e := range l.byClient {
			if now.Sub(e.seen) >= limiterIdle {
				delete(l.byClient, k)
			}
		}
		l.lastSweep = now
	}
	e, ok := l.byClient[key]
	if !ok {
		if len(l.byClient) >= l.max {
			l.evictOldest()
		}
		e = &limiterEntry{lim: rate.NewLimiter(rate.Every(loginEvery), loginBurst)}
		l.byClient[key] = e
	}
	e.seen = now
	return e.lim.AllowN(now, 1)
}

// evictOldest drops the least recently seen client. The caller holds l.mu.
func (l *loginLimiter) evictOldest() {
	var oldest string
	var at time.Time
	for k, e := range l.byClient {
		if oldest == "" || e.seen.Before(at) {
			oldest, at = k, e.seen
		}
	}
	delete(l.byClient, oldest)
}

// passwordHasher runs argon2id with at most hashSlots hashes at a time, so a burst of logins
// cannot exhaust memory or CPU.
type passwordHasher struct {
	slots chan struct{}
}

func newPasswordHasher(n int) *passwordHasher {
	return &passwordHasher{slots: make(chan struct{}, n)}
}

// do runs fn in a slot, waiting for one unless ctx ends first.
func (h *passwordHasher) do(ctx context.Context, fn func()) error {
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("wait for hash slot: %w", ctx.Err())
	}
	defer func() { <-h.slots }()
	fn()
	return nil
}

// hash returns the argon2id PHC string for pw.
func (h *passwordHasher) hash(ctx context.Context, pw homelab.Secret) (string, error) {
	var out string
	var herr error
	if err := h.do(ctx, func() { out, herr = argon2id.CreateHash(pw.Reveal(), argon2id.DefaultParams) }); err != nil {
		return "", err
	}
	if herr != nil {
		return "", fmt.Errorf("hash password: %w", herr)
	}
	return out, nil
}

// compare reports whether pw matches the argon2id PHC string phc.
func (h *passwordHasher) compare(ctx context.Context, pw homelab.Secret, phc string) (bool, error) {
	var ok bool
	var cerr error
	if err := h.do(ctx, func() { ok, cerr = argon2id.ComparePasswordAndHash(pw.Reveal(), phc) }); err != nil {
		return false, err
	}
	if cerr != nil {
		return false, fmt.Errorf("compare password: %w", cerr)
	}
	return ok, nil
}
