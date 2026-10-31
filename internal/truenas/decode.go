package truenas

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// The wire structs below mirror the api/v25_10_0 result models. Client, Fake (scenario seeds are
// raw query results) and Smoke all decode through them, so the three cannot drift apart.

type wireDockerStatus struct {
	Status      string `json:"status"`
	Description string `json:"description"`
}

// wireProp is a dataset property object; only value is read.
type wireProp struct {
	Value json.RawMessage `json:"value"`
}

// upper returns the property value upper-cased: the middleware upper-cases acltype, aclmode,
// atime and compression itself, recordsize is "128K" already, and upper-casing on decode keeps the
// comparison right if that ever changes.
func (p *wireProp) upper() string {
	if p == nil || len(p.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(p.Value, &s) == nil {
		return strings.ToUpper(s)
	}
	if string(p.Value) == "null" {
		return ""
	}
	return strings.ToUpper(string(p.Value))
}

type wireDataset struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Pool        string    `json:"pool"`
	Encrypted   bool      `json:"encrypted"`
	Locked      bool      `json:"locked"`
	Mountpoint  *string   `json:"mountpoint"`
	ACLType     *wireProp `json:"acltype"`
	ACLMode     *wireProp `json:"aclmode"`
	Atime       *wireProp `json:"atime"`
	Compression *wireProp `json:"compression"`
	RecordSize  *wireProp `json:"recordsize"`
}

// wireUserGroup is the primary group row embedded in a user entry (datastore key names).
type wireUserGroup struct {
	ID    int    `json:"id"`
	GID   int    `json:"bsdgrp_gid"`
	Group string `json:"bsdgrp_group"`
}

type wireUser struct {
	ID       int            `json:"id"`
	UID      int            `json:"uid"`
	Username string         `json:"username"`
	Builtin  bool           `json:"builtin"`
	Local    bool           `json:"local"`
	Group    *wireUserGroup `json:"group"`
}

