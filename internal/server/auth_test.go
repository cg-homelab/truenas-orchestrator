package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// authedRoute is an operation that requires credentials, with the statuses that show the
// credentials were accepted.
type authedRoute struct {
	method string
	path   string
	body   any
	ok     []int
}

func authedRoutes() []authedRoute {
	return []authedRoute{
		{http.MethodGet, "/api/auth/me", nil, []int{http.StatusOK}},
		{http.MethodGet, "/api/auth/tokens", nil, []int{http.StatusOK}},
		{http.MethodPost, "/api/auth/tokens", map[string]string{"name": "x"}, []int{http.StatusCreated}},
		{http.MethodDelete, "/api/auth/tokens/nope", nil, []int{http.StatusNotFound}},
		{http.MethodGet, "/api/repo", nil, []int{http.StatusOK}},
		{http.MethodPost, "/api/repo/fetch", nil, []int{http.StatusAccepted, http.StatusConflict}},
		{http.MethodGet, "/api/jobs/nope", nil, []int{http.StatusNotFound}},
		{http.MethodGet, "/api/jobs/nope/events", nil, []int{http.StatusOK}},
		{http.MethodGet, "/api/stacks", nil, []int{http.StatusOK}},
		{http.MethodGet, "/api/stacks/ok-control", nil, []int{http.StatusOK}},
		{http.MethodGet, "/api/stacks/ok-control?reveal=true", nil, []int{http.StatusOK}},
		// Last: it ends the session.
		{http.MethodPost, "/api/auth/logout", nil, []int{http.StatusNoContent}},
	}
}

func TestAuthenticatedRoutes(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := env.login(t)
	_, token := env.createToken(t, withCookie(cookie))

	creds := []struct {
		name string
		cred cred
	}{
		{"none", func(*http.Request) {}},
		{"bad bearer", withBearer("not-a-token")},
		{"bad cookie", withCookie(&http.Cookie{Name: sessionCookie, Value: "forged"})},
		{"cookie", withCookie(cookie)},
		{"bearer", withBearer(token)},
	}
	for _, c := range creds {
		t.Run(c.name, func(t *testing.T) {
			wantOK := c.name == "cookie" || c.name == "bearer"
			for _, rt := range authedRoutes() {
				r := env.do(t, rt.method, rt.path, rt.body, c.cred)
				switch {
				case !wantOK && r.status != http.StatusUnauthorized:
					t.Errorf("%s %s with %s credentials status = %d, want 401; body %s", rt.method, rt.path, c.name, r.status, r.body)
				case wantOK && !slices.Contains(rt.ok, r.status):
					t.Errorf("%s %s with %s status = %d, want one of %v; body %s", rt.method, rt.path, c.name, r.status, rt.ok, r.body)
				}
				if got := r.header.Get("Cache-Control"); got != "no-store" {
					t.Errorf("%s %s Cache-Control = %q, want no-store", rt.method, rt.path, got)
				}
				if got := r.header.Values("Vary"); !slices.Contains(got, "Cookie") || !slices.Contains(got, "Authorization") {
					t.Errorf("%s %s Vary = %q, want Cookie and Authorization", rt.method, rt.path, got)
				}
			}
		})
	}

	// The cookie's session ended with logout; the bearer token is unaffected.
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(cookie)); r.status != http.StatusUnauthorized {
		t.Errorf("GET /api/auth/me after logout status = %d, want 401", r.status)
	}
}

func TestPublicRoutes(t *testing.T) {
	env := newEnv(t, envOptions{})
	for _, path := range []string{"/api/health", "/api/auth/status"} {
		if r := env.do(t, http.MethodGet, path, nil); r.status != http.StatusOK {
			t.Errorf("GET %s without credentials status = %d, want 200; body %s", path, r.status, r.body)
		}
	}
}

