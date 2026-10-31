package homelab_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// testPublicKey and testFingerprint are an ed25519 key and what `ssh-keygen -lf` prints for it.
const (
	testPublicKey   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIByIo9YoYClKQMdLY1X2N3IdxKe3tE3/KPR7Fm9CVug3 test@example"
	testFingerprint = "SHA256:INUpED6XZ2bC0xDa/+L/2G+FMLxIh9eXfU3IgREgjlg"
	testPrivateKey  = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot a real key\n-----END OPENSSH PRIVATE KEY-----\n"
	stateRoot       = "/mnt/tank/state"
)

// fakeKeyGen returns the test key and counts its calls.
type fakeKeyGen struct {
	calls    int
	comments []string
	err      error
}

func (g *fakeKeyGen) generate(comment string) (homelab.Secret, string, error) {
	g.calls++
	g.comments = append(g.comments, comment)
	if g.err != nil {
		return homelab.Secret{}, "", g.err
	}
	return homelab.NewSecret(testPrivateKey), testPublicKey + "\n", nil
}

func TestSSHFingerprint(t *testing.T) {
	got, err := homelab.SSHFingerprint(testPublicKey)
	if err != nil || got != testFingerprint {
		t.Errorf("SSHFingerprint() = %q, %v, want %q", got, err, testFingerprint)
	}
	for _, bad := range []string{"", "ssh-ed25519", "ssh-ed25519 !!!not-base64"} {
		if _, err := homelab.SSHFingerprint(bad); !errors.Is(err, homelab.ErrValidation) {
			t.Errorf("SSHFingerprint(%q) error = %v, want ErrValidation", bad, err)
		}
	}
}

func TestEnsureDeployKey(t *testing.T) {
	h := newFakeHost(stateRoot)
	gen := &fakeKeyGen{}

	if _, err := homelab.ReadDeployKey(t.Context(), h, stateRoot); !errors.Is(err, homelab.ErrNotFound) {
		t.Fatalf("ReadDeployKey before creation error = %v, want ErrNotFound", err)
	}

	key, created, err := homelab.EnsureDeployKey(t.Context(), h, stateRoot, "truenas-orchestrator", gen.generate)
	if err != nil || !created {
		t.Fatalf("EnsureDeployKey = %v, %v, want created", created, err)
	}
	want := homelab.DeployKey{PublicKey: testPublicKey, Fingerprint: testFingerprint, Path: stateRoot + "/ssh/id_ed25519"}
	if diff := cmp.Diff(want, key); diff != "" {
		t.Errorf("key mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{
		"mkdir /mnt/tank/state/ssh", "create /mnt/tank/state/ssh/id_ed25519", "create /mnt/tank/state/ssh/id_ed25519.pub",
	}, h.writes); diff != "" {
		t.Errorf("writes mismatch (-want +got):\n%s", diff)
	}
	modes := map[string]homelab.FileMode{}
	for _, p := range []string{"/mnt/tank/state/ssh", "/mnt/tank/state/ssh/id_ed25519", "/mnt/tank/state/ssh/id_ed25519.pub"} {
		modes[p] = h.nodes[p].mode
	}
	if diff := cmp.Diff(map[string]homelab.FileMode{
		"/mnt/tank/state/ssh": 0o700, "/mnt/tank/state/ssh/id_ed25519": 0o600, "/mnt/tank/state/ssh/id_ed25519.pub": 0o644,
	}, modes); diff != "" {
		t.Errorf("modes mismatch (-want +got):\n%s", diff)
	}
	if got := string(h.nodes["/mnt/tank/state/ssh/id_ed25519"].data); got != testPrivateKey {
		t.Errorf("private key file holds %q", got)
	}
	if got := string(h.nodes["/mnt/tank/state/ssh/id_ed25519.pub"].data); got != testPublicKey+"\n" {
		t.Errorf("public key file holds %q", got)
	}

	// Never overwritten: a second call returns the same key and writes nothing.
	again, created, err := homelab.EnsureDeployKey(t.Context(), h, stateRoot, "truenas-orchestrator", gen.generate)
	if err != nil || created || again != key || gen.calls != 1 || len(h.writes) != 3 {
		t.Errorf("second EnsureDeployKey = %+v, %v, %v; generator calls %d, writes %d; want the same key and no writes",
			again, created, err, gen.calls, len(h.writes))
	}
	read, err := homelab.ReadDeployKey(t.Context(), h, stateRoot)
	if err != nil || read != key {
		t.Errorf("ReadDeployKey = %+v, %v, want %+v", read, err, key)
	}
}

func TestEnsureDeployKeyRefuses(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(h *fakeHost)
		genErr  error
		want    error
	}{
		{
			name:    "a public key without its private half",
			prepare: func(h *fakeHost) { h.file("/mnt/tank/state/ssh/id_ed25519.pub", testPublicKey, 0o644) },
			want:    homelab.ErrConflict,
		},
		{
			name:    "ssh is a file",
			prepare: func(h *fakeHost) { h.file("/mnt/tank/state/ssh", "", 0o600) },
			want:    homelab.ErrValidation,
		},
		{
			name: "a private key without its public half",
			prepare: func(h *fakeHost) {
				h.file("/mnt/tank/state/ssh/id_ed25519", testPrivateKey, 0o600)
			},
			want: homelab.ErrValidation,
		},
		{
			name:    "the private key is a symlink",
			prepare: func(h *fakeHost) { h.symlink("/mnt/tank/state/ssh/id_ed25519") },
			want:    homelab.ErrValidation,
		},
		{name: "the generator fails", genErr: errors.New("no entropy"), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFakeHost(stateRoot)
			if tt.prepare != nil {
				tt.prepare(h)
			}
			gen := &fakeKeyGen{err: tt.genErr}
			_, created, err := homelab.EnsureDeployKey(t.Context(), h, stateRoot, "c", gen.generate)
			if err == nil || created {
				t.Fatalf("EnsureDeployKey = %v, %v, want an error", created, err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error = %v, want %v", err, tt.want)
			}
			for _, w := range h.writes {
				if w != "mkdir /mnt/tank/state/ssh" {
					t.Errorf("wrote %s", w)
				}
			}
		})
	}
}

// Review 8: losing the race to create the private key is a conflict, even before the winner has
// written the public half.
func TestEnsureDeployKeyConcurrentCreate(t *testing.T) {
	h := newFakeHost(stateRoot)
	h.beforeWrite = func(op, p string) {
		if op == "create" && p == stateRoot+"/ssh/id_ed25519" && h.nodes[p] == nil {
			h.nodes[p] = &fakeNode{data: []byte("winner"), mode: 0o600}
		}
	}
	gen := &fakeKeyGen{}
	_, created, err := homelab.EnsureDeployKey(t.Context(), h, stateRoot, "c", gen.generate)
	if !errors.Is(err, homelab.ErrConflict) || errors.Is(err, homelab.ErrValidation) || created {
		t.Errorf("EnsureDeployKey = %v, %v, want an ErrConflict", created, err)
	}
	if string(h.nodes[stateRoot+"/ssh/id_ed25519"].data) != "winner" {
		t.Errorf("the winner's key was overwritten")
	}
}
