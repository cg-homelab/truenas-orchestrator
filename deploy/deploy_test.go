// Package deploy holds the files that install the orchestrator on a TrueNAS server: bootstrap.sh,
// its compose files and env example. This test ties them to the Go code and to the hand-install
// guide, because neither the script nor the guide can run on a development machine.
package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
	"github.com/cg-homelab/truenas-orchestrator/internal/truenas"
)

const (
	scriptPath = "bootstrap.sh"
	guidePath  = "../docs/hand-install.md"
)

func TestNetworksComposeMatchesGo(t *testing.T) {
	got, err := os.ReadFile("homelab-networks.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != homelab.NetworksAppCompose {
		t.Errorf("homelab-networks.yaml differs from homelab.NetworksAppCompose (-go +file):\n%s",
			cmp.Diff(homelab.NetworksAppCompose, string(got)))
	}
}

// payload is one `# payload:<name>` heredoc of bootstrap.sh.
type payload struct {
	name string
	line int // of the marker, for messages
	body string
}

// readPayloads extracts the payload heredocs laid out as bootstrap.sh's header describes: the
// marker line, then `cat <<EOF`, the body, and `EOF` at column 0.
func readPayloads(t *testing.T, script []byte) []payload {
	t.Helper()
	var (
		out   []payload
		lines []string
	)
	sc := bufio.NewScanner(bytes.NewReader(script))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(lines); i++ {
		name, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "# payload:")
		if !ok || name == "<name>" { // the header's description of the layout
			continue
		}
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) != "cat <<EOF" {
			t.Fatalf("%s:%d: payload:%s is not followed by `cat <<EOF`", scriptPath, i+1, name)
		}
		end := slices.Index(lines[i+2:], "EOF")
		if end < 0 {
			t.Fatalf("%s:%d: payload:%s has no EOF at column 0", scriptPath, i+1, name)
		}
		body := strings.Join(lines[i+2:i+2+end], "\n")
		out = append(out, payload{name: name, line: i + 1, body: body})
		i += 2 + end
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substitute replaces each ${VAR} with vars[VAR] verbatim, as the shell does. An unknown variable
// is an error, so a new placeholder needs an example value here.
func substitute(body string, vars map[string]string) (string, error) {
	var missing []string
	out := placeholderRe.ReplaceAllStringFunc(body, func(m string) string {
		name := placeholderRe.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no example value for %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// recorder is a homelab.TrueNAS that records the specs the orchestrator's bootstrap passes to each
// create call, then forwards them to the Fake.
type recorder struct {
	homelab.TrueNAS
	mu       sync.Mutex
	groups   []homelab.GroupSpec
	users    []homelab.UserSpec
	datasets []homelab.DatasetSpec
	perms    []homelab.PermissionSpec
	apps     []homelab.AppSpec
}

func (r *recorder) CreateGroup(ctx context.Context, spec homelab.GroupSpec) error {
	r.mu.Lock()
	r.groups = append(r.groups, spec)
	r.mu.Unlock()
	return r.TrueNAS.CreateGroup(ctx, spec)
}

func (r *recorder) CreateUser(ctx context.Context, spec homelab.UserSpec) error {
	r.mu.Lock()
	r.users = append(r.users, spec)
	r.mu.Unlock()
	return r.TrueNAS.CreateUser(ctx, spec)
}

func (r *recorder) CreateDataset(ctx context.Context, spec homelab.DatasetSpec) error {
	r.mu.Lock()
	r.datasets = append(r.datasets, spec)
	r.mu.Unlock()
	return r.TrueNAS.CreateDataset(ctx, spec)
}

func (r *recorder) SetPermissions(ctx context.Context, spec homelab.PermissionSpec) error {
	r.mu.Lock()
	r.perms = append(r.perms, spec)
	r.mu.Unlock()
	return r.TrueNAS.SetPermissions(ctx, spec)
}

func (r *recorder) CreateApp(ctx context.Context, spec homelab.AppSpec) error {
	r.mu.Lock()
	r.apps = append(r.apps, spec)
	r.mu.Unlock()
	return r.TrueNAS.CreateApp(ctx, spec)
}

// Example inputs, shared by the orchestrator's bootstrap run and the script's placeholders.
const (
	examplePUID       = 1000
	examplePGID       = 1000
	exampleGroupRowID = 45
	exampleHomelab    = "/mnt/tank/apps/homelab"
	exampleAppConfig  = "/mnt/tank/apps/appconfigs"
	exampleDatabase   = "/mnt/tank/databases"
	exampleMedia      = "/mnt/tank/media"
	exampleState      = "/mnt/tank/orchestrator"
)

// orchestratorSpecs runs the orchestrator's own bootstrap against the fresh Fake scenario and
// returns the specs it sent, so the script is compared with what the orchestrator really does.
func orchestratorSpecs(t *testing.T) *recorder {
	t.Helper()
	global, err := homelab.ParseDotEnv(fmt.Appendf(nil,
		"PUID=%d\nPGID=%d\nHOMELAB_STORAGE=%s\nAPPCONFIG_STORAGE=%s\nDATABASE_STORAGE=%s\nMEDIA_STORAGE=%s\n",
		examplePUID, examplePGID, exampleHomelab, exampleAppConfig, exampleDatabase, exampleMedia))
	if err != nil {
		t.Fatal(err)
	}
	spec, problems := homelab.NewEnvironmentSpec(homelab.EnvironmentConfig{
		SelfApp:      homelab.DefaultSelfApp,
		StateStorage: exampleState,
		Global:       global,
	})
	if len(problems) > 0 {
		t.Fatalf("NewEnvironmentSpec problems: %q", problems)
	}
	fake, err := truenas.LoadScenario(os.DirFS("../fixtures/truenas"), "fresh",
		truenas.FakeOptions{Mnt: "/mnt", SelfApp: homelab.DefaultSelfApp})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{TrueNAS: fake}
	if _, err := homelab.ApplyBootstrap(t.Context(), spec, rec, "", nil); err != nil {
		t.Fatalf("ApplyBootstrap on the fresh scenario: %v", err)
	}
	return rec
}

func TestPayloadsMatchEncoders(t *testing.T) {
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	rec := orchestratorSpecs(t)
	if len(rec.groups) != 1 || len(rec.users) != 1 || len(rec.apps) != 1 {
		t.Fatalf("bootstrap on fresh sent %d group, %d user and %d app creates, want 1 each",
			len(rec.groups), len(rec.users), len(rec.apps))
	}
	datasets := map[string]homelab.DatasetSpec{}
	for _, d := range rec.datasets {
		datasets[d.Name] = d
	}
	perms := map[string]homelab.PermissionSpec{}
	for _, p := range rec.perms {
		perms[p.Path] = p
	}
	need := func(name string, ok bool) {
		if !ok {
			t.Fatalf("bootstrap on fresh did not send %s; sent datasets %v, perms %v", name, rec.datasets, rec.perms)
		}
	}
	parent, ok := datasets["tank/apps"]
	need("dataset tank/apps", ok)
	root, ok := datasets["tank/apps/appconfigs"]
	need("dataset tank/apps/appconfigs", ok)
	database, ok := datasets["tank/databases"]
	need("dataset tank/databases", ok)
	perm, ok := perms[exampleAppConfig]
	need("setperm "+exampleAppConfig, ok)
	if parent.Props != nil || root.Props == nil || database.Props == nil {
		t.Fatalf("dataset props: parent %v, root %v, database %v; want nil, set, set", parent.Props, root.Props, database.Props)
	}
	// The state dataset is check-only for the orchestrator (design section 4), so it has no
	// orchestrator spec: bootstrap creates it like a 128K root, at 0700.
	stateProps := *root.Props
	stateProps.RecordSize = "128K"
	state := homelab.DatasetSpec{Name: "tank/orchestrator", Props: &stateProps}
	statePerm := homelab.PermissionSpec{Path: exampleState, UID: examplePUID, GID: examplePGID, Mode: 0o700}

	common := map[string]string{
		"PUID":               fmt.Sprint(examplePUID),
		"PGID":               fmt.Sprint(examplePGID),
		"GROUP_ROW_ID":       fmt.Sprint(exampleGroupRowID),
		"ORCH_STATE_STORAGE": exampleState,
		"TRUENAS_API_USER":   "truenas_admin",
		"ORCH_APP_NAME":      homelab.DefaultSelfApp,
		// *_JSON variables already hold JSON strings: the script builds them with jq -Rs.
		"NETWORKS_COMPOSE_JSON": jsonString(t, homelab.NetworksAppCompose),
		"ORCH_COMPOSE_JSON":     jsonString(t, "services: {}\n"),
	}
	with := func(extra map[string]string) map[string]string {
		m := maps.Clone(common)
		maps.Copy(m, extra)
		return m
	}
	tests := map[string]struct {
		vars map[string]string
		want []byte // nil: no Go encoder, only checked to parse
	}{
		"group":            {common, truenas.EncodeGroupCreate(rec.groups[0])},
		"user":             {common, truenas.EncodeUserCreate(rec.users[0], exampleGroupRowID)},
		"dataset-parent":   {with(map[string]string{"DATASET_NAME": parent.Name}), truenas.EncodeDatasetCreate(parent)},
		"dataset-root":     {with(map[string]string{"DATASET_NAME": root.Name}), truenas.EncodeDatasetCreate(root)},
		"dataset-database": {with(map[string]string{"DATASET_NAME": database.Name}), truenas.EncodeDatasetCreate(database)},
		"dataset-state":    {with(map[string]string{"DATASET_NAME": state.Name}), truenas.EncodeDatasetCreate(state)},
		"perm":             {with(map[string]string{"PERM_PATH": perm.Path}), truenas.EncodeSetPerm(perm)},
		"perm-state":       {common, truenas.EncodeSetPerm(statePerm)},
		"networks-app":     {common, truenas.EncodeAppCreate(rec.apps[0])},
		// api_key.create is a bootstrap-only call (the orchestrator never creates keys), and the
		// orchestrator app is never created by the orchestrator (D11): neither has a Go encoder.
		"api-key":          {common, nil},
		"orchestrator-app": {common, nil},
	}
	if rec.apps[0].Name != homelab.NetworksApp || rec.apps[0].Compose != homelab.NetworksAppCompose {
		t.Errorf("bootstrap created app %q, want %q with NetworksAppCompose", rec.apps[0].Name, homelab.NetworksApp)
	}
	if perm.Mode != 0o750 {
		t.Errorf("orchestrator setperm mode = %o, want 750", perm.Mode)
	}

	seen := map[string]bool{}
	for _, p := range readPayloads(t, script) {
		t.Run(p.name, func(t *testing.T) {
			tt, ok := tests[p.name]
			if !ok {
				t.Fatalf("%s:%d: payload:%s has no expectation in this test", scriptPath, p.line, p.name)
			}
			if seen[p.name] {
				t.Fatalf("%s:%d: payload:%s appears twice", scriptPath, p.line, p.name)
			}
			seen[p.name] = true
			text, err := substitute(p.body, tt.vars)
			if err != nil {
				t.Fatalf("%s:%d: payload:%s: %v", scriptPath, p.line, p.name, err)
			}
			var params []any
			if err := json.Unmarshal([]byte(text), &params); err != nil {
				t.Fatalf("%s:%d: payload:%s is not a JSON array: %v\n%s", scriptPath, p.line, p.name, err, text)
			}
			if len(params) != 1 {
				t.Fatalf("%s:%d: payload:%s has %d params, want 1", scriptPath, p.line, p.name, len(params))
			}
			if tt.want == nil {
				return
			}
			var want any
			if err := json.Unmarshal(tt.want, &want); err != nil {
				t.Fatalf("decode encoder output: %v", err)
			}
			if diff := cmp.Diff(want, params[0]); diff != "" {
				t.Errorf("%s:%d: payload:%s differs from the Go encoder (-go +script):\n%s",
					scriptPath, p.line, p.name, diff)
			}
		})
	}
	for name := range tests {
		if !seen[name] {
			t.Errorf("%s has no payload:%s", scriptPath, name)
		}
	}
}

// steps returns the step ids and titles matched by re (groups: id, title), in file order.
func steps(t *testing.T, file string, re *regexp.Regexp) (ids, titles []string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range re.FindAllSubmatch(data, -1) {
		ids = append(ids, string(m[1]))
		titles = append(titles, string(m[1])+" "+strings.TrimSpace(string(m[2])))
	}
	return ids, titles
}

func TestStepsMatchGuide(t *testing.T) {
	want := make([]string, 14)
	for i := range want {
		want[i] = fmt.Sprintf("B%d", i)
	}
	scriptIDs, scriptTitles := steps(t, scriptPath, regexp.MustCompile(`(?m)^# step:(B\d+)\b(.*)$`))
	guideIDs, guideTitles := steps(t, guidePath, regexp.MustCompile(`(?m)^## (B\d+)\b(.*)$`))
	if diff := cmp.Diff(want, scriptIDs); diff != "" {
		t.Errorf("%s step ids (-want +got):\n%s", scriptPath, diff)
	}
	if diff := cmp.Diff(want, guideIDs); diff != "" {
		t.Errorf("%s step ids (-want +got):\n%s", guidePath, diff)
	}
	if diff := cmp.Diff(scriptTitles, guideTitles); diff != "" {
		t.Errorf("step titles differ (-%s +%s):\n%s", scriptPath, guidePath, diff)
	}
}

func TestBootstrapParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	abs, err := filepath.Abs(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), bash, "-n", abs).CombinedOutput()
	if err != nil {
		t.Errorf("bash -n %s: %v\n%s", scriptPath, err, out)
	}
}