func TestSetupOnce(t *testing.T) {
	env := newEnv(t, envOptions{})

	var st AuthStatus
	decode(t, env.do(t, http.MethodGet, "/api/auth/status", nil), &st)
	if !st.NeedsSetup {
		t.Errorf("auth status before setup = %+v, want needs_setup", st)
	}

	if r := env.do(t, http.MethodPost, "/api/auth/setup", credentials(testUser, "short")); r.status != http.StatusUnprocessableEntity {
		t.Errorf("setup with a short password status = %d, want 422; body %s", r.status, r.body)
	} else if strings.Contains(r.body, `"short"`) {
		t.Errorf("setup error echoes the password: %s", r.body)
	}

	r := env.do(t, http.MethodPost, "/api/auth/setup", credentials(testUser, testPassword))
	if r.status != http.StatusCreated {
		t.Fatalf("first setup status = %d, want 201; body %s", r.status, r.body)
	}
	var u UserView
	decode(t, r, &u)
	if u.Username != testUser {
		t.Errorf("setup response = %+v, want username %s", u, testUser)
	}

	r = env.do(t, http.MethodPost, "/api/auth/setup", credentials("other", testPassword))
	if r.status != http.StatusForbidden || !strings.Contains(r.body, "setup already completed") {
		t.Errorf("second setup = %d %s, want 403 setup already completed", r.status, r.body)
	}

	decode(t, env.do(t, http.MethodGet, "/api/auth/status", nil), &st)
	if st.NeedsSetup {
		t.Errorf("auth status after setup = %+v, want needs_setup false", st)
	}

	cookie := env.login(t)
	var me MeView
	decode(t, env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(cookie)), &me)
	if me.Username != testUser || !me.CreatedAt.Equal(env.clock.Now()) {
		t.Errorf("GET /api/auth/me = %+v, want %s created at %v", me, testUser, env.clock.Now())
	}
}

func TestLoginFailuresAreIdentical(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	wrong := env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, "wrong password!"))
	unknown := env.do(t, http.MethodPost, "/api/auth/login", credentials("nobody", "wrong password!"))
	if wrong.status != http.StatusUnauthorized {
		t.Errorf("wrong password status = %d, want 401", wrong.status)
	}
	if wrong.status != unknown.status || wrong.body != unknown.body {
		t.Errorf("wrong password = %d %s, unknown user = %d %s, want identical", wrong.status, wrong.body, unknown.status, unknown.body)
	}
	if wrong.cookie != nil || unknown.cookie != nil {
		t.Errorf("failed login set a session cookie")
	}
}

func TestLoginRateLimit(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	for i := range loginBurst {
		if r := env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, "wrong password!")); r.status != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d, want 401", i+1, r.status)
		}
	}
	// Even the right password is refused while limited.
	if r := env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, testPassword)); r.status != http.StatusTooManyRequests {
		t.Fatalf("login after %d failures status = %d, want 429; body %s", loginBurst, r.status, r.body)
	}
	env.clock.Advance(loginEvery)
	if r := env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, testPassword)); r.status != http.StatusNoContent {
		t.Errorf("login %v later status = %d, want 204; body %s", loginEvery, r.status, r.body)
	}
}

func TestLoginLimiterIsPerIPAndEvicts(t *testing.T) {
	clock := newFakeClock()
	l := newLoginLimiter(clock.Now)
	for range loginBurst {
		if !l.allow("192.0.2.1:1234") {
			t.Fatal("allow within burst = false")
		}
	}
	if l.allow("192.0.2.1:9999") {
		t.Error("allow from the same IP on another port after the burst = true, want false")
	}
	if !l.allow("192.0.2.2:1234") {
		t.Error("allow from another IP = false, want true")
	}
	clock.Advance(limiterIdle)
	l.allow("192.0.2.3:1")
	l.mu.Lock()
	n := len(l.byClient)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("limiters after %v idle = %d, want 1 (only the new IP)", limiterIdle, n)
	}
}

