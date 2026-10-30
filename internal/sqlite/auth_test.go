package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestUsers(t *testing.T) {
	s := openMigrated(t)
	if n, err := s.CountUsers(t.Context()); err != nil || n != 0 {
		t.Fatalf("CountUsers() = %d, %v, want 0", n, err)
	}
	u, err := s.CreateFirstUser(t.Context(), "admin", "$argon2id$hash", t0)
	if err != nil {
		t.Fatalf("CreateFirstUser() error = %v", err)
	}
	want := User{ID: u.ID, Username: "admin", PasswordHash: "$argon2id$hash", CreatedAt: t0}
	if diff := cmp.Diff(want, u); diff != "" {
		t.Errorf("CreateFirstUser() mismatch (-want +got):\n%s", diff)
	}
	if _, err := s.CreateFirstUser(t.Context(), "second", "h", t0); !errors.Is(err, homelab.ErrForbidden) {
		t.Errorf("second CreateFirstUser() error = %v, want ErrForbidden", err)
	}
	got, err := s.UserByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatalf("UserByUsername() error = %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("UserByUsername() mismatch (-want +got):\n%s", diff)
	}
	if _, err := s.UserByUsername(t.Context(), "nobody"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("UserByUsername(nobody) error = %v, want ErrNotFound", err)
	}
}

