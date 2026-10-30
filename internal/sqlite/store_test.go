package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

func openMigrated(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return s
}

func TestOpenMigrateReadMeta(t *testing.T) {
	s := openMigrated(t)

	got, err := s.Meta(t.Context(), "created_by")
	if err != nil {
		t.Fatalf("Meta(created_by) error = %v", err)
	}
	if want := "orchestrator"; got != want {
		t.Errorf("Meta(created_by) = %q, want %q", got, want)
	}

	if _, err := s.Meta(t.Context(), "missing"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("Meta(missing) error = %v, want homelab.ErrNotFound", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openMigrated(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
}

func TestPragmas(t *testing.T) {
	s := openMigrated(t)
	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		t.Run(tt.pragma, func(t *testing.T) {
			var got string
			if err := s.db.QueryRowContext(t.Context(), "PRAGMA "+tt.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
			}
			if got != tt.want {
				t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
			}
		})
	}
}

func TestWithTx(t *testing.T) {
	s := openMigrated(t)
	errBoom := errors.New("boom")

	err := s.withTx(t.Context(), func(q *db.Queries) error {
		if _, err := q.GetMeta(t.Context(), "created_by"); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Errorf("withTx(fn returning errBoom) = %v, want errBoom", err)
	}

	if err := s.withTx(t.Context(), func(q *db.Queries) error {
		_, err := q.GetMeta(t.Context(), "created_by")
		return err
	}); err != nil {
		t.Errorf("withTx(read) = %v, want nil", err)
	}
}
