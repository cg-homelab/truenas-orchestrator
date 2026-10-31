package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestDeployKey(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	keyPath := filepath.Join(e.base, "state", "ssh", "id_ed25519")
	get := func() DeployKeyView {
		t.Helper()
		r := e.do(t, http.MethodGet, "/api/environment/deploy-key", nil, e.cookie)
		if r.status != http.StatusOK {
			t.Fatalf("GET deploy-key status = %d; body %s", r.status, r.body)
		}
		var v DeployKeyView
		decode(t, r, &v)
		return v
	}

	if v := get(); v != (DeployKeyView{Path: keyPath}) {
		t.Errorf("deploy key before creation = %+v, want only the path", v)
	}

	r := e.do(t, http.MethodPost, "/api/environment/deploy-key", nil, e.cookie)
	if r.status != http.StatusCreated {
		t.Fatalf("POST deploy-key status = %d, want 201; body %s", r.status, r.body)
	}
	var created DeployKeyView
	decode(t, r, &created)
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(created.PublicKey))
	if err != nil || comment != deployKeyComment || created.Fingerprint != ssh.FingerprintSHA256(pub) || !created.Exists || created.Path != keyPath {
		t.Fatalf("created key %+v (%v), want an ed25519 public key with its fingerprint", created, err)
	}
	if strings.Contains(r.body, "PRIVATE KEY") {
		t.Errorf("the response holds the private key")
	}
	for p, want := range map[string]os.FileMode{
		filepath.Dir(keyPath): 0o700 | os.ModeDir, keyPath: 0o600, keyPath + ".pub": 0o644,
	} {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode() != want {
			t.Errorf("%s mode %v, %v, want %v", p, fi.Mode(), err, want)
		}
	}
	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(priv); err != nil {
		t.Errorf("the private key file does not parse: %v", err)
	}

	if v := get(); v != created {
		t.Errorf("GET after creation = %+v, want %+v", v, created)
	}
	r = e.do(t, http.MethodPost, "/api/environment/deploy-key", nil, e.cookie)
	if r.status != http.StatusConflict || !strings.Contains(r.body, "never replaced") {
		t.Errorf("second POST = %d %s, want 409", r.status, r.body)
	}
	if again, _ := os.ReadFile(keyPath); string(again) != string(priv) {
		t.Errorf("the private key was rewritten")
	}

	entries, body := e.auditEntries(t, e.cookie)
	if len(entries) != 1 || entries[0].Action != auditDeployKeyCreate || entries[0].Target != keyPath ||
		entries[0].Details["fingerprint"] != created.Fingerprint || entries[0].Details["path"] != keyPath {
		t.Errorf("audit entries %+v, want one deploy_key.create", entries)
	}
	if strings.Contains(body, "PRIVATE KEY") || strings.Contains(e.logs.String(), "PRIVATE KEY") {
		t.Errorf("the private key reached the audit log or the server log")
	}
}

func TestDeployKeyNotConfigured(t *testing.T) {
	opts := envOptions{deps: func(d *Deps) { d.Environment = homelab.EnvironmentConfig{} }}
	e := newEnv(t, opts)
	e.setup(t)
	c := withCookie(e.login(t))
	r := e.do(t, http.MethodGet, "/api/environment/deploy-key", nil, c)
	if r.status != http.StatusOK || !strings.Contains(r.body, `"exists":false`) || !strings.Contains(r.body, `"path":""`) {
		t.Errorf("GET without ORCH_STATE_STORAGE = %d %s, want an absent key with no path", r.status, r.body)
	}
	r = e.do(t, http.MethodPost, "/api/environment/deploy-key", nil, c)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "ORCH_STATE_STORAGE") {
		t.Errorf("POST without ORCH_STATE_STORAGE = %d %s, want 422", r.status, r.body)
	}

	opts = envOptions{noHost: true}
	e = newEnv(t, opts)
	e.setup(t)
	c = withCookie(e.login(t))
	if r := e.do(t, http.MethodGet, "/api/environment/deploy-key", nil, c); r.status != http.StatusInternalServerError {
		t.Errorf("GET without a host = %d, want 500", r.status)
	}
}
