package gitrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSSHEnv(t *testing.T) {
	// A directory whose name has a space, a % (an ssh token), a double quote and a single quote.
	dir := filepath.Join(t.TempDir(), `state dir %h "q" it's`)
	if err := os.MkdirAll(filepath.Join(dir, "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "ssh", "id_ed25519")
	kh := filepath.Join(dir, "ssh", "known_hosts")
	if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(dir)
	// For ssh: % doubled, " escaped, all double-quoted. For the shell: ' closed, escaped, reopened.
	sshDir := parent + `/state dir %%h \"q\" it'\''s`
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{name: "no key configured", opts: Options{}, want: nil},
		{name: "key file missing", opts: Options{SSHKey: key + ".absent", KnownHosts: kh}, want: nil},
		{name: "key is a directory", opts: Options{SSHKey: dir}, want: nil},
		{
			name: "key and known_hosts",
			opts: Options{SSHKey: key, KnownHosts: kh},
			want: []string{`GIT_SSH_COMMAND=ssh -o 'IdentityFile="` + sshDir + `/ssh/id_ed25519"' -o IdentitiesOnly=yes` +
				` -o 'UserKnownHostsFile="` + sshDir + `/ssh/known_hosts"' -o StrictHostKeyChecking=accept-new`},
		},
		{
			name: "key without known_hosts",
			opts: Options{SSHKey: key},
			want: []string{`GIT_SSH_COMMAND=ssh -o 'IdentityFile="` + sshDir + `/ssh/id_ed25519"' -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewWithOptions(t.TempDir(), tt.opts).sshEnv()
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("sshEnv() mismatch (-want +got):\n%s", diff)
			}
		})
	}
	if got := New(t.TempDir()).sshEnv(); got != nil {
		t.Errorf("New(...).sshEnv() = %q, want nil", got)
	}

	// The real shell and ssh read the paths back intact: ssh -G prints its resolved configuration
	// without connecting (IdentityFile before % expansion, UserKnownHostsFile after it).
	t.Run("ssh resolves the paths", func(t *testing.T) {
		if _, err := exec.LookPath("ssh"); err != nil {
			t.Skip("ssh not installed")
		}
		env := NewWithOptions(t.TempDir(), Options{SSHKey: key, KnownHosts: kh}).sshEnv()
		cmd := strings.TrimPrefix(env[0], "GIT_SSH_COMMAND=") + " -F /dev/null -G example.invalid"
		out, err := exec.CommandContext(t.Context(), "sh", "-c", cmd).CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G: %v\n%s", err, out)
		}
		got := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(line, " "); ok && (k == "identityfile" || k == "userknownhostsfile") {
				got[k] = v
			}
		}
		want := map[string]string{
			"identityfile":       strings.ReplaceAll(key, "%", "%%"),
			"userknownhostsfile": kh,
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("ssh -G mismatch (-want +got):\n%s", diff)
		}
	})
}

// TestRunStreamingAddsEnv checks that the env Fetch computes reaches git: GIT_CONFIG_PARAMETERS
// stands in for GIT_SSH_COMMAND, which only a network fetch would exercise.
func TestRunStreamingAddsEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var l lines
	err := runStreaming(t.Context(), t.TempDir(), []string{"GIT_CONFIG_PARAMETERS='orch.probe=from-env'"}, l.add, "config", "--get", "orch.probe")
	if err != nil {
		t.Fatalf("runStreaming() error = %v", err)
	}
	if got := l.joined(); got != "from-env" {
		t.Errorf("git saw %q, want from-env", got)
	}
}
