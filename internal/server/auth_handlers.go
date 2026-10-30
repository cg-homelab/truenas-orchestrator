package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/argon2id"
	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

const (
	// minPasswordLength is the shortest password setup accepts, in characters.
	minPasswordLength = 12
	// maxPasswordLength bounds the work argon2id does for one request.
	maxPasswordLength = 1024
	// msgBadLogin is the one 401 body for an unknown user and a wrong password alike.
	msgBadLogin = "invalid username or password"
)

// Credentials is a username and password.
type Credentials struct {
	Username string      `json:"username" minLength:"1" maxLength:"64"`
	Password secretField `json:"password"`
}

// AuthStatus tells the UI whether the first account still has to be created.
type AuthStatus struct {
	NeedsSetup bool `json:"needs_setup"`
}

// UserView names a user.
type UserView struct {
	Username string `json:"username"`
}

// MeView is the authenticated user.
type MeView struct {
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// GetAuthStatusOutput is the get-auth-status response.
type GetAuthStatusOutput struct {
	Body AuthStatus
}

var opGetAuthStatus = huma.Operation{
	OperationID: "get-auth-status",
	Method:      http.MethodGet,
	Path:        "/api/auth/status",
	Summary:     "Report whether first-run setup is needed",
	Tags:        []string{"auth"},
}

func handleGetAuthStatus(
	logger *slog.Logger, store *sqlite.Store,
) func(context.Context, *struct{}) (*GetAuthStatusOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetAuthStatusOutput, error) {
		n, err := store.CountUsers(ctx)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &GetAuthStatusOutput{Body: AuthStatus{NeedsSetup: n == 0}}, nil
	}
}

// AuthSetupInput is the auth-setup request.
type AuthSetupInput struct {
	Body Credentials
}

// AuthSetupOutput is the auth-setup response.
type AuthSetupOutput struct {
	Body UserView
}

var opAuthSetup = huma.Operation{
	OperationID:   "auth-setup",
	Method:        http.MethodPost,
	Path:          "/api/auth/setup",
	Summary:       "Create the first account; allowed only while no account exists",
	Tags:          []string{"auth"},
	DefaultStatus: http.StatusCreated,
	Errors:        []int{http.StatusForbidden, http.StatusUnprocessableEntity},
}

// checkPassword enforces the password policy. Its messages never include the password.
func checkPassword(pw homelab.Secret) error {
	switch n := utf8.RuneCountInString(pw.Reveal()); {
	case n < minPasswordLength:
		return huma.Error422UnprocessableEntity("password must be at least 12 characters")
	case n > maxPasswordLength:
		return huma.Error422UnprocessableEntity("password must be at most 1024 characters")
	}
	return nil
}

func handleAuthSetup(
	logger *slog.Logger, store *sqlite.Store, hasher *passwordHasher, now func() time.Time,
) func(context.Context, *AuthSetupInput) (*AuthSetupOutput, error) {
	return func(ctx context.Context, in *AuthSetupInput) (*AuthSetupOutput, error) {
		if n, err := store.CountUsers(ctx); err != nil {
			return nil, apiError(ctx, logger, err)
		} else if n > 0 {
			return nil, huma.Error403Forbidden("setup already completed")
		}
		if err := checkPassword(in.Body.Password.Secret); err != nil {
			return nil, err
		}
		hash, err := hasher.hash(ctx, in.Body.Password.Secret)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		u, err := store.CreateFirstUser(ctx, in.Body.Username, hash, now())
		if errors.Is(err, homelab.ErrForbidden) {
			return nil, huma.Error403Forbidden("setup already completed")
		}
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		logger.InfoContext(ctx, "first user created", "user", u.Username)
		return &AuthSetupOutput{Body: UserView{Username: u.Username}}, nil
	}
}

// AuthLoginInput is the auth-login request.
type AuthLoginInput struct {
	Body       Credentials
	remoteAddr string
}

// Resolve implements huma.Resolver to capture the connection's address for rate limiting.
func (in *AuthLoginInput) Resolve(ctx huma.Context) []error {
	in.remoteAddr = ctx.RemoteAddr()
	return nil
}

// AuthLoginOutput is the auth-login response: the session cookie and no body.
type AuthLoginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

