package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/cg-homelab/truenas-orchestrator/internal/server"
)

// runOpenAPI writes the OpenAPI document to stdout. It needs no environment and no database:
// NewAPI is built with zero-value Deps.
func runOpenAPI(args []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("openapi", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	if err := fset.Parse(args); err != nil {
		return fmt.Errorf("openapi: %w: %w", err, errUsage)
	}
	if fset.NArg() != 0 {
		return fmt.Errorf("openapi takes no arguments: %w", errUsage)
	}

	api := server.NewAPI(http.NewServeMux(), server.Deps{})
	b, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal openapi: %w", err)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return fmt.Errorf("indent openapi: %w", err)
	}
	out.WriteByte('\n')
	if _, err := out.WriteTo(stdout); err != nil {
		return fmt.Errorf("write openapi: %w", err)
	}
	return nil
}
