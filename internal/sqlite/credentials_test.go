package sqlite

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestTrueNASCredentials(t *testing.T) {
	s := openMigrated(t)
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	if _, err := s.TrueNASCredentials(t.Context()); !errors.Is(err, homelab.ErrNotFound) {
		t.Fatalf("TrueNASCredentials() on an empty table error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTrueNASCredentials(t.Context()); !errors.Is(err, homelab.ErrNotFound) {
		t.Fatalf("DeleteTrueNASCredentials() on an empty table error = %v, want ErrNotFound", err)
	}

	for i, c := range []struct{ user, key string }{{"truenas_admin", "1-first"}, {"orchestrator", "2-second"}} {
		creds := homelab.TrueNASCredentials{Username: c.user, APIKey: homelab.NewSecret(c.key)}
		if err := s.PutTrueNASCredentials(t.Context(), creds, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("PutTrueNASCredentials(%s) error = %v", c.user, err)
		}
		got, err := s.TrueNASCredentials(t.Context())
		if err != nil {
			t.Fatalf("TrueNASCredentials() error = %v", err)
		}
		if got.Username != c.user || got.APIKey.Reveal() != c.key {
			t.Errorf("TrueNASCredentials() = %s, want %s with the stored key", got.Username, c.user)
		}
		if s := fmt.Sprintf("%v %+v", got, got); s != "{"+c.user+" [REDACTED]} {Username:"+c.user+" APIKey:[REDACTED]}" {
			t.Errorf("credentials print as %q", s)
		}
	}
	var n int
	if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM credentials").Scan(&n); err != nil || n != 1 {
		t.Errorf("credentials rows = %d, %v, want 1 (put replaces)", n, err)
	}

	if err := s.DeleteTrueNASCredentials(t.Context()); err != nil {
		t.Fatalf("DeleteTrueNASCredentials() error = %v", err)
	}
	if _, err := s.TrueNASCredentials(t.Context()); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("TrueNASCredentials() after delete error = %v, want ErrNotFound", err)
	}
}