type wireGroup struct {
	ID      int    `json:"id"`
	GID     int    `json:"gid"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	Local   bool   `json:"local"`
}

type wireStat struct {
	Type         string `json:"type"`
	UID          int    `json:"uid"`
	GID          int    `json:"gid"`
	Mode         uint32 `json:"mode"`
	ACL          bool   `json:"acl"`
	IsMountpoint bool   `json:"is_mountpoint"`
	Realpath     string `json:"realpath"`
}

type wireNetwork struct {
	Name   *string        `json:"name"`
	ID     *string        `json:"id"`
	Driver *string        `json:"driver"`
	Labels map[string]any `json:"labels"`
}

type wireApp struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	CustomApp bool   `json:"custom_app"`
}

// wireJob is a core.get_jobs entry, read only by the legacy-jobs fallback.
type wireJob struct {
	ID      int64   `json:"id"`
	State   string  `json:"state"`
	Error   *string `json:"error"`
	ExcInfo *struct {
		Type  *string         `json:"type"`
		Errno *int            `json:"errno"`
		Extra json.RawMessage `json:"extra"`
	} `json:"exc_info"`
}

type wireLogin struct {
	ResponseType string `json:"response_type"`
}

// composeProjectLabel is the label docker compose puts on the networks it creates.
const composeProjectLabel = "com.docker.compose.project"

// Required result keys, checked by Smoke so that a renamed field fails loudly instead of decoding
// as a zero value.
var (
	requiredDatasetKeys = []string{
		"id", "name", "pool", "encrypted", "locked", "mountpoint",
		"acltype", "aclmode", "atime", "compression", "recordsize",
	}
	requiredUserKeys      = []string{"id", "uid", "username", "builtin", "local", "group"}
	requiredUserGroupKeys = []string{"id", "bsdgrp_gid", "bsdgrp_group"}
	requiredGroupKeys     = []string{"id", "gid", "name", "builtin", "local"}
	requiredStatKeys      = []string{"type", "uid", "gid", "mode", "acl", "is_mountpoint"}
	requiredNetworkKeys   = []string{"name", "id", "driver", "labels"}
	requiredAppKeys       = []string{"name", "state", "custom_app"}
)

func decodeString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("decode string: %w", err)
	}
	return s, nil
}

func decodeDockerStatus(raw json.RawMessage) (homelab.DockerStatus, error) {
	var w wireDockerStatus
	if err := json.Unmarshal(raw, &w); err != nil {
		return homelab.DockerStatus{}, fmt.Errorf("decode docker status: %w", err)
	}
	return homelab.DockerStatus{Status: w.Status, Description: w.Description}, nil
}

func decodeDatasets(raw json.RawMessage) ([]homelab.Dataset, error) {
	var ws []wireDataset
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("decode datasets: %w", err)
	}
	out := make([]homelab.Dataset, 0, len(ws))
	for _, w := range ws {
		d := homelab.Dataset{
			Name:      w.Name,
			Encrypted: w.Encrypted,
			Locked:    w.Locked,
			Props: homelab.DatasetProps{
				ACLType:     w.ACLType.upper(),
				ACLMode:     w.ACLMode.upper(),
				Atime:       w.Atime.upper(),
				Compression: w.Compression.upper(),
				RecordSize:  w.RecordSize.upper(),
			},
		}
		if w.Mountpoint != nil {
			d.Mountpoint = *w.Mountpoint
		}
		out = append(out, d)
	}
	return out, nil
}

func decodeUsers(raw json.RawMessage) ([]homelab.User, error) {
	var ws []wireUser
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	out := make([]homelab.User, 0, len(ws))
	for _, w := range ws {
		u := homelab.User{ID: w.ID, UID: w.UID, Name: w.Username, Builtin: w.Builtin}
		if w.Group != nil {
			u.GID = w.Group.GID
		}
		out = append(out, u)
	}
	return out, nil
}

func decodeGroups(raw json.RawMessage) ([]homelab.Group, error) {
	var ws []wireGroup
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("decode groups: %w", err)
	}
	out := make([]homelab.Group, 0, len(ws))
	for _, w := range ws {
		out = append(out, homelab.Group{ID: w.ID, GID: w.GID, Name: w.Name, Builtin: w.Builtin})
	}
	return out, nil
}

func decodeStat(raw json.RawMessage) (homelab.PathStat, error) {
	var w wireStat
	if err := json.Unmarshal(raw, &w); err != nil {
		return homelab.PathStat{}, fmt.Errorf("decode stat: %w", err)
	}
	return homelab.PathStat{
		Exists:       true,
		Type:         homelab.PathType(strings.ToUpper(w.Type)),
		UID:          w.UID,
		GID:          w.GID,
		Mode:         homelab.FileMode(w.Mode & 0o7777),
		ExtendedACL:  w.ACL,
		IsMountpoint: w.IsMountpoint,
	}, nil
}

func decodeNetworks(raw json.RawMessage) ([]homelab.Network, error) {
	var ws []wireNetwork
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("decode networks: %w", err)
	}
	out := make([]homelab.Network, 0, len(ws))
	for _, w := range ws {
		n := homelab.Network{Name: deref(w.Name), ID: deref(w.ID), Driver: deref(w.Driver)}
		if p, ok := w.Labels[composeProjectLabel].(string); ok {
			n.ComposeProject = p
		}
		out = append(out, n)
	}
	return out, nil
}

func decodeApps(raw json.RawMessage) ([]homelab.App, error) {
	var ws []wireApp
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("decode apps: %w", err)
	}
	out := make([]homelab.App, 0, len(ws))
	for _, w := range ws {
		out = append(out, homelab.App{Name: w.Name, State: w.State, Custom: w.CustomApp})
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// missingKeys returns the keys of want absent from the JSON object raw, or every key when raw is
// not an object.
func missingKeys(raw json.RawMessage, want []string) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return want
	}
	var missing []string
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}
