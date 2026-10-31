package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"

	"github.com/danielgtaylor/huma/v2"

	"github.com/cg-homelab/truenas-orchestrator/internal/gitrepo"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// deployKeyComment is the comment on a generated deploy key's public half.
const deployKeyComment = "truenas-orchestrator"

// msgNoStateStorage is the 422 when the state dataset is not configured.
const msgNoStateStorage = "ORCH_STATE_STORAGE is not set: the deploy key lives on the state dataset"

// errNoHost is returned when the server was built without a storage host.
var errNoHost = errors.New("no storage host configured")

// deployKeyDeps are what the deploy key operations need.
type deployKeyDeps struct {
	host homelab.SetupHost
	// stateRoot is the configured ORCH_STATE_STORAGE, unchecked.
	stateRoot string
	gen       homelab.KeyGenerator
}

// root returns the state dataset path, or "" when it is not configured as an absolute path.
func (d deployKeyDeps) root() string {
	if !path.IsAbs(d.stateRoot) || path.Clean(d.stateRoot) == "/" {
		return ""
	}
	return path.Clean(d.stateRoot)
}

// DeployKeyView is the public side of the deploy key. The private half is never returned.
type DeployKeyView struct {
	Exists      bool   `json:"exists"`
	PublicKey   string `json:"public_key" doc:"The authorized_keys line to add to the repo as a read-only deploy key; empty when none exists"`
	Fingerprint string `json:"fingerprint" doc:"OpenSSH SHA256 fingerprint, e.g. SHA256:...; empty when none exists"`
	Path        string `json:"path" doc:"Where the private key lives (or will); empty when ORCH_STATE_STORAGE is not set"`
}

func newDeployKeyView(k homelab.DeployKey) DeployKeyView {
	return DeployKeyView{Exists: true, PublicKey: k.PublicKey, Fingerprint: k.Fingerprint, Path: k.Path}
}

// deployKeyError maps a deploy key error. Domain validation messages name paths only.
func deployKeyError(ctx context.Context, logger *slog.Logger, err error) error {
	if errors.Is(err, homelab.ErrValidation) {
		return huma.Error422UnprocessableEntity(err.Error())
	}
	return apiError(ctx, logger, err)
}

// GetDeployKeyOutput is the get-deploy-key response.
type GetDeployKeyOutput struct {
	Body DeployKeyView
}

var opGetDeployKey = huma.Operation{
	OperationID: "get-deploy-key",
	Method:      http.MethodGet,
	Path:        "/api/environment/deploy-key",
	Summary:     "Get the public half of the SSH deploy key",
	Tags:        []string{"environment"},
	Security:    authenticated(),
	Errors:      []int{http.StatusUnprocessableEntity},
}

func handleGetDeployKey(
	logger *slog.Logger, d deployKeyDeps,
) func(context.Context, *struct{}) (*GetDeployKeyOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*GetDeployKeyOutput, error) {
		if d.host == nil {
			return nil, apiError(ctx, logger, errNoHost)
		}
		root := d.root()
		if root == "" {
			return &GetDeployKeyOutput{Body: DeployKeyView{}}, nil
		}
		k, err := homelab.ReadDeployKey(ctx, d.host, root)
		switch {
		case errors.Is(err, homelab.ErrNotFound):
			return &GetDeployKeyOutput{Body: DeployKeyView{Path: path.Join(root, homelab.DeployKeyRel)}}, nil
		case err != nil:
			return nil, deployKeyError(ctx, logger, err)
		}
		return &GetDeployKeyOutput{Body: newDeployKeyView(k)}, nil
	}
}

// CreateDeployKeyOutput is the create-deploy-key response.
type CreateDeployKeyOutput struct {
	Body DeployKeyView
}

var opCreateDeployKey = huma.Operation{
	OperationID: "create-deploy-key",
	Method:      http.MethodPost,
	Path:        "/api/environment/deploy-key",
	Summary:     "Generate the SSH deploy key the orchestrator fetches the repo with",
	Description: "Generates an ed25519 key on the state dataset and returns its public half. An existing key " +
		"is never replaced: that is a 409.",
	Tags:          []string{"environment"},
	Security:      authenticated(),
	DefaultStatus: http.StatusCreated,
	Errors:        []int{http.StatusConflict, http.StatusUnprocessableEntity},
}

func handleCreateDeployKey(
	logger *slog.Logger, d deployKeyDeps, audit *auditor,
) func(context.Context, *struct{}) (*CreateDeployKeyOutput, error) {
	return func(ctx context.Context, _ *struct{}) (*CreateDeployKeyOutput, error) {
		if d.host == nil {
			return nil, apiError(ctx, logger, errNoHost)
		}
		root := d.root()
		if root == "" {
			return nil, huma.Error422UnprocessableEntity(msgNoStateStorage)
		}
		gen := d.gen
		if gen == nil {
			gen = gitrepo.NewDeployKey
		}
		k, created, err := homelab.EnsureDeployKey(ctx, d.host, root, deployKeyComment, gen)
		switch {
		case errors.Is(err, homelab.ErrConflict):
			return nil, huma.Error409Conflict(err.Error()) // names the stray public key's path
		case err != nil:
			return nil, deployKeyError(ctx, logger, err)
		case !created:
			return nil, huma.Error409Conflict("a deploy key already exists at " + k.Path + "; it is never replaced")
		}
		audit.record(ctx, auditDeployKeyCreate, k.Path, map[string]string{"path": k.Path, "fingerprint": k.Fingerprint})
		logger.InfoContext(ctx, "deploy key created", "path", k.Path, "fingerprint", k.Fingerprint)
		return &CreateDeployKeyOutput{Body: newDeployKeyView(k)}, nil
	}
}
