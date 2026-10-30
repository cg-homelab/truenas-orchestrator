package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// ListStacksOutput is the list-stacks response.
type ListStacksOutput struct {
	Body StackList
}

var opListStacks = huma.Operation{
	OperationID: "list-stacks",
	Method:      http.MethodGet,
	Path:        "/api/stacks",
	Summary:     "List stacks with their status and finding counts",
	Tags:        []string{"stacks"},
	Security:    authenticated(),
}

func handleListStacks(
	logger *slog.Logger, reports *reportCache,
) func(context.Context, *struct{}) (*ListStacksOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*ListStacksOutput, error) {
		rep, err := reports.get(ctx)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		out := &ListStacksOutput{Body: StackList{Stacks: make([]StackSummary, 0, len(rep.Stacks))}}
		for _, s := range rep.Stacks {
			out.Body.Stacks = append(out.Body.Stacks, newStackSummary(s))
		}
		return out, nil
	}
}

// GetStackInput is the get-stack request.
type GetStackInput struct {
	Name   string `path:"name" maxLength:"255" doc:"App directory name under compose/"`
	Reveal bool   `query:"reveal" doc:"Return env values instead of [REDACTED]; the reveal is logged"`
}

// GetStackOutput is the get-stack response.
type GetStackOutput struct {
	Body StackDetail
}

var opGetStack = huma.Operation{
	OperationID: "get-stack",
	Method:      http.MethodGet,
	Path:        "/api/stacks/{name}",
	Summary:     "Get one stack with its findings and resolved env scopes",
	Tags:        []string{"stacks"},
	Security:    authenticated(),
	Errors:      []int{http.StatusNotFound},
}

func handleGetStack(
	logger *slog.Logger, reports *reportCache,
) func(context.Context, *GetStackInput) (*GetStackOutput, error) {
	return func(ctx context.Context, in *GetStackInput) (*GetStackOutput, error) {
		rep, err := reports.get(ctx)
		if err != nil {
			return nil, apiError(ctx, logger, err)
		}
		for _, s := range rep.Stacks {
			if s.Name != in.Name {
				continue
			}
			if in.Reveal {
				u, _ := userFrom(ctx)
				logger.InfoContext(ctx, "env values revealed", "stack", s.Name, "user", u.Username)
			}
			return &GetStackOutput{Body: newStackDetail(s, in.Reveal)}, nil
		}
		return nil, huma.Error404NotFound("no such stack")
	}
}
