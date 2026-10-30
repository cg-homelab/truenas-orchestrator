// Package compose parses compose.yaml bytes into the homelab model and renders manifests and
// overlays as bytes. It is I/O-free: the caller reads and writes files.
package compose

import (
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Parser parses compose files. The zero value is ready to use.
type Parser struct{}

var _ homelab.ComposeParser = (*Parser)(nil)

// Parse parses the compose file at path (relative to the repo root) from src.
//
// M0 stub: it returns a ComposeFile carrying only the path. Parsing arrives in M1.
func (p *Parser) Parse(path string, src []byte) (homelab.ComposeFile, error) {
	_ = src // parsed in M1
	return homelab.ComposeFile{Path: path}, nil
}
