package server

import (
	"context"
	"errors"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// apiError maps a domain error to its HTTP error. It is the one place that does so. A 500 is
// logged with the full error and returned with a fixed message: internal error text can carry
// paths and never reaches a client. The 4xx messages are fixed too; a handler that wants a more
// specific message returns its own huma error.
func apiError(ctx context.Context, logger *slog.Logger, err error) error {
	switch {
	case errors.Is(err, homelab.ErrNotFound):
		return huma.Error404NotFound("not found")
	case errors.Is(err, homelab.ErrValidation):
		return huma.Error422UnprocessableEntity("validation failed")
	case errors.Is(err, homelab.ErrConflict):
		return huma.Error409Conflict("conflict")
	case errors.Is(err, homelab.ErrForbidden):
		return huma.Error403Forbidden("forbidden")
	case errors.Is(err, homelab.ErrUnauthenticated):
		return huma.Error401Unauthorized("authentication required")
	case ctx.Err() != nil:
		// The client went away or the request timed out: not a server fault worth an alert.
		logger.InfoContext(ctx, "request cancelled", "error", err)
		return huma.Error500InternalServerError("internal error")
	default:
		logger.ErrorContext(ctx, "internal error", "error", err)
		return huma.Error500InternalServerError("internal error")
	}
}
