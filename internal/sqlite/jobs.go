package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

// JobState is where a job is in its lifecycle.
type JobState string

// Job states. Succeeded and failed are terminal.
const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
)

// Terminal reports whether s is a final state.
func (s JobState) Terminal() bool {
	switch s {
	case JobSucceeded, JobFailed:
		return true
	case JobQueued, JobRunning:
		return false
	}
	return false
}

// Job is the persisted record of a background job.
type Job struct {
	ID        string
	Kind      string
	State     JobState
	CreatedAt time.Time
	// StartedAt and FinishedAt are zero until the job starts and finishes.
	StartedAt  time.Time
	FinishedAt time.Time
	// Error is empty unless the job failed.
	Error string
	// Log holds the job's output lines. It is written when the job finishes.
	Log []string
}

func newJob(r db.Job) (Job, error) {
	j := Job{
		ID:         r.ID,
		Kind:       r.Kind,
		State:      JobState(r.State),
		CreatedAt:  fromMillis(r.CreatedAt),
		StartedAt:  fromNullMillis(r.StartedAt),
		FinishedAt: fromNullMillis(r.FinishedAt),
		Error:      r.Error.String,
		Log:        []string{},
	}
	if err := json.Unmarshal([]byte(r.LogJson), &j.Log); err != nil {
		return Job{}, fmt.Errorf("decode log of job %s: %w", r.ID, err)
	}
	if j.Log == nil {
		j.Log = []string{}
	}
	return j, nil
}

// CreateJob records a new queued job.
func (s *Store) CreateJob(ctx context.Context, id, kind string, now time.Time) (Job, error) {
	row, err := s.q.CreateJob(ctx, db.CreateJobParams{ID: id, Kind: kind, CreatedAt: toMillis(now)})
	if err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}
	return newJob(row)
}

// StartJob marks job id as running.
func (s *Store) StartJob(ctx context.Context, id string, now time.Time) error {
	err := s.q.StartJob(ctx, db.StartJobParams{StartedAt: sql.NullInt64{Int64: toMillis(now), Valid: true}, ID: id})
	if err != nil {
		return fmt.Errorf("start job %s: %w", id, err)
	}
	return nil
}

// FinishJob records the final state, error message (empty for none) and log of job id.
func (s *Store) FinishJob(ctx context.Context, id string, state JobState, errMsg string, log []string, now time.Time) error {
	if log == nil {
		log = []string{}
	}
	b, err := json.Marshal(log)
	if err != nil {
		return fmt.Errorf("encode log of job %s: %w", id, err)
	}
	err = s.q.FinishJob(ctx, db.FinishJobParams{
		State:      string(state),
		FinishedAt: sql.NullInt64{Int64: toMillis(now), Valid: true},
		Error:      sql.NullString{String: errMsg, Valid: errMsg != ""},
		LogJson:    string(b),
		ID:         id,
	})
	if err != nil {
		return fmt.Errorf("finish job %s: %w", id, err)
	}
	return nil
}

// Job returns job id, or homelab.ErrNotFound.
func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	row, err := s.q.GetJob(ctx, id)
	if err != nil {
		return Job{}, notFound("get job "+id, err)
	}
	return newJob(row)
}

// UnfinishedJobs returns the ids of every queued or running job, oldest first.
func (s *Store) UnfinishedJobs(ctx context.Context) ([]string, error) {
	ids, err := s.q.ListUnfinishedJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unfinished jobs: %w", err)
	}
	return ids, nil
}