var opAuthLogin = huma.Operation{
	OperationID:   "auth-login",
	Method:        http.MethodPost,
	Path:          "/api/auth/login",
	Summary:       "Log in and receive a session cookie",
	Description:   "Unknown users and wrong passwords get the same 401. Attempts are rate-limited per client address (429).",
	Tags:          []string{"auth"},
	DefaultStatus: http.StatusNoContent,
	Errors:        []int{http.StatusUnauthorized, http.StatusTooManyRequests},
}

func handleAuthLogin(
	logger *slog.Logger, store *sqlite.Store, auth *authenticator, limiter *loginLimiter, hasher *passwordHasher,
) func(context.Context, *AuthLoginInput) (*AuthLoginOutput, error) {
	// dummyHash is compared against for unknown users, so they cost the same time as a wrong
	// password. Built once, on first use (NewAPI must not do work), outside the hash slots: it
	// cannot be triggered more than once per process.
	dummyHash := sync.OnceValues(func() (string, error) {
		return argon2id.CreateHash("timing-equaliser", argon2id.DefaultParams)
	})
	return func(ctx context.Context, in *AuthLoginInput) (*AuthLoginOutput, error) {
		if !limiter.allow(in.remoteAddr) {
			logger.WarnContext(ctx, "login rate limited")
			return nil, huma.Error429TooManyRequests("too many login attempts; try again later")
		}
		pw := in.Body.Password.Secret
		if len(pw.Reveal()) > maxPasswordLength*utf8.UTFMax {
			return nil, huma.Error401Unauthorized(msgBadLogin)
		}
		u, err := store.UserByUsername(ctx, in.Body.Username)
		if errors.Is(err, homelab.ErrNotFound) {
			hash, herr := dummyHash()
			if herr != nil {
				return nil, apiError(ctx, logger, herr)
			}
			if _, err := hasher.compare(ctx, pw, hash); err != nil {
				return nil, apiError(ctx, logger, err)
			}
			logger.InfoContext(ctx, "login failed")
			return nil, huma.Error401Unauthorized(msgBadLogin)
		}
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		ok, err := hasher.compare(ctx, pw, u.PasswordHash)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		if !ok {
			logger.InfoContext(ctx, "login failed")
			return nil, huma.Error401Unauthorized(msgBadLogin)
		}

		now := auth.now()
		if _, err := store.DeleteExpiredSessions(ctx, now, now.Add(-auth.idle)); err != nil {
			logger.ErrorContext(ctx, "delete expired sessions", "error", err)
		}
		raw, id := newToken()
		sess := sqlite.Session{
			ID: id, UserID: u.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(auth.lifetime),
		}
		if err := store.CreateSession(ctx, sess); err != nil {
			return nil, apiError(ctx, logger, err)
		}
		logger.InfoContext(ctx, "login succeeded", "user", u.Username)
		return &AuthLoginOutput{SetCookie: auth.cookie(raw, sess.ExpiresAt)}, nil
	}
}

// AuthLogoutOutput is the auth-logout response: a cookie that clears the session cookie.
type AuthLogoutOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
}

var opAuthLogout = huma.Operation{
	OperationID:   "auth-logout",
	Method:        http.MethodPost,
	Path:          "/api/auth/logout",
	Summary:       "Log out, deleting the current session",
	Tags:          []string{"auth"},
	Security:      authenticated(),
	DefaultStatus: http.StatusNoContent,
}

func handleAuthLogout(
	logger *slog.Logger, store *sqlite.Store, auth *authenticator,
) func(context.Context, *struct{}) (*AuthLogoutOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*AuthLogoutOutput, error) {
		u, _ := userFrom(ctx)
		if u.SessionID != "" {
			if err := store.DeleteSession(ctx, u.SessionID); err != nil {
				return nil, apiError(ctx, logger, err)
			}
		}
		return &AuthLogoutOutput{SetCookie: auth.clearCookie()}, nil
	}
}

// GetMeOutput is the get-me response.
type GetMeOutput struct {
	Body MeView
}

var opGetMe = huma.Operation{
	OperationID: "get-me",
	Method:      http.MethodGet,
	Path:        "/api/auth/me",
	Summary:     "Get the authenticated user",
	Tags:        []string{"auth"},
	Security:    authenticated(),
}

