package gitrepo

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestNewDeployKey(t *testing.T) {
	priv, line, err := NewDeployKey("truenas-orchestrator@nas")
	if err != nil {
		t.Fatalf("NewDeployKey() error = %v", err)
	}
	pem := priv.Reveal()
	if !strings.HasPrefix(pem, "-----BEGIN OPENSSH PRIVATE KEY-----\n") {
		t.Errorf("private key does not start with an OpenSSH PEM header")
	}
	signer, err := ssh.ParsePrivateKey([]byte(pem))
	if err != nil {
		t.Fatalf("ParsePrivateKey() error = %v", err)
	}
	if _, ok := signer.PublicKey().(ssh.CryptoPublicKey).CryptoPublicKey().(ed25519.PublicKey); !ok {
		t.Errorf("key type %s, want ed25519", signer.PublicKey().Type())
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatalf("ParseAuthorizedKey(%q) error = %v", line, err)
	}
	if comment != "truenas-orchestrator@nas" || !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Errorf("authorized key %q, want an ssh-ed25519 line with the comment", line)
	}
	if string(pub.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Errorf("the public key does not match the private key")
	}
	fp, err := homelab.SSHFingerprint(line)
	if err != nil || fp != ssh.FingerprintSHA256(pub) {
		t.Errorf("homelab.SSHFingerprint = %q, %v, want %q", fp, err, ssh.FingerprintSHA256(pub))
	}

	_, other, err := NewDeployKey("")
	if err != nil || other == line || strings.Count(other, " ") != 1 {
		t.Errorf("second key %q, %v: want a different key with no comment", other, err)
	}
}
