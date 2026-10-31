package gitrepo

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

var _ homelab.KeyGenerator = NewDeployKey

// NewDeployKey generates an ed25519 deploy key. It returns the private key in OpenSSH PEM format,
// as ssh-keygen writes it with no passphrase, and the public key as an authorized_keys line ending
// in comment. It implements homelab.KeyGenerator.
func NewDeployKey(comment string) (private homelab.Secret, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return homelab.Secret{}, "", fmt.Errorf("generate ed25519 key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return homelab.Secret{}, "", fmt.Errorf("encode private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return homelab.Secret{}, "", fmt.Errorf("encode public key: %w", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment = strings.TrimSpace(comment); comment != "" {
		line += " " + comment
	}
	return homelab.NewSecret(string(pem.EncodeToMemory(block))), line, nil
}