func handleGetMe() func(context.Context, *struct{}) (*GetMeOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetMeOutput, error) {
		u, _ := userFrom(ctx)
		return &GetMeOutput{Body: MeView{Username: u.Username, CreatedAt: u.CreatedAt.UTC()}}, nil
	}
}

// TokenView describes an API token without the token itself.
type TokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at" doc:"null when the token has never been used"`
}

// TokenList is the list-tokens response body.
type TokenList struct {
	Tokens []TokenView `json:"tokens"`
}

// ListTokensOutput is the list-tokens response.
type ListTokensOutput struct {
	Body TokenList
}

var opListTokens = huma.Operation{
	OperationID: "list-tokens",
	Method:      http.MethodGet,
	Path:        "/api/auth/tokens",
	Summary:     "List the authenticated user's API tokens",
	Tags:        []string{"auth"},
	Security:    authenticated(),
}

func handleListTokens(
	logger *slog.Logger, store *sqlite.Store,
) func(context.Context, *struct{}) (*ListTokensOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*ListTokensOutput, error) {
		u, _ := userFrom(ctx)
		ts, err := store.APITokens(ctx, u.ID)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		out := &ListTokensOutput{Body: TokenList{Tokens: make([]TokenView, 0, len(ts))}}
		for _, t := range ts {
			out.Body.Tokens = append(out.Body.Tokens, TokenView{
				ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt.UTC(), LastUsedAt: optionalTime(t.LastUsedAt),
			})
		}
		return out, nil
	}
}

// CreateTokenRequest names a new API token.
type CreateTokenRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"100" doc:"What the token is for, e.g. laptop CLI"`
}

// CreatedToken is a new API token. Token is shown only in this response.
type CreatedToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token" doc:"The bearer token. It is shown once and cannot be retrieved again"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateTokenInput is the create-token request.
type CreateTokenInput struct {
	Body CreateTokenRequest
}

// CreateTokenOutput is the create-token response.
type CreateTokenOutput struct {
	Body CreatedToken
}

var opCreateToken = huma.Operation{
	OperationID:   "create-token",
	Method:        http.MethodPost,
	Path:          "/api/auth/tokens",
	Summary:       "Create an API token for the authenticated user",
	Tags:          []string{"auth"},
	Security:      authenticated(),
	DefaultStatus: http.StatusCreated,
}

func handleCreateToken(
	logger *slog.Logger, store *sqlite.Store, now func() time.Time,
) func(context.Context, *CreateTokenInput) (*CreateTokenOutput, error) {
	return func(ctx context.Context, in *CreateTokenInput) (*CreateTokenOutput, error) {
		u, _ := userFrom(ctx)
		raw, hash := newToken()
		t, err := store.CreateAPIToken(ctx, u.ID, newID(), in.Body.Name, hash, now())
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		logger.InfoContext(ctx, "api token created", "user", u.Username, "token_id", t.ID)
		return &CreateTokenOutput{Body: CreatedToken{
			ID:        t.ID,
			Name:      t.Name,
			Token:     raw.Reveal(), // shown once, at creation, to the user who created it
			CreatedAt: t.CreatedAt.UTC(),
		}}, nil
	}
}

// DeleteTokenInput is the delete-token request.
type DeleteTokenInput struct {
	ID string `path:"id" maxLength:"64"`
}

var opDeleteToken = huma.Operation{
	OperationID:   "delete-token",
	Method:        http.MethodDelete,
	Path:          "/api/auth/tokens/{id}",
	Summary:       "Revoke one of the authenticated user's API tokens",
	Tags:          []string{"auth"},
	Security:      authenticated(),
	DefaultStatus: http.StatusNoContent,
	Errors:        []int{http.StatusNotFound},
}

func handleDeleteToken(
	logger *slog.Logger, store *sqlite.Store,
) func(context.Context, *DeleteTokenInput) (*struct{}, error) {
	return func(ctx context.Context, in *DeleteTokenInput) (*struct{}, error) {
		u, _ := userFrom(ctx)
		if err := store.DeleteAPIToken(ctx, u.ID, in.ID); err != nil {
			return nil, apiError(ctx, logger, err)
		}
		logger.InfoContext(ctx, "api token revoked", "user", u.Username, "token_id", in.ID)
		return nil, nil
	}
}
