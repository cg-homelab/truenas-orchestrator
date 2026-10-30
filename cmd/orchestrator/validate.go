package main

import (
	"context"
	"flag"
	"fmt"
	"io"
)

// runValidate validates the homelab repo at the path argument and prints its findings.
//
// M0 stub: it checks its arguments and prints a notice. M1 calls homelab.ValidateRepo with
// the context it now ignores.
func runValidate(_ context.Context, args []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("validate", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	asJSON := fset.Bool("json", false, "print findings as JSON")
	if err := fset.Parse(args); err != nil {
		return fmt.Errorf("validate: %w: %w", err, errUsage)
	}
	if fset.NArg() != 1 {
		return fmt.Errorf("validate needs exactly one repo path: %w", errUsage)
	}
	_ = *asJSON // selects the output format in M1
	if _, err := fmt.Fprintln(stdout, "validate: not yet implemented (M1)"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
