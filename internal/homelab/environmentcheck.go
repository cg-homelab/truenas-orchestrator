package homelab

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ItemKind is what a readiness item describes.
type ItemKind string

// Readiness item kinds, in the order CheckEnvironment reports them.
const (
	ItemVersion      ItemKind = "version"
	ItemDocker       ItemKind = "docker"
	ItemGlobalEnv    ItemKind = "global_env"
	ItemGroup        ItemKind = "group"
	ItemUser         ItemKind = "user"
	ItemIdentity     ItemKind = "identity"
	ItemDataset      ItemKind = "dataset"
	ItemStateDataset ItemKind = "state_dataset"
	ItemPermissions  ItemKind = "permissions"
	ItemMedia        ItemKind = "media"
	ItemNetworks     ItemKind = "networks"
	ItemNetworksApp  ItemKind = "networks_app"
	ItemSelfApp      ItemKind = "self_app"
	ItemDeployKey    ItemKind = "deploy_key"
)

// ItemState is how an item compares with the end state.
type ItemState string

// Item states.
const (
	// ItemOK matches the end state.
	ItemOK ItemState = "ok"
	// ItemMissing does not match, and the bootstrap plan fixes it (the deploy key: create-deploy-key).
	ItemMissing ItemState = "missing"
	// ItemWrong does not match and the plan cannot fix it; the message says what to do by hand.
	// It blocks a run.
	ItemWrong ItemState = "wrong"
	// ItemDiffers does not match in a way that is reported but accepted.
	ItemDiffers ItemState = "differs"
	// ItemUnknown could not be read. It blocks a run.
	ItemUnknown ItemState = "unknown"
)

// ReadinessItem is one thing the readiness check compared.
type ReadinessItem struct {
	Kind ItemKind
	// Target names it: a path, a dataset, an account or an app.
	Target string
	State  ItemState
	// Message explains a state other than ok, or what was found; never a secret.
	Message string
}

// VersionStatus is the TrueNAS version against the supported range.
type VersionStatus struct {
	// Detected is the version as parsed, e.g. "25.10.1"; "" when it could not be read.
	Detected string
	// Supported is SupportedTrueNAS.String().
	Supported string
	OK        bool
}

// BootstrapStepKind is what a bootstrap step changes.
type BootstrapStepKind string

// Bootstrap step kinds, in execution order.
const (
	StepGroupCreate    BootstrapStepKind = "group_create"
	StepUserCreate     BootstrapStepKind = "user_create"
	StepDatasetCreate  BootstrapStepKind = "dataset_create"
	StepPermissionsSet BootstrapStepKind = "permissions_set"
	StepAppCreate      BootstrapStepKind = "app_create"
)

// BootstrapAction says whether a step creates something or changes something that exists.
type BootstrapAction string

// Bootstrap actions.
const (
	BootstrapCreate BootstrapAction = "create"
	BootstrapChange BootstrapAction = "change"
)

// BootstrapStep is one TrueNAS mutation the bootstrap plan makes.
type BootstrapStep struct {
	Kind BootstrapStepKind
	// Target is the group or user name, the dataset name, the path or the app name.
	Target string
	Action BootstrapAction
	// Detail holds what the preview shows: ids, the mountpoint and properties of a dataset, and the
	// old and new owner and mode of a permissions step (from_owner, from_mode, from_acl, to_owner,
	// to_mode, stripacl). An empty from_owner means the directory is created by this plan.
	Detail map[string]string
}

// Readiness is the result of the environment-readiness check, and the bootstrap plan.
type Readiness struct {
	Version VersionStatus
	Items   []ReadinessItem
	// Steps is the plan, in execution order: group, user, datasets with parents first,
	// permissions, the networks app.
	Steps []BootstrapStep
	// Problems lists everything that forbids a run: the input problems and one line per wrong or
	// unknown item (except the deploy key, which is not part of the plan).
	Problems []string
	// Ready reports that no item is missing, wrong or unknown and there are no problems.
	Ready bool
	// Runnable reports that there are no problems, so the plan may run.
	Runnable bool
	// Digest is the hex SHA-256 of the steps: pass it to ApplyBootstrap to bind a run to the plan
	// that was reviewed.
	Digest string
}

// stepPayload is what a run needs to perform a step, parallel to Readiness.Steps.
type stepPayload struct {
	group   GroupSpec
	user    UserSpec
	dataset DatasetSpec
	perm    PermissionSpec
	app     AppSpec
}

