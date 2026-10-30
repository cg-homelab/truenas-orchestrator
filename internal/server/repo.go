package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// jobKindRepoFetch fetches and fast-forwards the homelab repo.
const jobKindRepoFetch = "repo.fetch"

// GetRepoOutput is the get-repo response.
type GetRepoOutput struct {
	Body RepoView
}

var opGetRepo = huma.Operation{
	OperationID: "get-repo",
	Method:      http.MethodGet,
	Path:        "/api/repo",
	Summary:     "Get the homelab repo's git status and repo-level findings",
	Tags:        []string{"repo"},
	Security:    authenticated(),
}

func handleGetRepo(
	logger *slog.Logger, reports *reportCache,
) func(context.Context, *struct{}) (*GetRepoOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetRepoOutput, error) {
		rep, err := reports.get(ctx)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &GetRepoOutput{Body: newRepoView(rep)}, nil
	}
}

// JobStarted is the response of an operation that starts a job.
type JobStarted struct {
	JobID string `json:"job_id" doc:"Follow it with get-job or job-events"`
}

// FetchRepoOutput is the fetch-repo response.
type FetchRepoOutput struct {
	Body JobStarted
}

var opFetchRepo = huma.Operation{
	OperationID:   "fetch-repo",
	Method:        http.MethodPost,
	Path:          "/api/repo/fetch",
	Summary:       "Start a job that fetches and fast-forwards the homelab repo",
	Tags:          []string{"repo"},
	Security:      authenticated(),
	DefaultStatus: http.StatusAccepted,
	Errors:        []int{http.StatusConflict, http.StatusServiceUnavailable},
}

func handleFetchRepo(
	logger *slog.Logger, repo fetcher, jobs *jobRunner,
) func(context.Context, *struct{}) (*FetchRepoOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*FetchRepoOutput, error) {
		if repo == nil {
			return nil, apiError(ctx, logger, errNoRepo)
		}
		j, err := jobs.enqueue(ctx, jobKindRepoFetch, func(ctx context.Context, report func(string)) error {
			_, err := repo.Fetch(ctx, report)
			return err
		})
		if errors.Is(err, homelab.ErrConflict) {
			return nil, huma.Error409Conflict("a fetch job is already running")
		}
		if errors.Is(err, errShuttingDown) {
			return nil, huma.Error503ServiceUnavailable("the server is shutting down")
		}
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		return &FetchRepoOutput{Body: JobStarted{JobID: j.ID}}, nil
	}
}