func TestLimiterKey(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"192.0.2.1:1234", "192.0.2.1"},
		{"192.0.2.2:1234", "192.0.2.2"},
		{"[::ffff:192.0.2.1]:80", "192.0.2.1"},
		{"[2001:db8:1:2::1]:1234", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:5", "2001:db8:1:2::/64"},
		{"[2001:db8:1:3::1]:1234", "2001:db8:1:3::/64"},
		{"[fe80::1%eth0]:1234", "fe80::/64"},
		{"not-an-address", "not-an-address"},
	}
	for _, tt := range tests {
		if got := limiterKey(tt.addr); got != tt.want {
			t.Errorf("limiterKey(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestLoginLimiterSharesIPv6Slash64(t *testing.T) {
	l := newLoginLimiter(newFakeClock().Now)
	for i := range loginBurst {
		if !l.allow(fmt.Sprintf("[2001:db8::%x]:1", i+1)) {
			t.Fatalf("attempt %d within the burst refused", i+1)
		}
	}
	if l.allow("[2001:db8::ffff]:1") {
		t.Error("another address in the same /64 after the burst was allowed")
	}
	if !l.allow("[2001:db8:0:1::1]:1") {
		t.Error("an address in another /64 was refused")
	}
}

func TestLoginLimiterCap(t *testing.T) {
	clock := newFakeClock()
	l := newLoginLimiter(clock.Now)
	l.max = 3
	for i := range 3 {
		l.allow(fmt.Sprintf("192.0.2.%d:1", i+1))
		clock.Advance(time.Second)
	}
	l.allow("192.0.2.9:1")
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byClient) != 3 {
		t.Errorf("limiters = %d, want the cap of 3", len(l.byClient))
	}
	if _, ok := l.byClient["192.0.2.1"]; ok {
		t.Error("the least recently seen client was not evicted")
	}
}

func TestPasswordHasherBoundsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newPasswordHasher(hashSlots)
		var mu sync.Mutex
		cur, peak := 0, 0
		var wg sync.WaitGroup
		for range 3 * hashSlots {
			wg.Go(func() {
				err := h.do(t.Context(), func() {
					mu.Lock()
					cur++
					peak = max(peak, cur)
					mu.Unlock()
					time.Sleep(time.Second)
					mu.Lock()
					cur--
					mu.Unlock()
				})
				if err != nil {
					t.Errorf("do() error = %v", err)
				}
			})
		}
		wg.Wait()
		if peak != hashSlots {
			t.Errorf("peak concurrent hashes = %d, want %d", peak, hashSlots)
		}
	})
}

func TestPasswordHasherWaitHonoursContext(t *testing.T) {
	h := newPasswordHasher(1)
	h.slots <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.do(ctx, func() { t.Error("fn ran without a slot") }); !errors.Is(err, context.Canceled) {
		t.Errorf("do() with a cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestOtherAuthorizationSchemeFallsBackToCookie(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := env.login(t)
	basic := func(r *http.Request) { r.SetBasicAuth("proxy-user", "proxy-pass") }
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(cookie), basic); r.status != http.StatusOK {
		t.Errorf("cookie plus Basic Authorization status = %d, want 200; body %s", r.status, r.body)
	}
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, basic); r.status != http.StatusUnauthorized {
		t.Errorf("Basic Authorization alone status = %d, want 401", r.status)
	}
	lower := func(r *http.Request) { r.Header.Set("Authorization", "bearer bogus") }
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(cookie), lower); r.status != http.StatusUnauthorized {
		t.Errorf("cookie plus an invalid lowercase bearer status = %d, want 401 (bearer decides)", r.status)
	}
}

func TestCrossOriginProtection(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))
	_, token := env.createToken(t, cookie)
	site := func(v string) cred { return func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", v) } }
	tests := []struct {
		name  string
		creds []cred
		want  int
	}{
		{"same-site cookie post", []cred{cookie, site("same-site")}, http.StatusForbidden},
		{"cross-site cookie post", []cred{cookie, site("cross-site")}, http.StatusForbidden},
		{"foreign origin without fetch metadata", []cred{cookie, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }}, http.StatusForbidden},
		{"same-origin cookie post", []cred{cookie, site("same-origin")}, http.StatusCreated},
		{"bearer without browser headers", []cred{withBearer(token)}, http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := env.do(t, http.MethodPost, "/api/auth/tokens", map[string]string{"name": "x"}, tt.creds...)
			if r.status != tt.want {
				t.Errorf("POST /api/auth/tokens status = %d, want %d; body %s", r.status, tt.want, r.body)
			}
		})
	}
	// Reads stay allowed cross-site; they change nothing.
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, cookie, site("same-site")); r.status != http.StatusOK {
		t.Errorf("same-site GET status = %d, want 200", r.status)
	}
}

