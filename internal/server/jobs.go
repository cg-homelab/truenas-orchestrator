package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite"
)

const (
	// jobQueueSize bounds how many jobs can wait for the runner.
	jobQueueSize = 16
	// jobFinishTimeout bounds recording a job's outcome after its context is cancelled.
	jobFinishTimeout = 5 * time.Second
	// errInterrupted is the error recorded for a job cut short by shutdown or a restart.
	errInterrupted = "interrupted"
	// maxJobLogLines bounds a job's log; older lines are discarded.
	maxJobLogLines = 2000
	// truncatedMarker stands in for discarded log lines.
	truncatedMarker = "… earlier lines truncated"
)

// urlCredentials matches the userinfo part of a URL, which git can print in progress lines and
// errors (https://user:token@host/...).
var urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// scrub removes URL credentials from a line before it is stored or shown.
func scrub(s string) string { return urlCredentials.ReplaceAllString(s, "${1}[REDACTED]@") }

// jobFunc is the work of a job. report appends one line to the job log.
type jobFunc func(ctx context.Context, report func(line string)) error

// job is a queued or running job, kept in memory so get-job and job-events can follow it live.
type job struct {
	run jobFunc

	mu  sync.Mutex
	rec sqlite.Job // everything but the log, which is in log
	// log holds the newest maxJobLogLines lines; dropped counts the older lines discarded.
	// Line numbers used as cursors are absolute: log[i] is line dropped+i.
	log     []string
	dropped int
	// changed is closed and replaced on every change, waking job-events subscribers.
	changed chan struct{}
}

// update applies fn under the lock and wakes subscribers.
func (j *job) update(fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	fn()
	close(j.changed)
	j.changed = make(chan struct{})
}

// appendLine adds a line, discarding the oldest once the log holds maxJobLogLines.
func (j *job) appendLine(line string) {
	j.update(func() {
		if len(j.log) < maxJobLogLines {
			j.log = append(j.log, line)
			return
		}
		copy(j.log, j.log[1:])
		j.log[len(j.log)-1] = line
		j.dropped++
	})
}

// fullLog returns the log as stored and shown: the kept lines, after a truncation marker when
// older lines were discarded. The caller holds j.mu.
func (j *job) fullLog() []string {
	out := make([]string, 0, len(j.log)+1)
	if j.dropped > 0 {
		out = append(out, truncatedMarker)
	}
	return append(out, j.log...)
}

// record returns the job's record with its log.
func (j *job) record() sqlite.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec := j.rec
	rec.Log = j.fullLog()
	return rec
}

// since returns the lines from absolute line cursor on (preceded by the truncation marker when
// some of them were discarded), the cursor after them, the record without its log, and a
// channel closed on the next change.
func (j *job) since(cursor int) (lines []string, next int, rec sqlite.Job, changed <-chan struct{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if cursor < j.dropped {
		lines = append(lines, truncatedMarker)
		cursor = j.dropped
	}
	lines = append(lines, j.log[cursor-j.dropped:]...)
	return lines, j.dropped + len(j.log), j.rec, j.changed
}

// jobRunner runs jobs one at a time, in order. Records go to the store; while a job is queued or
// running it is also held in memory. run must be called for queued jobs to execute.
type jobRunner struct {
	store  *sqlite.Store
	logger *slog.Logger
	now    func() time.Time
	// onDone runs after a job's outcome is recorded and before subscribers see it.
	onDone func()

	queue chan *job

	mu     sync.Mutex
	active map[string]*job // queued or running
	// closed is set when run returns; enqueue then refuses, so no job is left queued with
	// nobody to run it (and no job-events stream waits on one forever).
	closed bool
}

// errShuttingDown is returned by enqueue once the runner has stopped.
var errShuttingDown = errors.New("job runner stopped")

func newJobRunner(store *sqlite.Store, logger *slog.Logger, now func() time.Time, onDone func()) *jobRunner {
	return &jobRunner{
		store:  store,
		logger: logger,
		now:    now,
		onDone: onDone,
		queue:  make(chan *job, jobQueueSize),
		active: map[string]*job{},
	}
}

// newID returns a random public identifier.
func newID() string {
	return hex.EncodeToString(randomBytes(8))
}

// randomBytes returns n bytes from crypto/rand, which never fails on supported platforms.
func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error (Go 1.24+)
	return b
}

