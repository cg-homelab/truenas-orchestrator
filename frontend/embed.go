// Package frontend embeds the built SvelteKit SPA. //go:embed cannot reach parent directories,
// so the directive lives here, beside the build output. Only cmd/orchestrator imports it.
//
// frontend/build/ is gitignored: on a fresh clone, run `just setup` (which builds the SPA)
// before `go build ./...`.
package frontend

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:build
var build embed.FS

// FS returns the built SPA rooted at its index.html.
func FS() (fs.FS, error) {
	sub, err := fs.Sub(build, "build")
	if err != nil {
		return nil, fmt.Errorf("frontend build fs: %w", err)
	}
	return sub, nil
}
