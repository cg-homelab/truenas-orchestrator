package sqlite

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestAuditAppendAndList(t *testing.T) {
	s := openMigrated(t)
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	got, err := s.AuditEntries(t.Context(), 10)
	if err != nil {
		t.Fatalf("AuditEntries() on an empty log error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("AuditEntries() on an empty log = %+v, want none", got)
	}

	entries := []AuditEntry{
		{At: t0, User: "admin", Action: "env.write", Target: "compose/a/.env", Details: map[string]string{"keys_set": "A,B"}},
		{At: t0.Add(time.Second), User: "admin", Action: "secret.write", Target: "/mnt/x/secret"},
		{At: t0.Add(time.Second), User: "other", Action: "setup.run", Target: "a", Details: map[string]string{"created": "3"}},
	}
	for _, e := range entries {
		if err := s.Audit(t.Context(), e); err != nil {
			t.Fatalf("Audit(%s) error = %v", e.Action, err)
		}
	}

	got, err = s.AuditEntries(t.Context(), 10)
	if err != nil {
		t.Fatalf("AuditEntries() error = %v", err)
	}
	// Newest first; entries with the same timestamp in reverse insertion order.
	want := []AuditEntry{
		{ID: 3, At: t0.Add(time.Second), User: "other", Action: "setup.run", Target: "a", Details: map[string]string{"created": "3"}},
		{ID: 2, At: t0.Add(time.Second), User: "admin", Action: "secret.write", Target: "/mnt/x/secret", Details: map[string]string{}},
		{ID: 1, At: t0, User: "admin", Action: "env.write", Target: "compose/a/.env", Details: map[string]string{"keys_set": "A,B"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AuditEntries() mismatch (-want +got):\n%s", diff)
	}

	got, err = s.AuditEntries(t.Context(), 1)
	if err != nil {
		t.Fatalf("AuditEntries(1) error = %v", err)
	}
	if len(got) != 1 || got[0].ID != 3 {
		t.Errorf("AuditEntries(1) = %+v, want only entry 3", got)
	}
}
