package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

const (
	// sessionCookie holds the raw session token. The database has only its hash.
	sessionCookie = "orch_session"
	// touchInterval is how often a session's last_seen_at, or a token's last_used_at, is
	// written: at most once per interval, so a busy page does not write on every request.
	touchInterval = time.Minute
	// tokenBytes is the size of session and API tokens before encoding.
	tokenBytes = 32

	// Security scheme names in the OpenAPI document.
	schemeCookie = "cookieAuth"
	schemeBearer = "bearerAuth"
)

// Default session timeouts, used when Deps leaves them zero.
const (
	defaultSessionIdle     = 24 * time.Hour
	defaultSessionLifetime = 7 * 24 * time.Hour
)

// authenticated is the Security of every operation that needs a logged-in user: a session
// cookie or a bearer token.
func authenticated() []map[string][]string {
	return []map[string][]string{{schemeCookie: {}}, {schemeBearer: {}}}
}

// securitySchemes documents the two ways to authenticate.
func securitySchemes() map[string]*huma.SecurityScheme {
	return map[string]*huma.SecurityScheme{
		schemeCookie: {
			Type:        "apiKey",
			In:          "cookie",
			Name:        sessionCookie,
			Description: "Session cookie set by auth-login.",
		},
		schemeBearer: {
			Type:        "http",
			Scheme:      "bearer",
			Description: "API token from create-token.",
		},
	}
}

// newToken returns a random token and the SHA-256 hex digest that the database stores.
func newToken() (raw homelab.Secret, hash string) {
	s := base64.RawURLEncoding.EncodeToString(randomBytes(tokenBytes))
	return homelab.NewSecret(s), hashToken(s)
}

// hashToken returns the SHA-256 hex digest of a raw token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// authUser is the authenticated caller, stored in the request context.
type authUser struct {
	ID        int64
	Username  string
	CreatedAt time.Time
	// SessionID is the hashed id of the session used, empty for a bearer token.
	SessionID string
}

// userKey is the context key for the authUser.
type userKey struct{}

// userFrom returns the authenticated caller. It is set for every operation that declares
// Security.
func userFrom(ctx context.Context) (authUser, bool) {
	u, ok := ctx.Value(userKey{}).(authUser)
	return u, ok
}

// authenticator resolves the caller from a session cookie or a bearer token.
type authenticator struct {
	store    *sqlite.Store
	logger   *slog.Logger
	now      func() time.Time
	idle     time.Duration
	lifetime time.Duration
	secure   bool
}

// errNoCredentials means the request carries no, unknown or expired credentials.
var errNoCredentials = fmt.Errorf("no valid credentials: %w", homelab.ErrUnauthenticated)

// middleware is the huma middleware that enforces each operation's declared Security. Public
// operations declare none and pass through. It must be registered before any operation.
func (a *authenticator) middleware(api huma.API) func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if len(ctx.Operation().Security) == 0 {
			next(ctx)
			return
		}
		// Authenticated responses are per user and may carry revealed values: never cache.
		ctx.SetHeader("Cache-Control", "no-store")
		ctx.AppendHeader("Vary", "Cookie")
		ctx.AppendHeader("Vary", "Authorization")
		u, err := a.resolve(ctx)
		if err != nil {
			if !errors.Is(err, homelab.ErrUnauthenticated) {
				a.logger.ErrorContext(ctx.Context(), "authenticate", "error", err)
				_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "internal error")
				return
			}
			_ = huma.WriteErr(api, ctx, http.StatusUnauthorized, "authentication required")
			return
		}
		next(huma.WithValue(ctx, userKey{}, u))
	}
}

// resolve authenticates the request. A request with an `Authorization: Bearer` header is
// judged by the token alone; otherwise, including under another scheme such as the Basic
// credentials a proxy may forward, by the session cookie.
func (a *authenticator) resolve(ctx huma.Context) (authUser, error) {
	if scheme, raw, _ := strings.Cut(ctx.Header("Authorization"), " "); strings.EqualFold(scheme, "Bearer") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return authUser{}, errNoCredentials
		}
		return a.bearer(ctx.Context(), homelab.NewSecret(raw))
	}
	c, err := huma.ReadCookie(ctx, sessionCookie)
	if err != nil || c.Value == "" {
		return authUser{}, errNoCredentials
	}
	return a.session(ctx.Context(), homelab.NewSecret(c.Value))
}

// session validates a session cookie against the database: unknown, idle and expired sessions
// fail, and an expired one is deleted.
func (a *authenticator) session(ctx context.Context, raw homelab.Secret) (authUser, error) {
	id := hashToken(raw.Reveal())
	sess, u, err := a.store.SessionByID(ctx, id)
	if errors.Is(err, homelab.ErrNotFound) {
		return authUser{}, errNoCredentials
	}
	if err != nil {
		return authUser{}, err
	}
	now := a.now()
	if !now.Before(sess.ExpiresAt) || !now.Before(sess.CreatedAt.Add(a.lifetime)) || !now.Before(sess.LastSeenAt.Add(a.idle)) {
		if err := a.store.DeleteSession(ctx, id); err != nil {
			a.logger.ErrorContext(ctx, "delete expired session", "error", err)
		}
		return authUser{}, errNoCredentials
	}
	if now.Sub(sess.LastSeenAt) >= touchInterval {
		if err := a.store.TouchSession(ctx, id, now); err != nil {
			return authUser{}, err
		}
	}
	return authUser{ID: u.ID, Username: u.Username, CreatedAt: u.CreatedAt, SessionID: id}, nil
}

// bearer validates an API token and records its use.
func (a *authenticator) bearer(ctx context.Context, raw homelab.Secret) (authUser, error) {
	t, u, err := a.store.APITokenByHash(ctx, hashToken(raw.Reveal()))
	if errors.Is(err, homelab.ErrNotFound) {
		return authUser{}, errNoCredentials
	}
	if err != nil {
		return authUser{}, err
	}
	if now := a.now(); now.Sub(t.LastUsedAt) >= touchInterval {
		if err := a.store.TouchAPIToken(ctx, t.ID, now); err != nil {
			return authUser{}, err
		}
	}
	return authUser{ID: u.ID, Username: u.Username, CreatedAt: u.CreatedAt}, nil
}

// cookie returns the session cookie carrying raw, valid until expires.
func (a *authenticator) cookie(raw homelab.Secret, expires time.Time) http.Cookie {
	//nolint:gosec // G124: Secure follows ORCH_SECURE_COOKIES (TLS ends at the proxy); TestCookieFlags asserts it
	return http.Cookie{
		Name:     sessionCookie,
		Value:    raw.Reveal(), // the cookie is the one place the raw session token goes
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// clearCookie returns a cookie that makes the browser drop the session cookie.
func (a *authenticator) clearCookie() http.Cookie {
	//nolint:gosec // G124: Secure follows ORCH_SECURE_COOKIES (TLS ends at the proxy); TestCookieFlags asserts it
	return http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// secretField is a JSON string decoded straight into a homelab.Secret, so a password never
// exists as a plain string field of a request struct. It encodes, prints and logs redacted.
type secretField struct {
	homelab.Secret
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *secretField) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return errors.New("expected a string") // the default error would quote the input
	}
	s.Secret = homelab.NewSecret(v)
	return nil
}

// Schema implements huma.SchemaProvider: the field is a plain string in the API.
func (secretField) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Format: "password"}
}