// bootstrapPlan is a Readiness plus what a run needs.
type bootstrapPlan struct {
	readiness Readiness
	payloads  []stepPayload
}

// CheckEnvironment compares obs with spec. It is pure: everything it needs was read by
// ObserveEnvironment.
func CheckEnvironment(spec EnvironmentSpec, obs EnvironmentObserved) Readiness {
	return checkEnvironment(spec, obs).readiness
}

// checker accumulates items and steps.
type checker struct {
	spec EnvironmentSpec
	obs  EnvironmentObserved
	plan bootstrapPlan
	// datasetsPlanned holds the names of datasets a step creates, so shared parents appear once.
	datasetsPlanned map[string]bool
	// rootState holds the state of each root's dataset item.
	rootState map[string]ItemState
	// groupOK reports that a group with PGID exists or is planned, so the user can be created.
	groupOK bool
}

func checkEnvironment(spec EnvironmentSpec, obs EnvironmentObserved) bootstrapPlan {
	c := &checker{spec: spec, obs: obs, datasetsPlanned: map[string]bool{}, rootState: map[string]ItemState{}}
	c.plan.readiness.Problems = slices.Clone(spec.problems)
	c.checkVersion()
	c.checkDocker()
	c.checkGlobalEnv()
	c.checkGroup()
	c.checkUser()
	c.checkIdentity()
	for _, r := range spec.Roots {
		c.checkRoot(r)
	}
	c.checkState()
	for _, r := range spec.Roots {
		c.checkPermissions(r)
	}
	c.checkMedia()
	c.checkNetworks()
	c.checkSelfApp()
	c.checkDeployKey()

	r := &c.plan.readiness
	r.Runnable = len(r.Problems) == 0
	r.Ready = r.Runnable
	for _, it := range r.Items {
		switch it.State {
		case ItemMissing, ItemWrong, ItemUnknown:
			r.Ready = false
		case ItemOK, ItemDiffers:
		}
	}
	r.Digest = bootstrapDigest(r.Steps)
	if r.Items == nil {
		r.Items = []ReadinessItem{}
	}
	if r.Steps == nil {
		r.Steps = []BootstrapStep{}
	}
	if r.Problems == nil {
		r.Problems = []string{}
	}
	return c.plan
}

// item records an item; a wrong or unknown one also becomes a problem.
func (c *checker) item(kind ItemKind, target string, state ItemState, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if kind == ItemDataset {
		c.rootState[target] = state
	}
	r := &c.plan.readiness
	r.Items = append(r.Items, ReadinessItem{Kind: kind, Target: target, State: state, Message: msg})
	if state == ItemWrong || state == ItemUnknown {
		label := string(kind)
		if target != "" {
			label += " " + target
		}
		r.Problems = append(r.Problems, label+": "+msg)
	}
}

func (c *checker) step(s BootstrapStep, p stepPayload) {
	c.plan.readiness.Steps = append(c.plan.readiness.Steps, s)
	c.plan.payloads = append(c.plan.payloads, p)
}

func (c *checker) checkVersion() {
	r := &c.plan.readiness
	r.Version = VersionStatus{Supported: SupportedTrueNAS.String()}
	if c.obs.VersionErr != nil {
		c.item(ItemVersion, "", ItemUnknown, "could not read the TrueNAS version: %v", c.obs.VersionErr)
		return
	}
	r.Version.Detected = stripTrueNASPrefix(c.obs.Version)
	if err := CheckTrueNASVersion(c.obs.Version); err != nil {
		var uv *UnsupportedVersionError
		if errors.As(err, &uv) {
			r.Version.Detected = uv.Detected // truncated when unparsable
		}
		c.item(ItemVersion, r.Version.Detected, ItemWrong, "%v", err)
		return
	}
	r.Version.OK = true
	c.item(ItemVersion, r.Version.Detected, ItemOK, "TrueNAS %s is supported", r.Version.Detected)
}

func (c *checker) checkDocker() {
	switch {
	case c.obs.DockerErr != nil:
		c.item(ItemDocker, "", ItemUnknown, "could not read the Docker status: %v", c.obs.DockerErr)
	case c.obs.Docker.Status != "RUNNING":
		c.item(ItemDocker, "", ItemWrong, "Docker is %s, not RUNNING: choose an apps pool in Apps > Configuration", c.obs.Docker.Status)
	default:
		c.item(ItemDocker, "", ItemOK, "Docker is running")
	}
}

