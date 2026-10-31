package homelab

import (
	"context"
	"fmt"
)

// TrueNAS is the subset of the TrueNAS middleware the domain calls (D22, D35). A value is one
// authenticated, version-checked session; truenas.Dial and truenas.Fake produce them.
type TrueNAS interface {
	// SystemVersion returns the TrueNAS version string.
	SystemVersion(ctx context.Context) (string, error)
	// DockerStatus returns the state of the Docker service backing apps.
	DockerStatus(ctx context.Context) (DockerStatus, error)
	// Datasets returns every filesystem dataset.
	Datasets(ctx context.Context) ([]Dataset, error)
	// CreateDataset creates one filesystem dataset.
	CreateDataset(ctx context.Context, spec DatasetSpec) error
	// Users and Groups return local accounts only.
	Users(ctx context.Context) ([]User, error)
	// CreateUser creates a local user.
	CreateUser(ctx context.Context, spec UserSpec) error
	// Groups returns local groups only.
	Groups(ctx context.Context) ([]Group, error)
	// CreateGroup creates a local group.
	CreateGroup(ctx context.Context, spec GroupSpec) error
	// Stat describes a host path without following a final symlink; absent is PathStat{} and nil.
	Stat(ctx context.Context, path string) (PathStat, error)
	// SetPermissions sets owner and mode on one directory, non-recursively, stripping any extended ACL.
	SetPermissions(ctx context.Context, spec PermissionSpec) error
	// Networks returns the Docker networks.
	Networks(ctx context.Context) ([]Network, error)
	// Apps returns the installed apps.
	Apps(ctx context.Context) ([]App, error)
	// CreateApp creates a custom compose app. Implementations refuse CheckAppMutation failures too.
	CreateApp(ctx context.Context, spec AppSpec) error
}

// TrueNASCredentials identifies the API user a session authenticates as.
type TrueNASCredentials struct {
	// Username is the TrueNAS user that owns the API key.
	Username string
	// APIKey is the user's API key.
	APIKey Secret
}

// DockerStatus is the state of the Docker service backing apps.
type DockerStatus struct {
	// Status is RUNNING, UNCONFIGURED and so on, as the middleware reports it.
	Status string
	// Description is the middleware's human-readable explanation.
	Description string
}

// Dataset is a filesystem dataset.
type Dataset struct {
	// Name is the full dataset name, e.g. "tank/apps".
	Name string
	// Mountpoint is the host path, or "" when unmounted.
	Mountpoint string
	// Encrypted reports whether the dataset is encrypted.
	Encrypted bool
	// Locked reports whether an encrypted dataset is locked.
	Locked bool
	// Props holds the dataset properties the orchestrator cares about.
	Props DatasetProps
}

// DatasetProps are the dataset properties the orchestrator sets and checks. Values are upper-case.
type DatasetProps struct {
	ACLType, ACLMode, Atime, Compression, RecordSize string
}

// DatasetSpec describes a dataset to create.
type DatasetSpec struct {
	// Name is the full dataset name.
	Name string
	// Props is nil to create with only the name and type (an ancestor inherits everything).
	Props *DatasetProps
}

// User is a local account.
type User struct {
	// ID is the middleware row id; UID is the Unix uid.
	ID, UID int
	// GID is the gid of the primary group.
	GID int
	// Name is the login name.
	Name string
	// Builtin reports a system account.
	Builtin bool
}

// UserSpec describes a user to create.
type UserSpec struct {
	// Name is the login name.
	Name string
	// UID is the Unix uid; GID is the primary group's gid, which the client resolves to the group row id.
	UID, GID int
	// FullName is the display name.
	FullName string
}

// Group is a local group.
type Group struct {
	// ID is the middleware row id; GID is the Unix gid.
	ID, GID int
	// Name is the group name.
	Name string
	// Builtin reports a system group.
	Builtin bool
}

// GroupSpec describes a group to create.
type GroupSpec struct {
	// Name is the group name.
	Name string
	// GID is the Unix gid.
	GID int
}

// PathType is the kind of a host path: "DIRECTORY", "FILE", "SYMLINK" or "OTHER".
type PathType string

// The values of PathType.
const (
	// PathDirectory is a directory.
	PathDirectory PathType = "DIRECTORY"
	// PathFile is a regular file.
	PathFile PathType = "FILE"
	// PathSymlink is a symbolic link.
	PathSymlink PathType = "SYMLINK"
	// PathOther is anything else, such as a FIFO or a device.
	PathOther PathType = "OTHER"
)

// PathStat describes a host path as TrueNAS sees it.
type PathStat struct {
	// Exists is false for an absent path, in which case the other fields are zero.
	Exists bool
	// Type is the kind of the path.
	Type PathType
	// UID and GID own the path.
	UID, GID int
	// Mode holds the permission bits only (& 0o7777).
	Mode FileMode
	// ExtendedACL reports an ACL beyond the mode bits.
	ExtendedACL bool
	// IsMountpoint reports that the path is a dataset mountpoint.
	IsMountpoint bool
}

// PermissionSpec describes an owner and mode to set on one directory.
type PermissionSpec struct {
	// Path is the absolute host path.
	Path string
	// UID and GID are the new owner.
	UID, GID int
	// Mode is the new permission bits, at most 0o777.
	Mode FileMode
}

// Network is a Docker network.
type Network struct {
	// Name, ID and Driver are as Docker reports them.
	Name, ID, Driver string
	// ComposeProject is the com.docker.compose.project label, or "" when absent.
	ComposeProject string
}

// App is an installed TrueNAS app.
type App struct {
	// Name is the app name and State its middleware state, e.g. RUNNING.
	Name, State string
	// Custom reports a custom compose app.
	Custom bool
}

// AppSpec describes a custom compose app to create.
type AppSpec struct {
	// Name is the app name.
	Name string
	// Compose is the custom_compose_config_string.
	Compose string
}

// CheckAppMutation returns an error wrapping ErrForbidden when target is the orchestrator's own app.
// Comparison is exact: app names are lower-case by the middleware's own rule.
func CheckAppMutation(selfApp, target string) error {
	if selfApp != "" && selfApp == target {
		return fmt.Errorf("app %q is the orchestrator itself and is never modified: %w", target, ErrForbidden)
	}
	return nil
}
