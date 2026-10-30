package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

// User is an orchestrator account.
type User struct {
	ID       int64
	Username string
	// PasswordHash is the argon2id PHC string. It never leaves the server.
	PasswordHash string
	CreatedAt    time.Time
}

// Session is a browser login. ID is the SHA-256 hex of the raw session token, which only the
// cookie holds.
type Session struct {
	ID         string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	// ExpiresAt is the absolute end of the session's lifetime.
	ExpiresAt time.Time
}

// APIToken is a long-lived bearer token. The token itself is never stored; only its hash is.
type APIToken struct {
	ID        string
	UserID    int64
	Name      string
	CreatedAt time.Time
	// LastUsedAt is zero when the token has never been used.
	LastUsedAt time.Time
}

// toMillis and fromMillis convert between time.Time and the Unix-millisecond columns.
func toMillis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func fromNullMillis(ms sql.NullInt64) time.Time {
	if !ms.Valid {
		return time.Time{}
	}
	return fromMillis(ms.Int64)
}

func newUser(u db.User) User {
	return User{ID: u.ID, Username: u.Username, PasswordHash: u.PasswordHash, CreatedAt: fromMillis(u.CreatedAt)}
}

// notFound translates sql.ErrNoRows into homelab.ErrNotFound, wrapping every error with what.
func notFound(what string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, homelab.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	n, err := s.q.CountUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CreateFirstUser creates the initial account. It fails with homelab.ErrForbidden once any
// account exists; the check and the insert share one transaction, so two concurrent calls
// cannot both succeed.
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string, now time.Time) (User, error) {
	var u User
	err := s.withTx(ctx, func(q *db.Queries) error {
		n, err := q.CountUsers(ctx)
		if err != nil {
			return fmt.Errorf("count users: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("create first user: %w", homelab.ErrForbidden)
		}
		row, err := q.CreateUser(ctx, db.CreateUserParams{
			Username: username, PasswordHash: passwordHash, CreatedAt: toMillis(now),
		})
		if err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		u = newUser(row)
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// UserByUsername returns the account named username, or homelab.ErrNotFound.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	row, err := s.q.GetUserByUsername(ctx, username)
	if err != nil {
		return User{}, notFound("get user", err)
	}
	return newUser(row), nil
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:         sess.ID,
		UserID:     sess.UserID,
		CreatedAt:  toMillis(sess.CreatedAt),
		LastSeenAt: toMillis(sess.LastSeenAt),
		ExpiresAt:  toMillis(sess.ExpiresAt),
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionByID returns the session whose hashed id is id, with its user, or homelab.ErrNotFound.
// It does not check expiry; the caller decides with its configured timeouts.
func (s *Store) SessionByID(ctx context.Context, id string) (Session, User, error) {
	row, err := s.q.GetSession(ctx, id)
	if err != nil {
		return Session{}, User{}, notFound("get session", err)
	}
	sess := Session{
		ID:         row.ID,
		UserID:     row.UserID,
		CreatedAt:  fromMillis(row.CreatedAt),
		LastSeenAt: fromMillis(row.LastSeenAt),
		ExpiresAt:  fromMillis(row.ExpiresAt),
	}
	u := User{ID: row.UserID, Username: row.Username, PasswordHash: row.PasswordHash, CreatedAt: fromMillis(row.UserCreatedAt)}
	return sess, u, nil
}

// TouchSession records activity on the session with hashed id id.
func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	if err := s.q.TouchSession(ctx, db.TouchSessionParams{LastSeenAt: toMillis(now), ID: id}); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteSession removes the session with hashed id id. Deleting an absent session is not an
// error.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if err := s.q.DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes every session past its absolute expiry at now, or idle since
// before idleCutoff. It returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now, idleCutoff time.Time) (int64, error) {
	n, err := s.q.DeleteExpiredSessions(ctx, db.DeleteExpiredSessionsParams{
		Now: toMillis(now), IdleCutoff: toMillis(idleCutoff),
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}

// CreateAPIToken stores a token for userID under the public id and name; tokenHash is the
// SHA-256 hex of the raw token.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, id, name, tokenHash string, now time.Time) (APIToken, error) {
	row, err := s.q.CreateAPIToken(ctx, db.CreateAPITokenParams{
		ID: id, UserID: userID, Name: name, TokenHash: tokenHash, CreatedAt: toMillis(now),
	})
	if err != nil {
		return APIToken{}, fmt.Errorf("create api token: %w", err)
	}
	return APIToken{
		ID: row.ID, UserID: row.UserID, Name: row.Name,
		CreatedAt: fromMillis(row.CreatedAt), LastUsedAt: fromNullMillis(row.LastUsedAt),
	}, nil
}

// APITokens returns userID's tokens, oldest first.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.q.ListAPITokens(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	out := make([]APIToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, APIToken{
			ID: r.ID, UserID: r.UserID, Name: r.Name,
			CreatedAt: fromMillis(r.CreatedAt), LastUsedAt: fromNullMillis(r.LastUsedAt),
		})
	}
	return out, nil
}

// DeleteAPIToken revokes userID's token id, or returns homelab.ErrNotFound when userID has no
// such token.
func (s *Store) DeleteAPIToken(ctx context.Context, userID int64, id string) error {
	n, err := s.q.DeleteAPIToken(ctx, db.DeleteAPITokenParams{ID: id, UserID: userID})
	if err != nil {
		return fmt.Errorf("delete api token: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete api token: %w", homelab.ErrNotFound)
	}
	return nil
}

// APITokenByHash returns the token whose hash is tokenHash, with its user, or
// homelab.ErrNotFound.
func (s *Store) APITokenByHash(ctx context.Context, tokenHash string) (APIToken, User, error) {
	row, err := s.q.GetAPITokenByHash(ctx, tokenHash)
	if err != nil {
		return APIToken{}, User{}, notFound("get api token", err)
	}
	t := APIToken{
		ID: row.ID, UserID: row.UserID, Name: row.Name,
		CreatedAt: fromMillis(row.CreatedAt), LastUsedAt: fromNullMillis(row.LastUsedAt),
	}
	u := User{ID: row.UserID, Username: row.Username, PasswordHash: row.PasswordHash, CreatedAt: fromMillis(row.UserCreatedAt)}
	return t, u, nil
}

// TouchAPIToken records a use of token id.
func (s *Store) TouchAPIToken(ctx context.Context, id string, now time.Time) error {
	err := s.q.TouchAPIToken(ctx, db.TouchAPITokenParams{LastUsedAt: sql.NullInt64{Int64: toMillis(now), Valid: true}, ID: id})
	if err != nil {
		return fmt.Errorf("touch api token: %w", err)
	}
	return nil
}