func (c *checker) checkGlobalEnv() {
	if len(c.spec.globalProblems) > 0 {
		c.item(ItemGlobalEnv, GlobalEnvPath, ItemWrong, "%s", strings.Join(c.spec.globalProblems, "; "))
		return
	}
	c.item(ItemGlobalEnv, GlobalEnvPath, ItemOK, "defines %s", strings.Join(globalEnvKeys, ", "))
}

// refusedID returns why id may never be the homelab identity, existing account or not, or "".
func refusedID(key string, id int) string {
	switch id {
	case 0:
		return fmt.Sprintf("%s 0 is root; choose an id between %d and %d", key, minHomelabID, maxHomelabID)
	case truenasAdminID:
		return fmt.Sprintf("%s %d is truenas_admin's id; choose an id between %d and %d", key, id, minHomelabID, maxHomelabID)
	}
	return ""
}

// builtinProblem is the refusal of an id that a builtin TrueNAS account holds.
func builtinProblem(key, kind, name string, id int) string {
	return fmt.Sprintf("%s %d is held by the builtin TrueNAS %s %s; choose an id between %d and %d",
		key, id, kind, name, minHomelabID, maxHomelabID)
}

// idProblem returns why id may not be created for an account, or "".
func idProblem(key string, id int) string {
	if p := refusedID(key, id); p != "" {
		return p
	}
	if id < minHomelabID || id > maxHomelabID {
		return fmt.Sprintf("%s %d is outside %d-%d, which TrueNAS keeps free for this, and no existing account holds it", key, id, minHomelabID, maxHomelabID)
	}
	return ""
}

func (c *checker) checkGroup() {
	gid := c.spec.PGID
	if gid < 0 {
		return
	}
	if c.obs.GroupsErr != nil {
		c.item(ItemGroup, HomelabAccount, ItemUnknown, "could not list groups: %v", c.obs.GroupsErr)
		return
	}
	if p := refusedID("PGID", gid); p != "" {
		c.item(ItemGroup, strconv.Itoa(gid), ItemWrong, "%s", p)
		return
	}
	for _, g := range c.obs.Groups {
		if g.GID == gid {
			if g.Builtin {
				c.item(ItemGroup, g.Name, ItemWrong, "%s", builtinProblem("PGID", "group", g.Name, gid))
				return
			}
			c.groupOK = true
			c.item(ItemGroup, g.Name, ItemOK, "group %s holds gid %d", g.Name, gid)
			return
		}
	}
	for _, g := range c.obs.Groups {
		if g.Name == HomelabAccount {
			c.item(ItemGroup, HomelabAccount, ItemWrong,
				"group %s exists with gid %d, not PGID %d: rename or remove it, or set PGID=%d", HomelabAccount, g.GID, gid, g.GID)
			return
		}
	}
	if p := idProblem("PGID", gid); p != "" {
		c.item(ItemGroup, HomelabAccount, ItemWrong, "%s", p)
		return
	}
	c.groupOK = true
	c.item(ItemGroup, HomelabAccount, ItemMissing, "no group holds gid %d; the plan creates %s", gid, HomelabAccount)
	c.step(BootstrapStep{
		Kind: StepGroupCreate, Target: HomelabAccount, Action: BootstrapCreate,
		Detail: map[string]string{"gid": strconv.Itoa(gid)},
	}, stepPayload{group: GroupSpec{Name: HomelabAccount, GID: gid}})
}

