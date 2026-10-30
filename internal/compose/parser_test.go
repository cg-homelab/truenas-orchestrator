package compose

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestParseStub(t *testing.T) {
	var p Parser
	got, err := p.Parse("whoami/compose.yaml", []byte("services: {}\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := homelab.ComposeFile{Path: "whoami/compose.yaml"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Parse() mismatch (-want +got):\n%s", diff)
	}
}