func TestCookieFlags(t *testing.T) {
	for _, secure := range []bool{true, false} {
		t.Run(fmt.Sprintf("secure=%v", secure), func(t *testing.T) {
			env := newEnv(t, envOptions{secureCookies: secure})
			env.setup(t)
			r := env.do(t, http.MethodPost, "/api/auth/login", credentials(testUser, testPassword))
			raw := r.header.Get("Set-Cookie")
			c := r.cookie
			if c == nil {
				t.Fatalf("login set no %s cookie; Set-Cookie %q", sessionCookie, raw)
			}
			if c.Secure != secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
				t.Errorf("cookie %q: Secure %v HttpOnly %v SameSite %v Path %q, want Secure %v HttpOnly SameSite=Lax Path=/",
					raw, c.Secure, c.HttpOnly, c.SameSite, c.Path, secure)
			}
			if len(c.Value) != 43 { // 32 bytes, base64url without padding
				t.Errorf("session token length = %d, want 43", len(c.Value))
			}

			out := env.do(t, http.MethodPost, "/api/auth/logout", nil, withCookie(c))
			if out.cookie == nil || out.cookie.MaxAge >= 0 || out.cookie.Secure != secure {
				t.Errorf("logout Set-Cookie = %q, want an expiring cookie with Secure %v", out.header.Get("Set-Cookie"), secure)
			}
		})
	}
}

func TestNewSessionPerLogin(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	a, b := env.login(t), env.login(t)
	if a.Value == b.Value {
		t.Error("two logins returned the same session token")
	}
	for _, c := range []*http.Cookie{a, b} {
		if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(c)); r.status != http.StatusOK {
			t.Errorf("GET /api/auth/me status = %d, want 200", r.status)
		}
	}
	// The database holds only the hash.
	if _, _, err := env.store.SessionByID(t.Context(), a.Value); err == nil {
		t.Error("session found by its raw token; the database must store only the hash")
	}
	if _, _, err := env.store.SessionByID(t.Context(), hashToken(a.Value)); err != nil {
		t.Errorf("SessionByID(hash) error = %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	const idle, lifetime = time.Hour, 3 * time.Hour
	tests := []struct {
		name  string
		steps []time.Duration // clock advances, each followed by a request
		want  []int
	}{
		{"active session stays valid", []time.Duration{30 * time.Minute, 50 * time.Minute, 50 * time.Minute}, []int{200, 200, 200}},
		{"idle timeout", []time.Duration{idle - time.Minute, 2 * idle}, []int{200, 401}},
		{"idle timeout exactly", []time.Duration{idle}, []int{401}},
		{"absolute lifetime despite activity", []time.Duration{50 * time.Minute, 50 * time.Minute, 50 * time.Minute, 30 * time.Minute}, []int{200, 200, 200, 401}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t, envOptions{idle: idle, lifetime: lifetime})
			env.setup(t)
			cookie := env.login(t)
			for i, d := range tt.steps {
				env.clock.Advance(d)
				r := env.do(t, http.MethodGet, "/api/auth/me", nil, withCookie(cookie))
				if r.status != tt.want[i] {
					t.Fatalf("request %d after +%v status = %d, want %d", i+1, d, r.status, tt.want[i])
				}
			}
		})
	}
}