func (c *checker) checkUser() {
	uid, gid := c.spec.PUID, c.spec.PGID
	if uid < 0 {
		return
	}
	if c.obs.UsersErr != nil {
		c.item(ItemUser, HomelabAccount, ItemUnknown, "could not list users: %v", c.obs.UsersErr)
		return
	}
	if p := refusedID("PUID", uid); p != "" {
		c.item(ItemUser, strconv.Itoa(uid), ItemWrong, "%s", p)
		return
	}
	for _, u := range c.obs.Users {
		if u.UID != uid {
			continue
		}
		if u.Builtin {
			c.item(ItemUser, u.Name, ItemWrong, "%s", builtinProblem("PUID", "user", u.Name, uid))
			return
		}
		if gid >= 0 && u.GID != gid {
			c.item(ItemUser, u.Name, ItemDiffers, "user %s holds uid %d; its primary group is gid %d, not PGID %d", u.Name, uid, u.GID, gid)
			return
		}
		c.item(ItemUser, u.Name, ItemOK, "user %s holds uid %d", u.Name, uid)
		return
	}
	for _, u := range c.obs.Users {
		if u.Name == HomelabAccount {
			c.item(ItemUser, HomelabAccount, ItemWrong,
				"user %s exists with uid %d, not PUID %d: rename or remove it, or set PUID=%d", HomelabAccount, u.UID, uid, u.UID)
			return
		}
	}
	if p := idProblem("PUID", uid); p != "" {
		c.item(ItemUser, HomelabAccount, ItemWrong, "%s", p)
		return
	}
	if gid < 0 || !c.groupOK {
		c.item(ItemUser, HomelabAccount, ItemMissing, "no user holds uid %d; it can be created once its group exists", uid)
		return
	}
	c.item(ItemUser, HomelabAccount, ItemMissing, "no user holds uid %d; the plan creates %s", uid, HomelabAccount)
	c.step(BootstrapStep{
		Kind: StepUserCreate, Target: HomelabAccount, Action: BootstrapCreate,
		Detail: map[string]string{"uid": strconv.Itoa(uid), "gid": strconv.Itoa(gid)},
	}, stepPayload{user: UserSpec{Name: HomelabAccount, UID: uid, GID: gid, FullName: homelabFullName}})
}

// checkIdentity compares the uid and gid the orchestrator runs as with PUID and PGID: it writes
// secrets at 0600 and fetches the repo, so it must be the identity the containers run as (D32).
func (c *checker) checkIdentity() {
	if !c.spec.CheckIdentity || c.spec.PUID < 0 || c.spec.PGID < 0 {
		return
	}
	var notes []string
	state := ItemOK
	if c.spec.ProcessUID != c.spec.PUID {
		state = ItemWrong
		notes = append(notes, fmt.Sprintf("the orchestrator runs as uid %d but PUID is %d; the app must run as PUID:PGID (the M4 image runs as uid 1000)",
			c.spec.ProcessUID, c.spec.PUID))
	}
	if c.spec.ProcessGID != c.spec.PGID {
		if state == ItemOK {
			state = ItemDiffers
		}
		notes = append(notes, fmt.Sprintf("the orchestrator runs as gid %d but PGID is %d; the app should run as PUID:PGID",
			c.spec.ProcessGID, c.spec.PGID))
	}
	if len(notes) > 0 {
		c.item(ItemIdentity, "process", state, "%s", strings.Join(notes, "; "))
		return
	}
	c.item(ItemIdentity, "process", ItemOK, "the orchestrator runs as %d:%d, which is PUID:PGID", c.spec.ProcessUID, c.spec.PGID)
}

// propsDiff lists the properties of got that differ from want, e.g. "compression ZSTD (expected LZ4)".
func propsDiff(want, got DatasetProps) []string {
	var out []string
	for _, p := range []struct{ name, want, got string }{
		{"acltype", want.ACLType, got.ACLType},
		{"aclmode", want.ACLMode, got.ACLMode},
		{"atime", want.Atime, got.Atime},
		{"compression", want.Compression, got.Compression},
		{"recordsize", want.RecordSize, got.RecordSize},
	} {
		if !strings.EqualFold(p.want, p.got) {
			out = append(out, fmt.Sprintf("%s %s (expected %s)", p.name, p.got, p.want))
		}
	}
	return out
}

// statOf returns the observed stat of p and whether it was read.
func (c *checker) statOf(p string) (PathStat, bool, error) {
	if err, failed := c.obs.StatErrs[p]; failed {
		return PathStat{}, true, err
	}
	st, ok := c.obs.Stats[p]
	return st, ok, nil
}

