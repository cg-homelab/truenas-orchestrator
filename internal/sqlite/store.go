package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the orchestrator's database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
	q  *db.Queries
}

// Open opens (creating if needed) the SQLite database at path and checks that it is reachable.
// It does not migrate; call Migrate. Pragmas are set in the DSN so every pooled connection
// gets them.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return &Store{db: sqlDB, q: db.New(sqlDB)}, nil
}

// Close closes the connection pool.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}

// Migrate applies every pending migration embedded from migrations/.
func (s *Store) Migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.db, sub)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Meta returns the schema_meta value for key, or homelab.ErrNotFound.
func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	v, err := s.q.GetMeta(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("meta %q: %w", key, homelab.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("meta %q: %w", key, err)
	}
	return v, nil
}

// withTx runs fn in a transaction, committing if fn returns nil and rolling back otherwise.
// It is the only caller of BeginTx; exported Store methods use it so *db.Queries never leaves
// the package.
func (s *Store) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