// enqueue records a new job of kind and queues it. It returns homelab.ErrConflict when a job of
// the same kind is already queued or running.
func (r *jobRunner) enqueue(ctx context.Context, kind string, fn jobFunc) (sqlite.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return sqlite.Job{}, errShuttingDown
	}
	for _, j := range r.active {
		if j.rec.Kind == kind { // Kind never changes, so no need for j.mu
			return sqlite.Job{}, fmt.Errorf("job %s already active: %w", kind, homelab.ErrConflict)
		}
	}
	rec, err := r.store.CreateJob(ctx, newID(), kind, r.now())
	if err != nil {
		return sqlite.Job{}, err
	}
	j := &job{run: fn, rec: rec, log: []string{}, changed: make(chan struct{})}
	select {
	case r.queue <- j:
	default:
		if err := r.store.FinishJob(ctx, rec.ID, sqlite.JobFailed, "job queue full", nil, r.now()); err != nil {
			r.logger.ErrorContext(ctx, "record job failure", "job_id", rec.ID, "error", err)
		}
		return sqlite.Job{}, fmt.Errorf("job queue full: %w", homelab.ErrConflict)
	}
	r.active[rec.ID] = j
	return rec, nil
}

// lookup returns the in-memory job id, or nil when it is not queued or running.
func (r *jobRunner) lookup(id string) *job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[id]
}

// get returns job id from memory when it is active, else from the store.
func (r *jobRunner) get(ctx context.Context, id string) (sqlite.Job, error) {
	if j := r.lookup(id); j != nil {
		return j.record(), nil
	}
	return r.store.Job(ctx, id)
}

// run executes queued jobs until ctx is done. It first marks every job a previous process left
// queued or running as failed, then, on return, does the same for jobs still queued.
func (r *jobRunner) run(ctx context.Context) {
	r.failOrphans(ctx)
	for {
		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.closed = true
			r.mu.Unlock()
			r.abandonQueued(ctx)
			return
		case j := <-r.queue:
			r.execute(ctx, j)
		}
	}
}

// failOrphans marks unfinished jobs that this process does not hold as failed: interrupted.
func (r *jobRunner) failOrphans(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids, err := r.store.UnfinishedJobs(ctx)
	if err != nil {
		r.logger.ErrorContext(ctx, "list unfinished jobs", "error", err)
		return
	}
	for _, id := range ids {
		if _, ok := r.active[id]; ok {
			continue
		}
		if err := r.store.FinishJob(ctx, id, sqlite.JobFailed, errInterrupted, nil, r.now()); err != nil {
			r.logger.ErrorContext(ctx, "fail interrupted job", "job_id", id, "error", err)
			continue
		}
		r.logger.InfoContext(ctx, "job marked interrupted", "job_id", id)
	}
}

// abandonQueued fails every job still waiting in the queue.
func (r *jobRunner) abandonQueued(ctx context.Context) {
	for {
		select {
		case j := <-r.queue:
			r.finish(ctx, j, sqlite.JobFailed, errInterrupted)
		default:
			return
		}
	}
}

// execute runs one job to completion and records its outcome.
func (r *jobRunner) execute(ctx context.Context, j *job) {
	id := j.rec.ID
	if ctx.Err() != nil { // dequeued after shutdown began
		r.finish(ctx, j, sqlite.JobFailed, errInterrupted)
		return
	}
	now := r.now()
	if err := r.store.StartJob(ctx, id, now); err != nil {
		r.logger.ErrorContext(ctx, "start job", "job_id", id, "error", err)
		r.finish(ctx, j, sqlite.JobFailed, "internal error")
		return
	}
	j.update(func() {
		j.rec.State = sqlite.JobRunning
		j.rec.StartedAt = now
	})
	r.logger.InfoContext(ctx, "job started", "job_id", id, "kind", j.rec.Kind)

	err := r.call(ctx, j)
	switch {
	case err == nil:
		r.finish(ctx, j, sqlite.JobSucceeded, "")
	case ctx.Err() != nil:
		r.finish(ctx, j, sqlite.JobFailed, errInterrupted)
	default:
		r.finish(ctx, j, sqlite.JobFailed, scrub(err.Error()))
	}
}

// call runs the job function, turning a panic into an error.
func (r *jobRunner) call(ctx context.Context, j *job) (err error) {
	defer func() {
		if v := recover(); v != nil {
			r.logger.ErrorContext(ctx, "job panic", "job_id", j.rec.ID, "panic", v)
			err = errors.New("internal error")
		}
	}()
	return j.run(ctx, func(line string) { j.appendLine(scrub(line)) })
}

// finish records the outcome, runs onDone, then publishes the final state to subscribers and
// forgets the job. The store write gets its own bounded context so a shutdown still records it.
func (r *jobRunner) finish(ctx context.Context, j *job, state sqlite.JobState, errMsg string) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobFinishTimeout)
	defer cancel()
	now := r.now()
	log := j.record().Log
	if err := r.store.FinishJob(fctx, j.rec.ID, state, errMsg, log, now); err != nil {
		r.logger.ErrorContext(fctx, "record job outcome", "job_id", j.rec.ID, "error", err)
	}
	if r.onDone != nil {
		r.onDone()
	}
	j.update(func() {
		j.rec.State = state
		j.rec.FinishedAt = now
		j.rec.Error = errMsg
	})
	r.mu.Lock()
	delete(r.active, j.rec.ID)
	r.mu.Unlock()
	r.logger.InfoContext(fctx, "job finished", "job_id", j.rec.ID, "kind", j.rec.Kind, "state", string(state))
}