func (c *checker) checkRoot(r StorageRoot) {
	target := r.Path
	if c.obs.DatasetsErr != nil {
		c.item(ItemDataset, target, ItemUnknown, "could not list datasets: %v", c.obs.DatasetsErr)
		return
	}
	if c.isPoolMountpoint(r.Path) {
		// Setting PUID:PGID 0750 here would lock every other uid out of the whole pool.
		c.item(ItemDataset, target, ItemWrong, "%s is /mnt or a pool's mountpoint: a storage root must be a child dataset, not the pool root, e.g. %s/apps", r.Path, r.Path)
		return
	}
	st, read, err := c.statOf(r.Path)
	switch {
	case err != nil:
		c.item(ItemDataset, target, ItemUnknown, "could not stat %s: %v", r.Path, err)
		return
	case !read:
		c.item(ItemDataset, target, ItemUnknown, "%s was not observed", r.Path)
		return
	case st.Exists:
		c.checkExistingRoot(r, st)
		return
	}

	parent, found := containingDataset(c.obs.Datasets, r.Path)
	switch {
	case !found:
		c.item(ItemDataset, target, ItemWrong, "%s is not under any mounted dataset; storage roots must be on a pool, e.g. /mnt/tank/...", r.Path)
		return
	case path.Clean(parent.Mountpoint) == r.Path:
		c.item(ItemDataset, target, ItemWrong, "dataset %s is mounted at %s, but the path does not exist", parent.Name, r.Path)
		return
	case parent.Locked:
		c.item(ItemDataset, target, ItemWrong, "%s would be inside the locked dataset %s: unlock it first", r.Path, parent.Name)
		return
	}
	// Every directory between the parent dataset's mountpoint and the root must be absent: an
	// existing plain directory there cannot become a dataset.
	var missing []string
	for a := r.Path; a != path.Clean(parent.Mountpoint); a = path.Dir(a) {
		if a != r.Path {
			ast, aread, aerr := c.statOf(a)
			switch {
			case aerr != nil:
				c.item(ItemDataset, target, ItemUnknown, "could not stat %s: %v", a, aerr)
				return
			case aread && ast.Exists:
				c.item(ItemDataset, target, ItemWrong,
					"%s does not exist and its nearest existing ancestor %s is a plain directory inside dataset %s, where a dataset cannot be created: create the datasets by hand or move %s",
					r.Path, a, parent.Name, a)
				return
			}
		}
		missing = append(missing, a)
	}
	slices.Reverse(missing)
	// A dataset that already holds a planned name but is not mounted there (unmounted, a legacy or
	// none mountpoint, locked, or mounted elsewhere) would make the create fail or be skipped.
	for _, p := range missing {
		name := datasetName(parent, p)
		if i := slices.IndexFunc(c.obs.Datasets, func(d Dataset) bool { return d.Name == name }); i >= 0 {
			where := "not mounted"
			if mp := c.obs.Datasets[i].Mountpoint; mp != "" {
				where = "mounted at " + mp
			}
			c.item(ItemDataset, target, ItemWrong, "dataset %s exists but is %s, not at %s: unlock it or fix its mountpoint", name, where, p)
			return
		}
	}
	c.item(ItemDataset, target, ItemMissing, "the plan creates the dataset %s", datasetName(parent, r.Path))
	for _, p := range missing {
		name := datasetName(parent, p)
		if c.datasetsPlanned[name] {
			continue
		}
		c.datasetsPlanned[name] = true
		if p != r.Path {
			c.step(BootstrapStep{
				Kind: StepDatasetCreate, Target: name, Action: BootstrapCreate,
				Detail: map[string]string{"mountpoint": p, "props": "inherit"},
			}, stepPayload{dataset: DatasetSpec{Name: name}})
			continue
		}
		props := r.Props
		c.step(BootstrapStep{
			Kind: StepDatasetCreate, Target: name, Action: BootstrapCreate,
			Detail: map[string]string{
				"mountpoint": p, "role": string(r.Role),
				"acltype": props.ACLType, "aclmode": props.ACLMode, "atime": props.Atime,
				"compression": props.Compression, "recordsize": props.RecordSize,
			},
		}, stepPayload{dataset: DatasetSpec{Name: name, Props: &props}})
	}
}

// isPoolMountpoint reports whether p is /mnt or the mountpoint of a pool's root dataset.
func (c *checker) isPoolMountpoint(p string) bool {
	if p == "/mnt" {
		return true
	}
	return slices.ContainsFunc(c.obs.Datasets, func(d Dataset) bool {
		return poolDataset(d) && isPoolRoot(d) && path.Clean(d.Mountpoint) == p
	})
}

// datasetName is the name of the dataset mounted at p under parent: parent's name plus the path
// below its mountpoint.
func datasetName(parent Dataset, p string) string {
	rel := strings.TrimPrefix(p, strings.TrimSuffix(path.Clean(parent.Mountpoint), "/")+"/")
	return parent.Name + "/" + rel
}

