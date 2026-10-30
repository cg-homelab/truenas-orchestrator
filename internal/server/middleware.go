package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// authenticate is the huma middleware that enforces each operation's declared Security.
//
// M0 placeholder: it passes every request through. M2 replaces it with session and bearer
// token checks. It is registered before any operation so that replacement covers them all.
func authenticate() func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		next(ctx)
	}
}

// logRequests logs one line per request with its method, path, status and duration.
func logRequests(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start),
			)
		})
	}
}

// recoverPanics turns a panic in a handler into a logged 500 instead of a dropped connection.
func recoverPanics(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler { //nolint:errorlint // recover() returns the value as panicked, never wrapped
					panic(v)
				}
				logger.ErrorContext(r.Context(), "handler panic",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", v,
				)
				http.Error(w, "internal error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder records the status code a handler writes.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records code and forwards it.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
