// Package truenas talks to the TrueNAS SCALE middleware. Client is the real JSON-RPC client
// (websocket transport arrives in M4); Fake is an in-process implementation for tests and dev.
package truenas

import (
	"context"
	"fmt"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Client speaks JSON-RPC 2.0 to the TrueNAS middleware.
//
// M0 stub: it holds its configuration only and makes no calls.
type Client struct {
	url string
	key homelab.Secret
}

var _ homelab.TrueNAS = (*Client)(nil)

// NewClient returns a Client for the middleware at url, authenticating with key.
func NewClient(url string, key homelab.Secret) *Client {
	return &Client{url: url, key: key}
}

// SystemVersion returns the TrueNAS version. Not implemented until M4.
func (c *Client) SystemVersion(ctx context.Context) (string, error) {
	_ = ctx
	return "", fmt.Errorf("truenas %s: system version: not implemented until M4", c.url)
}