func (c *checker) checkExistingRoot(r StorageRoot, st PathStat) {
	target := r.Path
	if st.Type != PathDirectory {
		c.item(ItemDataset, target, ItemWrong, "%s is a %s, not a directory: move it away", r.Path, strings.ToLower(string(st.Type)))
		return
	}
	if d, ok := datasetAt(c.obs.Datasets, r.Path); ok {
		if d.Locked {
			c.item(ItemDataset, target, ItemWrong, "dataset %s is locked: unlock it", d.Name)
			return
		}
		if diff := propsDiff(r.Props, d.Props); len(diff) > 0 {
			c.item(ItemDataset, target, ItemDiffers, "dataset %s has %s; existing datasets are never changed", d.Name, strings.Join(diff, ", "))
			return
		}
		c.item(ItemDataset, target, ItemOK, "dataset %s", d.Name)
		return
	}
	parent, found := containingDataset(c.obs.Datasets, r.Path)
	switch {
	case !found:
		c.item(ItemDataset, target, ItemWrong, "%s is not under any mounted dataset; storage roots must be on a pool", r.Path)
	case parent.Locked:
		c.item(ItemDataset, target, ItemWrong, "%s is inside the locked dataset %s: unlock it", r.Path, parent.Name)
	default:
		c.item(ItemDataset, target, ItemDiffers,
			"%s is a plain directory inside dataset %s, not a dataset of its own; it works, but shares that dataset's snapshots and properties", r.Path, parent.Name)
	}
}

// rootUsable reports whether r exists as a usable directory or is created by this plan, with
// its stat when it exists.
func (c *checker) rootUsable(r StorageRoot) (PathStat, bool) {
	switch c.rootState[r.Path] {
	case ItemMissing:
		return PathStat{}, true
	case ItemOK, ItemDiffers:
		st, _, _ := c.statOf(r.Path)
		return st, true
	case ItemWrong, ItemUnknown:
	}
	return PathStat{}, false
}

func (c *checker) checkPermissions(r StorageRoot) {
	if c.spec.PUID < 0 || c.spec.PGID < 0 {
		return
	}
	st, ok := c.rootUsable(r)
	if !ok {
		return // the dataset item says why
	}
	toOwner := fmt.Sprintf("%d:%d", c.spec.PUID, c.spec.PGID)
	detail := map[string]string{
		"from_owner": "", "from_mode": "", "from_acl": "",
		"to_owner": toOwner, "to_mode": rootMode.String(), "stripacl": "true",
	}
	action := BootstrapCreate
	if st.Exists {
		if st.UID == c.spec.PUID && st.GID == c.spec.PGID && st.Mode == rootMode && !st.ExtendedACL {
			c.item(ItemPermissions, r.Path, ItemOK, "owned by %s with mode %s and no extended ACL", toOwner, rootMode)
			return
		}
		action = BootstrapChange
		detail["from_owner"] = fmt.Sprintf("%d:%d", st.UID, st.GID)
		detail["from_mode"] = st.Mode.String()
		detail["from_acl"] = strconv.FormatBool(st.ExtendedACL)
		acl := ""
		if st.ExtendedACL {
			acl = " with an extended ACL"
		}
		c.item(ItemPermissions, r.Path, ItemMissing, "owned by %d:%d with mode %s%s; the plan sets %s %s",
			st.UID, st.GID, st.Mode, acl, toOwner, rootMode)
	} else {
		c.item(ItemPermissions, r.Path, ItemMissing, "the plan sets %s %s on the new dataset", toOwner, rootMode)
	}
	c.step(BootstrapStep{Kind: StepPermissionsSet, Target: r.Path, Action: action, Detail: detail},
		stepPayload{perm: PermissionSpec{Path: r.Path, UID: c.spec.PUID, GID: c.spec.PGID, Mode: rootMode}})
}

