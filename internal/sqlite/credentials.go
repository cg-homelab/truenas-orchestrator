package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/sqlite/db"
)

// credentialTrueNAS is the credentials row holding the TrueNAS API key (D34).
const credentialTrueNAS = "truenas"

// TrueNASCredentials returns the stored TrueNAS API user and key, or an error wrapping
// homelab.ErrNotFound when none is stored. The key is a homelab.Secret from the scan onwards.
func (s *Store) TrueNASCredentials(ctx context.Context) (homelab.TrueNASCredentials, error) {
	row, err := s.q.GetCredential(ctx, credentialTrueNAS)
	if err != nil {
		return homelab.TrueNASCredentials{}, notFound("truenas credentials", err)
	}
	return homelab.TrueNASCredentials{Username: row.Username, APIKey: homelab.NewSecret(row.Secret)}, nil
}

// PutTrueNASCredentials stores creds, replacing any stored before.
func (s *Store) PutTrueNASCredentials(ctx context.Context, creds homelab.TrueNASCredentials, now time.Time) error {
	// The key's point of use: it is written to its row.
	err := s.q.PutCredential(ctx, db.PutCredentialParams{
		Name: credentialTrueNAS, Username: creds.Username, Secret: creds.APIKey.Reveal(), UpdatedAt: toMillis(now),
	})
	if err != nil {
		return fmt.Errorf("store truenas credentials: %w", err)
	}
	return nil
}

// DeleteTrueNASCredentials removes the stored TrueNAS credentials, or returns an error wrapping
// homelab.ErrNotFound when none are stored.
func (s *Store) DeleteTrueNASCredentials(ctx context.Context) error {
	n, err := s.q.DeleteCredential(ctx, credentialTrueNAS)
	if err != nil {
		return fmt.Errorf("delete truenas credentials: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("truenas credentials: %w", homelab.ErrNotFound)
	}
	return nil
}
