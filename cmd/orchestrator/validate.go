package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/compose"
	"github.com/cg-homelab/truenas-orchestrator/internal/gitrepo"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// errFindings means validation ran and found at least one error-severity finding. main exits 1.
var errFindings = errors.New("validation found errors")

// runValidate validates the homelab repo at the path argument and prints the report: text by
// default, JSON with --json. It returns errFindings, after printing, when any finding has error
// severity.
func runValidate(ctx context.Context, args []string, stdout io.Writer) error {
	fset := flag.NewFlagSet("validate", flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	asJSON := fset.Bool("json", false, "print the report as JSON")
	storage := fset.String("homelab-storage", "", "HOMELAB_STORAGE root to resolve host paths against (default: the repo path)")
	checkHost := fset.Bool("check-host-paths", false, "check that host paths exist on this machine")
	if err := fset.Parse(args); err != nil {
		return fmt.Errorf("validate: %w: %w", err, errUsage)
	}
	if fset.NArg() != 1 {
		return fmt.Errorf("validate needs exactly one repo path: %w", errUsage)
	}
	abs, err := filepath.Abs(fset.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	opts := homelab.Options{HomelabStorage: abs}
	if *storage != "" {
		if opts.HomelabStorage, err = filepath.Abs(*storage); err != nil {
			return fmt.Errorf("resolve --homelab-storage: %w", err)
		}
	}
	if *checkHost {
		opts.HostPathExists = hostPathExists
	}

	report, err := homelab.ValidateRepo(ctx, gitrepo.New(abs), &compose.Parser{}, opts)
	if err != nil {
		return fmt.Errorf("validate %s: %w", fset.Arg(0), err)
	}

	if *asJSON {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal report: %w", err)
		}
		b = append(b, '\n')
		if _, err := stdout.Write(b); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	} else if err := writeReportText(stdout, report); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if countSeverity(report, homelab.SeverityError) > 0 {
		return errFindings
	}
	return nil
}

// hostPathExists is the real filesystem check behind --check-host-paths.
func hostPathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

// countSeverity counts the findings of severity sev across the repo and every stack.
func countSeverity(r homelab.Report, sev homelab.Severity) int {
	n := 0
	count := func(fs []homelab.Finding) {
		for _, f := range fs {
			if f.Severity == sev {
				n++
			}
		}
	}
	count(r.Findings)
	for _, s := range r.Stacks {
		count(s.Findings)
	}
	return n
}

// writeReportText prints the human-readable report. It prints findings only, never env values.
func writeReportText(w io.Writer, r homelab.Report) error {
	var b strings.Builder
	dirty := "clean"
	if r.Repo.Dirty {
		dirty = "dirty"
	}
	fmt.Fprintf(&b, "repo: branch %s  ahead %d  behind %d  %s\n", r.Repo.Branch, r.Repo.Ahead, r.Repo.Behind, dirty)
	if len(r.Findings) > 0 {
		fmt.Fprintf(&b, "\n== repo  (%d findings)\n", len(r.Findings))
		writeFindings(&b, r.Findings)
	}
	for _, s := range r.Stacks {
		fmt.Fprintf(&b, "\n== %s  [%s]  (%d findings)\n", s.Name, s.Status, len(s.Findings))
		writeFindings(&b, s.Findings)
	}
	fmt.Fprintf(&b, "\n%d errors, %d warnings, %d info\n",
		countSeverity(r, homelab.SeverityError),
		countSeverity(r, homelab.SeverityWarning),
		countSeverity(r, homelab.SeverityInfo))
	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("write text report: %w", err)
	}
	return nil
}

func writeFindings(b *strings.Builder, findings []homelab.Finding) {
	for _, f := range findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(b, "  %s  %s  %s  %s\n", f.Code, f.Severity, loc, f.Message)
		if f.Fix != "" {
			fmt.Fprintf(b, "    fix: %s\n", f.Fix)
		}
	}
}