func (c *checker) checkState() {
	p := c.spec.StateStorage
	if p == "" {
		return
	}
	st, read, err := c.statOf(p)
	switch {
	case err != nil:
		c.item(ItemStateDataset, p, ItemUnknown, "could not stat %s: %v", p, err)
		return
	case !read:
		c.item(ItemStateDataset, p, ItemUnknown, "%s was not observed", p)
		return
	case !st.Exists:
		c.item(ItemStateDataset, p, ItemWrong, "%s does not exist: create the state dataset as the bootstrap guide does (B4, B5)", p)
		return
	case st.Type != PathDirectory:
		c.item(ItemStateDataset, p, ItemWrong, "%s is a %s, not a directory", p, strings.ToLower(string(st.Type)))
		return
	}
	var notes []string
	if c.obs.DatasetsErr == nil {
		if _, ok := datasetAt(c.obs.Datasets, p); !ok {
			notes = append(notes, "it is a plain directory, not a dataset")
		}
	}
	if c.spec.PUID >= 0 && c.spec.PGID >= 0 && (st.UID != c.spec.PUID || st.GID != c.spec.PGID || st.Mode != stateMode || st.ExtendedACL) {
		notes = append(notes, fmt.Sprintf("owned by %d:%d with mode %s, expected %d:%d %s and no extended ACL; the orchestrator never changes it (B5)",
			st.UID, st.GID, st.Mode, c.spec.PUID, c.spec.PGID, stateMode))
	}
	if len(notes) > 0 {
		c.item(ItemStateDataset, p, ItemDiffers, "%s", strings.Join(notes, "; "))
		return
	}
	c.item(ItemStateDataset, p, ItemOK, "state dataset owned by %d:%d with mode %s", st.UID, st.GID, st.Mode)
}

func (c *checker) checkMedia() {
	p := c.spec.MediaStorage
	if p == "" {
		return
	}
	st, read, err := c.statOf(p)
	switch {
	case err != nil:
		c.item(ItemMedia, p, ItemUnknown, "could not stat %s: %v", p, err)
	case !read:
		c.item(ItemMedia, p, ItemUnknown, "%s was not observed", p)
	case !st.Exists:
		c.item(ItemMedia, p, ItemDiffers, "%s does not exist; stacks that mount media will fail until it does. The orchestrator never creates it", p)
	case st.Type != PathDirectory:
		c.item(ItemMedia, p, ItemDiffers, "%s is a %s, not a directory", p, strings.ToLower(string(st.Type)))
	default:
		c.item(ItemMedia, p, ItemOK, "exists")
	}
}

// networksProject is the compose project label of networks the NetworksApp app created.
var networksProject = "ix-" + NetworksApp

// networkCreateCommand is the manual fallback for one homelab network (D33).
func networkCreateCommand(name string) string {
	if name == "isolated" {
		return "docker network create --driver bridge --internal " + name
	}
	return "docker network create --driver bridge " + name
}

func (c *checker) checkNetworks() {
	target := strings.Join(HomelabNetworks, ",")
	if c.obs.NetworksErr != nil {
		c.item(ItemNetworks, target, ItemUnknown, "could not list Docker networks: %v", c.obs.NetworksErr)
		return
	}
	var app *App
	if c.obs.AppsErr == nil {
		for i := range c.obs.Apps {
			if c.obs.Apps[i].Name == NetworksApp {
				app = &c.obs.Apps[i]
			}
		}
	}
	var present, missing []string
	managed := true
	for _, name := range HomelabNetworks {
		idx := slices.IndexFunc(c.obs.Networks, func(n Network) bool { return n.Name == name })
		if idx < 0 {
			missing = append(missing, name)
			continue
		}
		present = append(present, name)
		if c.obs.Networks[idx].ComposeProject != networksProject {
			managed = false
		}
	}

	switch {
	case len(missing) == 0:
		c.item(ItemNetworks, target, ItemOK, "all four networks exist")
	case len(present) == 0 || managed:
		// Compose adopts networks that carry its own project label, so creating the app is right
		// both when none exist and when the existing ones are all ix-homelab-networks'.
		have := "none of the networks exist"
		if len(present) > 0 {
			have = fmt.Sprintf("%s are missing while %s carry the %s project label", strings.Join(missing, ", "), strings.Join(present, ", "), networksProject)
		}
		switch {
		case c.obs.AppsErr != nil:
			c.item(ItemNetworks, target, ItemUnknown, "%s, and the apps could not be listed: %v", have, c.obs.AppsErr)
		case app != nil && app.State == "DEPLOYING":
			c.item(ItemNetworks, target, ItemUnknown, "%s; the %s app is deploying: check again", have, NetworksApp)
		case app != nil:
			c.item(ItemNetworks, target, ItemWrong, "%s, but the %s app exists (state %s): start or redeploy it in Apps", have, NetworksApp, app.State)
		default:
			c.item(ItemNetworks, target, ItemMissing, "%s; the plan creates the %s app, which creates them", have, NetworksApp)
			c.planNetworksApp()
		}
	default:
		cmds := make([]string, 0, len(missing))
		for _, m := range missing {
			cmds = append(cmds, networkCreateCommand(m))
		}
		c.item(ItemNetworks, target, ItemWrong,
			"%s exist but %s are missing; compose cannot adopt networks it did not create, so create the missing ones on the TrueNAS host: %s",
			strings.Join(present, ", "), strings.Join(missing, ", "), strings.Join(cmds, "; "))
	}

	switch {
	case c.obs.AppsErr != nil:
		c.item(ItemNetworksApp, NetworksApp, ItemUnknown, "could not list apps: %v", c.obs.AppsErr)
	case app == nil:
		// Nothing to report: either the plan creates it (the networks item says so), or the
		// networks were made another way and the app is not needed.
	case app.State == "RUNNING":
		c.item(ItemNetworksApp, NetworksApp, ItemOK, "state %s", app.State)
	case app.State == "DEPLOYING" && len(missing) == 0:
		c.item(ItemNetworksApp, NetworksApp, ItemOK, "state %s", app.State)
	case app.State == "DEPLOYING":
		c.item(ItemNetworksApp, NetworksApp, ItemUnknown, "deploying; check again")
	case len(missing) == 0:
		c.item(ItemNetworksApp, NetworksApp, ItemDiffers,
			"state %s: start it in Apps; while it is stopped, a network no container uses is removed", app.State)
	default:
		c.item(ItemNetworksApp, NetworksApp, ItemWrong, "state %s: start it in Apps", app.State)
	}
}