func TestTokens(t *testing.T) {
	env := newEnv(t, envOptions{})
	env.setup(t)
	cookie := withCookie(env.login(t))

	var list TokenList
	decode(t, env.do(t, http.MethodGet, "/api/auth/tokens", nil, cookie), &list)
	if list.Tokens == nil || len(list.Tokens) != 0 {
		t.Fatalf("token list before create = %#v, want an empty, non-null list", list.Tokens)
	}

	id, token := env.createToken(t, cookie)
	decode(t, env.do(t, http.MethodGet, "/api/auth/tokens", nil, cookie), &list)
	if len(list.Tokens) != 1 || list.Tokens[0].ID != id || list.Tokens[0].Name != "cli" || list.Tokens[0].LastUsedAt != nil {
		t.Fatalf("token list = %+v, want one unused token %s named cli", list.Tokens, id)
	}
	if r := env.do(t, http.MethodGet, "/api/auth/tokens", nil, cookie); strings.Contains(r.body, token) {
		t.Errorf("token list contains the raw token")
	}

	env.clock.Advance(time.Minute)
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withBearer(token)); r.status != http.StatusOK {
		t.Fatalf("GET /api/auth/me with bearer status = %d, want 200", r.status)
	}
	decode(t, env.do(t, http.MethodGet, "/api/auth/tokens", nil, cookie), &list)
	if lu := list.Tokens[0].LastUsedAt; lu == nil || !lu.Equal(env.clock.Now()) {
		t.Errorf("last_used_at after use = %v, want %v", lu, env.clock.Now())
	}

	if r := env.do(t, http.MethodDelete, "/api/auth/tokens/"+id, nil, cookie); r.status != http.StatusNoContent {
		t.Fatalf("DELETE token status = %d, want 204; body %s", r.status, r.body)
	}
	if r := env.do(t, http.MethodGet, "/api/auth/me", nil, withBearer(token)); r.status != http.StatusUnauthorized {
		t.Errorf("revoked token status = %d, want 401", r.status)
	}
	if r := env.do(t, http.MethodDelete, "/api/auth/tokens/"+id, nil, cookie); r.status != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", r.status)
	}
}

func TestSecretFieldRedacts(t *testing.T) {
	var c Credentials
	if err := json.Unmarshal([]byte(`{"username":"u","password":"hunter2hunter2"}`), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := c.Password.Reveal(); got != "hunter2hunter2" {
		t.Errorf("Password.Reveal() = %q, want hunter2hunter2", got)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var logs syncBuffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("credentials", "creds", c, "pw", c.Password)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", c), "%+v": fmt.Sprintf("%+v", c), "%#v": fmt.Sprintf("%#v", c),
		"json": string(b), "slog": logs.String(),
	} {
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s output leaks the password: %s", name, out)
		}
	}
	if err := json.Unmarshal([]byte(`{"username":"u","password":42}`), &c); err == nil || strings.Contains(err.Error(), "42") {
		t.Errorf("Unmarshal(non-string password) error = %v, want an error without the value", err)
	}
}

func TestValidationErrorsDoNotEchoInput(t *testing.T) {
	env := newEnv(t, envOptions{})
	const pw = "pw-SENTINEL-value"
	tests := []struct {
		name        string
		contentType string
		body        string
	}{
		{"missing username", "application/json", `{"password":"` + pw + `"}`},
		{"unexpected property", "application/json", `{"username":"u","password":"` + pw + `","remember":"` + pw + `"}`},
		{"malformed json", "application/json", `{"username":"u","password":"` + pw + `"`},
		{"text/plain body", "text/plain", `{"username":"u","password":"` + pw + `"}`},
		{"wrong type", "application/json", `{"username":["` + pw + `"],"password":"` + pw + `"}`},
		{"password wrong type", "application/json", `{"username":"u","password":["` + pw + `"]}`},
		{"username too long", "application/json", `{"username":"` + strings.Repeat(pw, 10) + `","password":"x"}`},
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/setup"} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.srv.URL+path, strings.NewReader(tt.body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", tt.contentType)
				resp, err := env.srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode < 400 {
					t.Errorf("POST %s status = %d, want an error", path, resp.StatusCode)
				}
				if strings.Contains(string(b), "SENTINEL") {
					t.Errorf("POST %s response echoes the input: %s", path, b)
				}
			})
		}
	}
}