func TestSessions(t *testing.T) {
	s := openMigrated(t)
	u, err := s.CreateFirstUser(t.Context(), "admin", "h", t0)
	if err != nil {
		t.Fatal(err)
	}
	sess := Session{ID: "hash-a", UserID: u.ID, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(7 * 24 * time.Hour)}
	if err := s.CreateSession(t.Context(), sess); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	gotSess, gotUser, err := s.SessionByID(t.Context(), "hash-a")
	if err != nil {
		t.Fatalf("SessionByID() error = %v", err)
	}
	if diff := cmp.Diff(sess, gotSess); diff != "" {
		t.Errorf("SessionByID() session mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(u, gotUser); diff != "" {
		t.Errorf("SessionByID() user mismatch (-want +got):\n%s", diff)
	}

	later := t0.Add(time.Hour)
	if err := s.TouchSession(t.Context(), "hash-a", later); err != nil {
		t.Fatalf("TouchSession() error = %v", err)
	}
	if gotSess, _, _ = s.SessionByID(t.Context(), "hash-a"); !gotSess.LastSeenAt.Equal(later) {
		t.Errorf("LastSeenAt after touch = %v, want %v", gotSess.LastSeenAt, later)
	}

	// hash-b is idle since t0; hash-a was seen at t0+1h.
	if err := s.CreateSession(t.Context(), Session{ID: "hash-b", UserID: u.ID, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0.Add(time.Hour * 100)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteExpiredSessions(t.Context(), t0.Add(2*time.Hour), t0.Add(30*time.Minute))
	if err != nil || n != 1 {
		t.Errorf("DeleteExpiredSessions() = %d, %v, want 1", n, err)
	}
	if _, _, err := s.SessionByID(t.Context(), "hash-b"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("idle session after cleanup error = %v, want ErrNotFound", err)
	}

	if err := s.DeleteSession(t.Context(), "hash-a"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, _, err := s.SessionByID(t.Context(), "hash-a"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("SessionByID() after delete error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteSession(t.Context(), "hash-a"); err != nil {
		t.Errorf("DeleteSession() of an absent session error = %v, want nil", err)
	}
}

func TestAPITokens(t *testing.T) {
	s := openMigrated(t)
	u, err := s.CreateFirstUser(t.Context(), "admin", "h", t0)
	if err != nil {
		t.Fatal(err)
	}
	if ts, err := s.APITokens(t.Context(), u.ID); err != nil || ts == nil || len(ts) != 0 {
		t.Errorf("APITokens() before create = %#v, %v, want empty non-nil", ts, err)
	}
	tok, err := s.CreateAPIToken(t.Context(), u.ID, "id1", "cli", "thash", t0)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}
	want := APIToken{ID: "id1", UserID: u.ID, Name: "cli", CreatedAt: t0}
	if diff := cmp.Diff(want, tok); diff != "" {
		t.Errorf("CreateAPIToken() mismatch (-want +got):\n%s", diff)
	}
	if _, err := s.CreateAPIToken(t.Context(), u.ID, "id2", "dup", "thash", t0); err == nil {
		t.Error("CreateAPIToken() with a duplicate hash succeeded")
	}

	got, gotUser, err := s.APITokenByHash(t.Context(), "thash")
	if err != nil || gotUser.Username != "admin" {
		t.Fatalf("APITokenByHash() = %+v, %+v, %v", got, gotUser, err)
	}
	if err := s.TouchAPIToken(t.Context(), "id1", t0.Add(time.Minute)); err != nil {
		t.Fatalf("TouchAPIToken() error = %v", err)
	}
	ts, err := s.APITokens(t.Context(), u.ID)
	if err != nil || len(ts) != 1 || !ts[0].LastUsedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("APITokens() after touch = %+v, %v, want last used at t0+1m", ts, err)
	}

	if err := s.DeleteAPIToken(t.Context(), u.ID+1, "id1"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("DeleteAPIToken() for another user error = %v, want ErrNotFound", err)
	}
	if err := s.DeleteAPIToken(t.Context(), u.ID, "id1"); err != nil {
		t.Errorf("DeleteAPIToken() error = %v", err)
	}
	if _, _, err := s.APITokenByHash(t.Context(), "thash"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("APITokenByHash() after delete error = %v, want ErrNotFound", err)
	}
}

func TestJobs(t *testing.T) {
	s := openMigrated(t)
	j, err := s.CreateJob(t.Context(), "j1", "repo.fetch", t0)
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	want := Job{ID: "j1", Kind: "repo.fetch", State: JobQueued, CreatedAt: t0, Log: []string{}}
	if diff := cmp.Diff(want, j); diff != "" {
		t.Errorf("CreateJob() mismatch (-want +got):\n%s", diff)
	}
	if _, err := s.CreateJob(t.Context(), "j2", "repo.fetch", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.StartJob(t.Context(), "j1", t0.Add(time.Second)); err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	if ids, err := s.UnfinishedJobs(t.Context()); err != nil || !cmp.Equal(ids, []string{"j1", "j2"}) {
		t.Errorf("UnfinishedJobs() = %v, %v, want [j1 j2]", ids, err)
	}
	if err := s.FinishJob(t.Context(), "j1", JobFailed, "boom", []string{"a", "b"}, t0.Add(2*time.Second)); err != nil {
		t.Fatalf("FinishJob() error = %v", err)
	}
	got, err := s.Job(t.Context(), "j1")
	if err != nil {
		t.Fatalf("Job() error = %v", err)
	}
	want = Job{
		ID: "j1", Kind: "repo.fetch", State: JobFailed, CreatedAt: t0,
		StartedAt: t0.Add(time.Second), FinishedAt: t0.Add(2 * time.Second), Error: "boom", Log: []string{"a", "b"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Job() mismatch (-want +got):\n%s", diff)
	}
	if ids, err := s.UnfinishedJobs(t.Context()); err != nil || !cmp.Equal(ids, []string{"j2"}) {
		t.Errorf("UnfinishedJobs() after finish = %v, %v, want [j2]", ids, err)
	}
	if _, err := s.Job(t.Context(), "nope"); !errors.Is(err, homelab.ErrNotFound) {
		t.Errorf("Job(nope) error = %v, want ErrNotFound", err)
	}
	if !JobFailed.Terminal() || !JobSucceeded.Terminal() || JobRunning.Terminal() || JobQueued.Terminal() {
		t.Error("JobState.Terminal() is wrong")
	}
}