func (c *checker) planNetworksApp() {
	sum := sha256.Sum256([]byte(NetworksAppCompose))
	c.item(ItemNetworksApp, NetworksApp, ItemMissing, "the plan creates it")
	c.step(BootstrapStep{
		Kind: StepAppCreate, Target: NetworksApp, Action: BootstrapCreate,
		Detail: map[string]string{
			"networks":       strings.Join(HomelabNetworks, ","),
			"compose_sha256": hex.EncodeToString(sum[:]),
		},
	}, stepPayload{app: AppSpec{Name: NetworksApp, Compose: NetworksAppCompose}})
}

func (c *checker) checkSelfApp() {
	if c.spec.SelfApp == "" {
		return
	}
	if c.obs.AppsErr != nil {
		c.item(ItemSelfApp, c.spec.SelfApp, ItemUnknown, "could not list apps: %v", c.obs.AppsErr)
		return
	}
	for _, a := range c.obs.Apps {
		if a.Name == c.spec.SelfApp {
			c.item(ItemSelfApp, a.Name, ItemOK, "state %s; the orchestrator never modifies its own app", a.State)
			return
		}
	}
	c.item(ItemSelfApp, c.spec.SelfApp, ItemDiffers, "no app named %s: the orchestrator is not running as that TrueNAS app (ORCH_SELF_APP)", c.spec.SelfApp)
}

func (c *checker) checkDeployKey() {
	if c.obs.DeployKey == "" || c.spec.StateStorage == "" {
		return
	}
	// The deploy key is not part of the plan (create-deploy-key makes it), so even an unreadable
	// one is not a problem that blocks a run; it only keeps the environment from being ready.
	it := ReadinessItem{Kind: ItemDeployKey, Target: path.Join(c.spec.StateStorage, DeployKeyRel)}
	switch c.obs.DeployKey {
	case ItemOK:
		it.State, it.Message = ItemOK, "exists"
	case ItemMissing:
		it.State, it.Message = ItemMissing, "no deploy key yet: generate one and add its public half to the repo as a read-only deploy key"
	case ItemWrong, ItemDiffers, ItemUnknown:
		it.State, it.Message = ItemUnknown, "could not read the deploy key; see the server log"
	}
	c.plan.readiness.Items = append(c.plan.readiness.Items, it)
}

// bootstrapDigest hashes each step's kind, target, action and details sorted by key.
func bootstrapDigest(steps []BootstrapStep) string {
	h := sha256.New()
	for _, s := range steps {
		fmt.Fprintf(h, "%s\x00%s\x00%s", s.Kind, s.Target, s.Action)
		for _, k := range slices.Sorted(maps.Keys(s.Detail)) {
			fmt.Fprintf(h, "\x00%s=%s", k, s.Detail[k])
		}
		fmt.Fprint(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