// JobView is a job's record and log.
type JobView struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind" doc:"What the job does, e.g. repo.fetch"`
	State      string     `json:"state" enum:"queued,running,succeeded,failed"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      *string    `json:"error" doc:"Why the job failed; null unless state is failed"`
	Log        []string   `json:"log"`
}

func newJobView(j sqlite.Job) JobView {
	log := j.Log
	if log == nil {
		log = []string{}
	}
	return JobView{
		ID:         j.ID,
		Kind:       j.Kind,
		State:      string(j.State),
		CreatedAt:  j.CreatedAt.UTC(),
		StartedAt:  optionalTime(j.StartedAt.UTC()),
		FinishedAt: optionalTime(j.FinishedAt.UTC()),
		Error:      optionalString(j.Error),
		Log:        log,
	}
}

// GetJobInput is the get-job request.
type GetJobInput struct {
	ID string `path:"id" maxLength:"64"`
}

// GetJobOutput is the get-job response.
type GetJobOutput struct {
	Body JobView
}

var opGetJob = huma.Operation{
	OperationID: "get-job",
	Method:      http.MethodGet,
	Path:        "/api/jobs/{id}",
	Summary:     "Get a job's state and log",
	Tags:        []string{"jobs"},
	Security:    authenticated(),
	Errors:      []int{http.StatusNotFound},
}

func handleGetJob(logger *slog.Logger, jobs *jobRunner) func(context.Context, *GetJobInput) (*GetJobOutput, error) {
	return func(ctx context.Context, in *GetJobInput) (*GetJobOutput, error) {
		j, err := jobs.get(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &GetJobOutput{Body: newJobView(j)}, nil
	}
}

// JobLogEvent carries one job log line.
type JobLogEvent struct {
	Line string `json:"line"`
}

// JobDoneEvent ends a job-events stream.
type JobDoneEvent struct {
	State string  `json:"state" enum:"succeeded,failed"`
	Error *string `json:"error" doc:"Why the job failed; null on success"`
}

// JobEventsInput is the job-events request.
type JobEventsInput struct {
	ID string `path:"id" maxLength:"64"`
}

var opJobEvents = huma.Operation{
	OperationID: "job-events",
	Method:      http.MethodGet,
	Path:        "/api/jobs/{id}/events",
	Summary:     "Stream a job's log lines, then its outcome",
	Description: "Replays the log so far, streams new lines as `log` events, and always ends with one `done` " +
		"event, also for a job that already finished or does not exist (state failed).",
	Tags:     []string{"jobs"},
	Security: authenticated(),
}

// jobEventTypes maps SSE event names to their data types.
var jobEventTypes = map[string]any{
	"log":  JobLogEvent{},
	"done": JobDoneEvent{},
}

func handleJobEvents(logger *slog.Logger, jobs *jobRunner) func(context.Context, *JobEventsInput, sse.Sender) {
	return func(ctx context.Context, in *JobEventsInput, send sse.Sender) {
		j := jobs.lookup(in.ID)
		if j == nil {
			sendFinishedJob(ctx, logger, jobs, in.ID, send)
			return
		}
		cursor := 0
		for {
			lines, next, rec, changed := j.since(cursor)
			for _, l := range lines {
				if err := send.Data(JobLogEvent{Line: l}); err != nil {
					return
				}
			}
			cursor = next
			if rec.State.Terminal() {
				_ = send.Data(newJobDone(rec.State, rec.Error))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}
}

// sendFinishedJob replays a job that is not in memory from the store and ends the stream. An
// unknown job, or one a previous process left unfinished, ends with a failed done event: an
// EventSource cannot see status codes.
func sendFinishedJob(ctx context.Context, logger *slog.Logger, jobs *jobRunner, id string, send sse.Sender) {
	rec, err := jobs.store.Job(ctx, id)
	switch {
	case errors.Is(err, homelab.ErrNotFound):
		_ = send.Data(newJobDone(sqlite.JobFailed, "job not found"))
		return
	case err != nil:
		logger.ErrorContext(ctx, "internal error", "error", err)
		_ = send.Data(newJobDone(sqlite.JobFailed, "internal error"))
		return
	}
	for _, l := range rec.Log {
		if err := send.Data(JobLogEvent{Line: l}); err != nil {
			return
		}
	}
	if !rec.State.Terminal() {
		_ = send.Data(newJobDone(sqlite.JobFailed, errInterrupted))
		return
	}
	_ = send.Data(newJobDone(rec.State, rec.Error))
}

func newJobDone(state sqlite.JobState, errMsg string) JobDoneEvent {
	return JobDoneEvent{State: string(state), Error: optionalString(errMsg)}
}
