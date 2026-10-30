package gitrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want homelab.RepoStatus
	}{
		{
			name: "unborn branch",
			out:  "# branch.oid (initial)\n# branch.head main\n",
			want: homelab.RepoStatus{Branch: "main"},
		},
		{
			name: "tracking branch ahead and behind with changes",
			out: "# branch.oid 1234567890abcdef\n# branch.head main\n# branch.upstream origin/main\n" +
				"# branch.ab +2 -3\n1 .M N... 100644 100644 100644 abc abc compose.yaml\n? new.txt\n",
			want: homelab.RepoStatus{Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 3},
		},
		{
			name: "detached head",
			out:  "# branch.oid 1234567890abcdef\n# branch.head (detached)\n",
			want: homelab.RepoStatus{Branch: "(detached)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatus([]byte(tt.out))
			if err != nil {
				t.Fatalf("parseStatus() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parseStatus() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseStatusErrors(t *testing.T) {
	tests := []struct {
		name string
		out  string
	}{
		{"no branch header", "? new.txt\n"},
		{"malformed ahead behind", "# branch.head main\n# branch.ab 2 3\n"},
		{"non-numeric ahead", "# branch.head main\n# branch.ab +x -0\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := parseStatus([]byte(tt.out)); err == nil {
				t.Errorf("parseStatus(%q) = %+v, want error", tt.out, got)
			}
		})
	}
}

func TestStatusAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	upstream := t.TempDir()
	git(t, upstream, "init", "-b", "main")
	commit(t, upstream, "a.txt")

	clone := filepath.Join(t.TempDir(), "clone")
	git(t, upstream, "clone", upstream, clone)
	commit(t, clone, "b.txt")
	commit(t, clone, "c.txt")
	commit(t, upstream, "d.txt")
	git(t, clone, "fetch", "origin")

	tests := []struct {
		name string
		dir  string
		want homelab.RepoStatus
	}{
		{"no upstream", upstream, homelab.RepoStatus{Branch: "main"}},
		{
			"clone ahead and behind",
			clone,
			homelab.RepoStatus{Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(tt.dir)
			if r.Root() != tt.dir {
				t.Errorf("Root() = %q, want %q", r.Root(), tt.dir)
			}
			got, err := r.Status(t.Context())
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Status() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStatusNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "git", "status", "--porcelain=v2", "--branch")
	cmd.Dir = dir
	cmd.Env = isolatedEnv(dir)
	if err := cmd.Run(); err == nil {
		t.Skip("temp dir is inside a git repo")
	}
	if got, err := New(dir).Status(t.Context()); err == nil {
		t.Errorf("Status() on a non-repo = %+v, want error", got)
	}
}

// git runs a git command in dir with the user's and system git config ignored, so signing
// and hooks configured on the developer's machine cannot interfere.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = isolatedEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-m", "add "+name)
}

func isolatedEnv(home string) []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+home,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
}
