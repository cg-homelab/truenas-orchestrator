package homelab

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Paths of the deploy key and ssh's known_hosts, relative to the state dataset (D32, D34).
const (
	deployKeyDir = "ssh"
	// DeployKeyRel is the private key; ssh reads it for git fetch.
	DeployKeyRel = deployKeyDir + "/id_ed25519"
	// DeployKeyPubRel is the public half shown in the UI.
	DeployKeyPubRel = DeployKeyRel + ".pub"
	// KnownHostsRel is ssh's known_hosts for the repo's host, written on first fetch.
	KnownHostsRel = deployKeyDir + "/known_hosts"
)

// Modes of the deploy key files (D34).
const (
	deployKeyDirMode FileMode = 0o700
	deployKeyMode    FileMode = 0o600
	deployKeyPubMode FileMode = 0o644
)

// KeyGenerator makes a new SSH key pair: the private key in OpenSSH PEM format and the public key
// as an authorized_keys line. gitrepo.NewDeployKey implements it; key generation needs
// golang.org/x/crypto/ssh, which imports net, so it cannot live here.
type KeyGenerator func(comment string) (private Secret, authorizedKey string, err error)

// DeployKey is the public side of the deploy key. The private half never leaves its file.
type DeployKey struct {
	// PublicKey is the authorized_keys line, e.g. "ssh-ed25519 AAAA... comment".
	PublicKey string
	// Fingerprint is OpenSSH's SHA256 fingerprint, e.g. "SHA256:abc...".
	Fingerprint string
	// Path is the absolute path of the private key.
	Path string
}

// EnsureDeployKey returns the deploy key under stateRoot, generating it with gen when it is
// absent. created reports that this call wrote it. It writes through host only: ssh/ with mode
// 0700, the private key by exclusive create with mode 0600, then the public key with mode 0644.
// An existing key is never overwritten, and a public key without its private half is an error
// wrapping ErrConflict rather than something to replace.
func EnsureDeployKey(
	ctx context.Context, host SetupHost, stateRoot, comment string, gen KeyGenerator,
) (key DeployKey, created bool, err error) {
	switch k, err := ReadDeployKey(ctx, host, stateRoot); {
	case err == nil:
		return k, false, nil
	case !errors.Is(err, ErrNotFound):
		return DeployKey{}, false, err
	}
	info, err := host.Stat(ctx, stateRoot, deployKeyDir)
	switch {
	case err != nil:
		return DeployKey{}, false, fmt.Errorf("stat %s: %w", deployKeyDir, err)
	case !info.Exists:
		if err := host.Mkdir(ctx, stateRoot, deployKeyDir, deployKeyDirMode); err != nil && !errors.Is(err, ErrConflict) {
			return DeployKey{}, false, fmt.Errorf("create %s: %w", path.Join(stateRoot, deployKeyDir), err)
		}
	case !info.IsDir || info.IsSymlink:
		return DeployKey{}, false, fmt.Errorf("%s is not a directory: %w", path.Join(stateRoot, deployKeyDir), ErrValidation)
	}
	pub, err := host.Stat(ctx, stateRoot, DeployKeyPubRel)
	if err != nil {
		return DeployKey{}, false, fmt.Errorf("stat %s: %w", DeployKeyPubRel, err)
	}
	if pub.Exists {
		return DeployKey{}, false, fmt.Errorf("%s exists without its private key; remove it first: %w",
			path.Join(stateRoot, DeployKeyPubRel), ErrConflict)
	}

	private, authorized, err := gen(comment)
	if err != nil {
		return DeployKey{}, false, fmt.Errorf("generate the deploy key: %w", err)
	}
	authorized = strings.TrimSpace(authorized)
	fp, err := SSHFingerprint(authorized)
	if err != nil {
		return DeployKey{}, false, fmt.Errorf("generated public key: %w", err)
	}
	if err := host.CreateExclusive(ctx, stateRoot, DeployKeyRel, []byte(private.Reveal()), deployKeyMode); err != nil { // the key's point of use
		if errors.Is(err, ErrConflict) {
			// Created concurrently: report the one that won if its public half is already there,
			// and otherwise a conflict, never a "recreate the public key" validation error.
			if k, rerr := ReadDeployKey(ctx, host, stateRoot); rerr == nil {
				return k, false, nil
			}
			return DeployKey{}, false, fmt.Errorf("a deploy key at %s was created concurrently: %w", path.Join(stateRoot, DeployKeyRel), ErrConflict)
		}
		return DeployKey{}, false, fmt.Errorf("write %s: %w", path.Join(stateRoot, DeployKeyRel), err)
	}
	if err := host.CreateExclusive(ctx, stateRoot, DeployKeyPubRel, []byte(authorized+"\n"), deployKeyPubMode); err != nil {
		return DeployKey{}, false, fmt.Errorf("write %s: %w", path.Join(stateRoot, DeployKeyPubRel), err)
	}
	return DeployKey{PublicKey: authorized, Fingerprint: fp, Path: path.Join(stateRoot, DeployKeyRel)}, true, nil
}

// ReadDeployKey returns the deploy key under stateRoot, or an error wrapping ErrNotFound when the
// private key does not exist. The private key is only stat'ed, never read. A private key that is
// not a regular file, or one whose public half is missing or unparsable, is an error wrapping
// ErrValidation.
func ReadDeployKey(ctx context.Context, host SetupHost, stateRoot string) (DeployKey, error) {
	keyPath := path.Join(stateRoot, DeployKeyRel)
	info, err := host.Stat(ctx, stateRoot, DeployKeyRel)
	switch {
	case err != nil:
		return DeployKey{}, fmt.Errorf("stat %s: %w", keyPath, err)
	case !info.Exists:
		return DeployKey{}, fmt.Errorf("deploy key %s: %w", keyPath, ErrNotFound)
	case info.IsDir || info.IsSymlink:
		return DeployKey{}, fmt.Errorf("%s is not a regular file: %w", keyPath, ErrValidation)
	}
	b, err := host.ReadFile(ctx, stateRoot, DeployKeyPubRel)
	switch {
	case errors.Is(err, ErrNotFound):
		return DeployKey{}, fmt.Errorf("%s is missing; recreate it with ssh-keygen -y -f %s: %w",
			path.Join(stateRoot, DeployKeyPubRel), keyPath, ErrValidation)
	case err != nil:
		return DeployKey{}, fmt.Errorf("read %s: %w", path.Join(stateRoot, DeployKeyPubRel), err)
	}
	authorized := strings.TrimSpace(string(b))
	fp, err := SSHFingerprint(authorized)
	if err != nil {
		return DeployKey{}, fmt.Errorf("%s: %w", path.Join(stateRoot, DeployKeyPubRel), err)
	}
	return DeployKey{PublicKey: authorized, Fingerprint: fp, Path: keyPath}, nil
}

// SSHFingerprint returns OpenSSH's SHA256 fingerprint ("SHA256:" and the unpadded base64 of the
// SHA-256 of the key blob) of an authorized_keys line such as "ssh-ed25519 AAAA... comment".
// Options before the key type are not supported. A malformed line is an error wrapping
// ErrValidation.
func SSHFingerprint(authorizedKey string) (string, error) {
	fields := strings.Fields(authorizedKey)
	if len(fields) < 2 {
		return "", fmt.Errorf("not an authorized_keys line: %w", ErrValidation)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(blob) == 0 {
		return "", fmt.Errorf("the key of an authorized_keys line is not base64: %w", ErrValidation)
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}
