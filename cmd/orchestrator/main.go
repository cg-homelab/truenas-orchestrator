// Command orchestrator is the TrueNAS homelab orchestrator: the HTTP server, the validator CLI,
// and the OpenAPI dumper.
//
// Usage:
//
//	orchestrator serve
//	orchestrator validate [--json] [--homelab-storage DIR] [--check-host-paths] <path>
//	orchestrator openapi
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Exit codes. 1 is any error (including, from M1, error-severity findings); 2 is a usage error.
const (
	exitError = 1
	exitUsage = 2
)

// errUsage marks errors caused by how the command was invoked. main exits 2 for them.
var errUsage = errors.New("usage: orchestrator serve | validate [--json] [--homelab-storage DIR] [--check-host-paths] <path> | openapi")

func main() {
	if err := run(context.Background(), os.Args, os.Environ(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

// run is the whole program, minus the process boundary: tests call it with fake arguments,
// a literal environment and a buffer.
func run(ctx context.Context, args, environ []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) < 2 {
		return errUsage
	}
	switch cmd, rest := args[1], args[2:]; cmd {
	case "serve":
		return runServe(ctx, rest, environ, stdout)
	case "validate":
		return runValidate(ctx, rest, stdout) // flags and a path only: no environment, no database
	case "openapi":
		return runOpenAPI(rest, stdout) // no configuration at all
	default:
		return fmt.Errorf("unknown command %q: %w", cmd, errUsage)
	}
}

// exitCode maps an error returned by run to a process exit code.
func exitCode(err error) int {
	if errors.Is(err, errUsage) {
		return exitUsage
	}
	return exitError // includes errFindings
}
