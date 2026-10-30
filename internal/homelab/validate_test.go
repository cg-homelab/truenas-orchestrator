package homelab_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/cg-homelab/truenas-orchestrator/internal/compose"
	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

const homelabStorage = "/srv/homelab"

func validateFixture(t *testing.T, r *fakeRepo, opts homelab.Options) homelab.Report {
	t.Helper()
	if opts.HomelabStorage == "" {
		opts.HomelabStorage = homelabStorage
	}
	rep, err := homelab.ValidateRepo(t.Context(), r, &compose.Parser{}, opts)
	if err != nil {
		t.Fatalf("ValidateRepo() error = %v", err)
	}
	return rep
}

func stackByName(t *testing.T, rep homelab.Report, name string) homelab.StackReport {
	t.Helper()
	for _, s := range rep.Stacks {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stack %s not in report", name)
	return homelab.StackReport{}
}

// codes returns the distinct codes of fs, sorted.
func codes(fs []homelab.Finding) []homelab.Code {
	out := []homelab.Code{}
	for _, f := range fs {
		if !slices.Contains(out, f.Code) {
			out = append(out, f.Code)
		}
	}
	slices.Sort(out)
	return out
}

// TestValidateRepoFixture asserts, per stack, the exact set of codes and the status documented
// in fixtures/homelab-repo/FIXTURE.md, with host-path checks disabled.
func TestValidateRepoFixture(t *testing.T) {
	type C = homelab.Code
	want := map[string]struct {
		status homelab.Status
		codes  []C
	}{
		"ok-control":         {homelab.StatusReady, []C{}},
		"authelia":           {homelab.StatusNeedsSetup, []C{homelab.ENV004, homelab.PATH002, homelab.SETUP001}},
		"bad-yaml":           {homelab.StatusBroken, []C{homelab.COMPOSE002}},
		"broken-networks":    {homelab.StatusBroken, []C{homelab.NET001, homelab.NET002, homelab.NET003}},
		"broken-paths":       {homelab.StatusBroken, []C{homelab.PATH003}},
		"broken-setup":       {homelab.StatusBroken, []C{homelab.SETUP002, homelab.SETUP003, homelab.SETUP006}},
		"broken-toml":        {homelab.StatusBroken, []C{homelab.SETUP005}},
		"db-client":          {homelab.StatusNeedsSetup, []C{homelab.ENV004}},
		"db-postgres":        {homelab.StatusBroken, []C{homelab.COMPOSE001, homelab.ENV009}},
		"db-prometheus":      {homelab.StatusBroken, []C{homelab.ENV001, homelab.ENV007, homelab.ENV008}},
		"db-redis":           {homelab.StatusNeedsSetup, []C{homelab.ENV004}},
		"db-timescale":       {homelab.StatusBroken, []C{homelab.COMPOSE001, homelab.ENV004, homelab.PATH002}},
		"env-container-only": {homelab.StatusBroken, []C{homelab.ENV002, homelab.ENV003}},
		"env-drift":          {homelab.StatusBroken, []C{homelab.ENV001, homelab.ENV005, homelab.ENV006, homelab.ENV008, homelab.ENV010}},
		"legacy-name":        {homelab.StatusReady, []C{homelab.COMPOSE003, homelab.COMPOSE004}},
		"no-compose":         {homelab.StatusBroken, []C{homelab.COMPOSE005}},
		"Bad_Name":           {homelab.StatusBroken, []C{homelab.COMPOSE006}},
	}
	for name, w := range want {
		slices.Sort(w.codes)
		want[name] = w
	}

	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{})

	var names []string
	for _, s := range rep.Stacks {
		names = append(names, s.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("stacks not sorted by name: %v", names)
	}
	if len(rep.Stacks) != len(want) {
		t.Errorf("got %d stacks %v, want %d", len(rep.Stacks), names, len(want))
	}
	for _, s := range rep.Stacks {
		t.Run(s.Name, func(t *testing.T) {
			w, ok := want[s.Name]
			if !ok {
				t.Fatalf("unexpected stack %s", s.Name)
			}
			if diff := cmp.Diff(w.codes, codes(s.Findings)); diff != "" {
				t.Errorf("codes mismatch (-want +got):\n%s\nfindings: %+v", diff, s.Findings)
			}
			if s.Status != w.status {
				t.Errorf("status = %s, want %s", s.Status, w.status)
			}
			// The status invariant holds with no exceptions: Broken if and only if an error.
			hasError := slices.ContainsFunc(s.Findings, func(f homelab.Finding) bool { return f.Severity == homelab.SeverityError })
			if hasError != (s.Status == homelab.StatusBroken) {
				t.Errorf("status %s with error findings = %v", s.Status, hasError)
			}
		})
	}
	if len(rep.Findings) != 0 {
		t.Errorf("repo findings = %+v, want none", rep.Findings)
	}
}

func TestValidateRepoEveryImplementedCodeCovered(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{
		HostPathExists: func(string) (bool, error) { return false, nil },
	})
	seen := map[homelab.Code]bool{}
	for _, s := range rep.Stacks {
		for _, f := range s.Findings {
			seen[f.Code] = true
		}
	}
	all := []homelab.Code{
		homelab.COMPOSE001, homelab.COMPOSE002, homelab.COMPOSE003, homelab.COMPOSE004, homelab.COMPOSE005, homelab.COMPOSE006,
		homelab.ENV001, homelab.ENV002, homelab.ENV003, homelab.ENV004, homelab.ENV005,
		homelab.ENV006, homelab.ENV007, homelab.ENV008, homelab.ENV009, homelab.ENV010,
		homelab.PATH001, homelab.PATH002, homelab.PATH003,
		homelab.NET001, homelab.NET002, homelab.NET003,
		homelab.SETUP001, homelab.SETUP002, homelab.SETUP003, homelab.SETUP005, homelab.SETUP006,
	}
	for _, c := range all {
		if !seen[c] {
			t.Errorf("no fixture stack produces %s", c)
		}
	}
}

func TestValidateRepoHostPathChecks(t *testing.T) {
	missing := func(string) (bool, error) { return false, nil }
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{HostPathExists: missing})

	got := map[string][]homelab.Finding{}
	for _, name := range []string{"broken-paths", "ok-control"} {
		for _, f := range stackByName(t, rep, name).Findings {
			if f.Code == homelab.PATH001 {
				got[name] = append(got[name], homelab.Finding{Code: f.Code, Severity: f.Severity, File: f.File, Line: f.Line})
			}
		}
	}
	want := map[string][]homelab.Finding{
		// The ../ escape is PATH003 only: PATH001 is not checked for a path outside the roots.
		"broken-paths": {{Code: homelab.PATH001, Severity: homelab.SeverityWarning, File: "compose/broken-paths/compose.yaml", Line: 17}},
		// setup.toml [[dirs]] declares the path, so it is info and the stack needs setup.
		"ok-control": {{Code: homelab.PATH001, Severity: homelab.SeverityInfo, File: "compose/ok-control/compose.yaml", Line: 17}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("PATH001 findings mismatch (-want +got):\n%s", diff)
	}
	if s := stackByName(t, rep, "ok-control"); s.Status != homelab.StatusNeedsSetup {
		t.Errorf("ok-control status = %s, want NeedsSetup when its declared path is missing", s.Status)
	}

	present := func(string) (bool, error) { return true, nil }
	rep = validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{HostPathExists: present})
	if s := stackByName(t, rep, "ok-control"); s.Status != homelab.StatusReady || len(s.Findings) != 0 {
		t.Errorf("ok-control with every path present = %s %+v, want Ready with no findings", s.Status, s.Findings)
	}
}

func TestHomelabStorageIsInjected(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{HomelabStorage: "/opt/homelab"})
	for _, s := range rep.Stacks {
		for _, f := range s.Findings {
			if strings.Contains(f.Message, "HOMELAB_STORAGE is referenced") {
				t.Errorf("%s: HOMELAB_STORAGE reported unresolved: %+v", s.Name, f)
			}
		}
		if s.Name == "bad-yaml" || s.Name == "Bad_Name" {
			continue
		}
		v, ok := s.Interpolation.Lookup("HOMELAB_STORAGE")
		if !ok {
			t.Errorf("%s: HOMELAB_STORAGE missing from the interpolation scope", s.Name)
			continue
		}
		// .global.env defines HOMELAB_STORAGE=/mnt/to/apps/homelab; the injected value wins.
		if v.Source != homelab.SourceInjected || v.Value.Reveal() != "/opt/homelab" || v.Line != 0 {
			t.Errorf("%s: HOMELAB_STORAGE = {%q %s %d}, want the injected value", s.Name, v.Value.Reveal(), v.Source, v.Line)
		}
	}
}

func TestDeployEnvProvenanceFromGlobalEnv(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{})
	s := stackByName(t, rep, "db-postgres")
	tests := []struct {
		key, value, source string
		line               int
	}{
		{"DATABASE_STORAGE", "/mnt/to/databases", "compose/.global.env", 14},
		{"SERVICE_NAME", "db-postgres", "compose/db-postgres/.env", 1},
		{"HOMELAB_STORAGE", homelabStorage, homelab.SourceInjected, 0},
	}
	for _, tt := range tests {
		v, ok := s.Interpolation.Lookup(tt.key)
		if !ok {
			t.Errorf("Interpolation.Lookup(%s) not found", tt.key)
			continue
		}
		got := []any{v.Value.Reveal(), v.Source, v.Line}
		if diff := cmp.Diff([]any{tt.value, tt.source, tt.line}, got); diff != "" {
			t.Errorf("%s provenance mismatch (-want +got):\n%s", tt.key, diff)
		}
	}
	// Container scope: .global.env, then .env, then environment: wins.
	c, ok := s.Container.Lookup("PGDATA")
	if !ok || c.Source != homelab.SourceEnvironment || c.Line != 27 {
		t.Errorf("Container PGDATA = %+v, want from compose.yaml:environment line 27", c)
	}
	c, ok = s.Container.Lookup("POSTGRES_PASSWORD")
	if !ok || c.Source != "compose/db-postgres/.env" || c.Line != 2 {
		t.Errorf("Container POSTGRES_PASSWORD = %+v, want from compose/db-postgres/.env line 2", c)
	}
}

func TestEnvContainerOnly(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{})
	s := stackByName(t, rep, "env-container-only")
	type loc struct {
		Code homelab.Code
		Line int
		Var  string
	}
	var got []loc
	for _, f := range s.Findings {
		name, _, _ := strings.Cut(f.Message, " ")
		got = append(got, loc{f.Code, f.Line, name})
	}
	want := []loc{
		{homelab.ENV002, 7, "MISSING"},
		{homelab.ENV003, 23, "DATA_DIR"},
		{homelab.ENV003, 25, "EXTRA_DIR"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
	extra, ok := s.Container.Lookup("EXTRA_DIR")
	if !ok || extra.Source != "compose/env-container-only/extra.env" || extra.Line != 2 {
		t.Errorf("Container EXTRA_DIR = %+v, want from compose/env-container-only/extra.env line 2", extra)
	}
	if _, ok := s.Interpolation.Lookup("DATA_DIR"); ok {
		t.Errorf("DATA_DIR is in the interpolation scope; it must be container-only")
	}
}

func TestGitFindings(t *testing.T) {
	r := loadFixture(t, fixtureRoot)
	r.tracked["compose/env-drift/.env"] = true
	r.tracked["compose/.global.env"] = true
	r.status = homelab.RepoStatus{Branch: "main", Upstream: "origin/main", Behind: 2, Dirty: true}
	rep := validateFixture(t, r, homelab.Options{})

	var got []homelab.Finding
	for _, f := range rep.Findings {
		got = append(got, homelab.Finding{Code: f.Code, Severity: f.Severity, File: f.File})
	}
	want := []homelab.Finding{
		{Code: homelab.GIT002, Severity: homelab.SeverityWarning},
		{Code: homelab.GIT003, Severity: homelab.SeverityInfo},
		{Code: homelab.GIT001, Severity: homelab.SeverityError, File: "compose/.global.env"},
		{Code: homelab.GIT001, Severity: homelab.SeverityError, File: "compose/env-drift/.env"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("repo findings mismatch (-want +got):\n%s", diff)
	}
	if rep.Repo != r.status {
		t.Errorf("Report.Repo = %+v, want %+v", rep.Repo, r.status)
	}
}

// TestNoSecretValuesInOutput checks that no env-file value reaches a finding message, and that
// JSON encoding of the report redacts every value.
func TestNoSecretValuesInOutput(t *testing.T) {
	r := loadFixture(t, fixtureRoot)
	values := []string{"REPLACE_THIS", "something-else", "surprise", "/mnt/to/apps/appconfigs", "/mnt/to/databases", "Europe/Oslo"}
	rep := validateFixture(t, r, homelab.Options{HostPathExists: func(string) (bool, error) { return false, nil }})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("json.Marshal(report): %v", err)
	}
	for _, v := range values {
		if strings.Contains(string(b), v) {
			t.Errorf("JSON report contains env value %q", v)
		}
	}
	if !strings.Contains(string(b), `"interpolation":{"vars":[`) {
		t.Errorf("JSON report has no snake_case interpolation scope: %.200s", b)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("JSON report contains null; empty lists must encode as []")
	}
}

func TestInterpolationScopeDefaults(t *testing.T) {
	r := &fakeRepo{root: "/srv/homelab", files: map[string][]byte{
		"compose/app/.env":        []byte("SERVICE_NAME=app\nSET=yes\n"),
		"compose/app/example.env": []byte("SERVICE_NAME=app\nSET=yes\n"),
		"compose/app/compose.yaml": []byte(`services:
  app:
    container_name: ${SERVICE_NAME}
    image: alpine:${TAG:-3.22}
    labels:
      - a=${SET:-unused}
      - b=${UNSET_OPTIONAL:+x}
      - c=${OUTER:-${SET}}
    deploy:
      resources:
        limits:
          memory: 64M
`),
	}, tracked: map[string]bool{}}
	rep := validateFixture(t, r, homelab.Options{})
	s := stackByName(t, rep, "app")
	if len(s.Findings) != 0 {
		t.Errorf("findings = %+v, want none: defaults and optional refs are never missing", s.Findings)
	}
	tag, ok := s.Interpolation.Lookup("TAG")
	if !ok || tag.Source != homelab.SourceDefault || tag.Value.Reveal() != "3.22" || tag.Line != 4 {
		t.Errorf("TAG = {%q %s %d}, want the default 3.22 at line 4", tag.Value.Reveal(), tag.Source, tag.Line)
	}
	set, _ := s.Interpolation.Lookup("SET")
	if set.Source != "compose/app/.env" {
		t.Errorf("SET source = %s, want compose/app/.env (a default never overrides a value)", set.Source)
	}
	outer, _ := s.Interpolation.Lookup("OUTER")
	if outer.Value.Reveal() != "yes" {
		t.Errorf("OUTER = %q, want the nested default resolved to yes", outer.Value.Reveal())
	}
}

func TestEnvResolutionPrecedence(t *testing.T) {
	r := &fakeRepo{root: "/srv/homelab", files: map[string][]byte{
		"compose/.global.env": []byte("A=global\nB=global\nHOMELAB_STORAGE=/from/file\n"),
		"compose/app/.env":    []byte("B=app\nC=app\n"),
		"compose/app/one.env": []byte("D=one\nE=one\n"),
		"compose/app/two.env": []byte("E=two\nF=two\n"),
		"compose/app/compose.yaml": []byte(`services:
  app:
    image: alpine
    command: echo $$A $$D $$E $$F $$G
    env_file:
      - ./one.env
      - two.env
    environment:
      F: env
      G: ${C}
    labels:
      - b=${B}
    networks: [home]
    deploy: {resources: {limits: {memory: 64M}}}
networks:
  home:
    external: true
`),
	}, tracked: map[string]bool{}}
	rep := validateFixture(t, r, homelab.Options{})
	s := stackByName(t, rep, "app")

	scope := func(es homelab.EnvScope) map[string]string {
		m := map[string]string{}
		for _, v := range es.Vars {
			m[v.Key] = v.Value.Reveal() + " @" + v.Source
		}
		return m
	}
	wantInterp := map[string]string{
		"A":               "global @compose/.global.env",
		"B":               "app @compose/app/.env",
		"C":               "app @compose/app/.env",
		"HOMELAB_STORAGE": homelabStorage + " @injected",
	}
	if diff := cmp.Diff(wantInterp, scope(s.Interpolation)); diff != "" {
		t.Errorf("interpolation scope mismatch (-want +got):\n%s", diff)
	}
	wantContainer := map[string]string{
		"D": "one @compose/app/one.env",
		"E": "two @compose/app/two.env",
		"F": "env @compose.yaml:environment",
		"G": "app @compose.yaml:environment",
	}
	if diff := cmp.Diff(wantContainer, scope(s.Container)); diff != "" {
		t.Errorf("container scope mismatch (-want +got):\n%s", diff)
	}
	// $$A is not in container scope: the app does not list .global.env under env_file:.
	if diff := cmp.Diff([]homelab.Code{homelab.ENV002}, codes(s.Findings)); diff != "" {
		t.Errorf("codes mismatch (-want +got):\n%s\n%+v", diff, s.Findings)
	}
	if n := len(s.Findings); n != 1 || !strings.HasPrefix(s.Findings[0].Message, "A ") {
		t.Errorf("findings = %+v, want exactly ENV002 for A", s.Findings)
	}
}

func TestDeployEnv(t *testing.T) {
	global, _ := homelab.ParseDotEnv([]byte("HOMELAB_STORAGE=/file\nX=g\nY=g\n"))
	app, _ := homelab.ParseDotEnv([]byte("Y=a\nHOMELAB_STORAGE=/app-file\n"))
	got := homelab.DeployEnv("/injected", global, app, "g.env", "a.env")
	want := []string{"HOMELAB_STORAGE=/injected@injected:0", "X=g@g.env:2", "Y=a@a.env:1"}
	var gotS []string
	for _, v := range got.Vars {
		gotS = append(gotS, v.Key+"="+v.Value.Reveal()+"@"+v.Source+":"+strconv.Itoa(v.Line))
	}
	if diff := cmp.Diff(want, gotS); diff != "" {
		t.Errorf("DeployEnv() mismatch (-want +got):\n%s", diff)
	}

	noInject := homelab.DeployEnv("", global, homelab.DotEnv{}, "g.env", "a.env")
	if v, _ := noInject.Lookup("HOMELAB_STORAGE"); v.Source != "g.env" {
		t.Errorf("DeployEnv(\"\") HOMELAB_STORAGE source = %s, want g.env when nothing is injected", v.Source)
	}
}

func TestENV004Severity(t *testing.T) {
	r := loadFixture(t, fixtureRoot)
	r.files["compose/ok-control/compose.yaml"] = []byte(`services:
  ok-control:
    container_name: "${SERVICE_NAME}"
    image: nginx
    networks: [proxy]
    env_file:
      - ${HOMELAB_STORAGE}/compose/.global.env
      - ${HOMELAB_STORAGE}/compose/ok-control/.env
      - ${HOMELAB_STORAGE}/compose/ok-control/secrets.env
    deploy: {resources: {limits: {memory: 64M}}}
networks:
  proxy:
    external: true
`)
	rep := validateFixture(t, r, homelab.Options{})

	own := stackByName(t, rep, "db-client")
	if len(own.Findings) != 1 || own.Findings[0].Code != homelab.ENV004 || own.Findings[0].Severity != homelab.SeverityWarning {
		t.Errorf("db-client findings = %+v, want one ENV004 warning for its own .env", own.Findings)
	}
	if own.Status != homelab.StatusNeedsSetup {
		t.Errorf("db-client status = %s, want NeedsSetup", own.Status)
	}

	other := stackByName(t, rep, "ok-control")
	var got []homelab.Finding
	for _, f := range other.Findings {
		got = append(got, homelab.Finding{Code: f.Code, Severity: f.Severity, Line: f.Line})
	}
	want := []homelab.Finding{{Code: homelab.ENV004, Severity: homelab.SeverityError, Line: 9}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ok-control findings mismatch (-want +got):\n%s", diff)
	}
	if other.Status != homelab.StatusBroken {
		t.Errorf("ok-control status = %s, want Broken for a missing env_file that is not its own .env", other.Status)
	}
}

func TestENV008OnlyWithUnresolvedReferences(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{})
	var msgs []string
	for _, s := range rep.Stacks {
		for _, f := range s.Findings {
			if f.Code == homelab.ENV008 {
				msgs = append(msgs, s.Name+": "+f.Message)
			}
		}
	}
	want := []string{
		"db-prometheus: PORT is defined but never referenced in compose.yaml; unresolved references in this stack: APPDATA_STORAGE",
		"env-drift: UNDOCUMENTED_KEY is defined but never referenced in compose.yaml; unresolved references in this stack: GREETING",
	}
	if diff := cmp.Diff(want, msgs); diff != "" {
		t.Errorf("ENV008 messages mismatch (-want +got):\n%s", diff)
	}
}

func TestSetupCodesPointAtLines(t *testing.T) {
	rep := validateFixture(t, loadFixture(t, fixtureRoot), homelab.Options{})
	type loc struct {
		Stack string
		Code  homelab.Code
		File  string
		Line  int
	}
	var got []loc
	for _, name := range []string{"broken-setup", "broken-toml", "no-compose"} {
		for _, f := range stackByName(t, rep, name).Findings {
			got = append(got, loc{name, f.Code, f.File, f.Line})
		}
	}
	want := []loc{
		{"broken-setup", homelab.SETUP006, "compose/broken-setup/setup.toml", 3},
		{"broken-setup", homelab.SETUP003, "compose/broken-setup/setup.toml", 5},
		{"broken-setup", homelab.SETUP002, "compose/broken-setup/setup.toml", 11},
		{"broken-toml", homelab.SETUP005, "compose/broken-toml/setup.toml", 5},
		{"no-compose", homelab.COMPOSE005, "compose/no-compose", 0},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
}

// appRepo builds a fake repo with one app, "app", and the fixture's .global.env.
func appRepo(t *testing.T, files map[string]string) *fakeRepo {
	t.Helper()
	r := &fakeRepo{root: "/srv/homelab", files: map[string][]byte{}, tracked: map[string]bool{}, unreadable: map[string]bool{}}
	global, err := os.ReadFile(fixtureRoot + "/compose/.global.env")
	if err != nil {
		t.Fatal(err)
	}
	r.files["compose/.global.env"] = global
	for k, v := range files {
		r.files["compose/app/"+k] = []byte(v)
	}
	return r
}

func findingsOf(t *testing.T, rep homelab.Report) []string {
	t.Helper()
	var out []string
	for _, f := range stackByName(t, rep, "app").Findings {
		out = append(out, fmt.Sprintf("%s %s %d", f.Code, f.Severity, f.Line))
	}
	return out
}

const appHeader = `services:
  app:
    image: x
    networks: [home]
    deploy: {resources: {limits: {memory: 64M}}}
`

const appNetworks = `networks:
  home:
    external: true
`

func TestContainerReadsCoveredByMissingEnv(t *testing.T) {
	r := appRepo(t, map[string]string{
		"example.env": "SERVICE_NAME=app\nAPI_KEY=REPLACE_THIS\n",
		"compose.yaml": appHeader + `    command: ["sh", "-c", "use $$API_KEY; for i in 1 2; do echo $$i; done"]
    environment:
      HASH: $$argon2id$$v=19$$m=65536
    env_file:
      - ${HOMELAB_STORAGE}/compose/app/.env
` + appNetworks,
	})
	rep := validateFixture(t, r, homelab.Options{})
	// Only the missing .env: API_KEY comes from example.env once setup creates it, $$i is the
	// loop's own variable and the argon2 hash is never shell-evaluated.
	if diff := cmp.Diff([]string{"ENV004 warning 10"}, findingsOf(t, rep)); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
}

func TestSecretInEnvFilePathNeverPrinted(t *testing.T) {
	const token = "tok-9f8e7d6c5b4a"
	for _, present := range []bool{true, false} {
		files := map[string]string{
			".env":        "SERVICE_NAME=app\nTOKEN=" + token + "\n",
			"example.env": "SERVICE_NAME=app\nTOKEN=x\n",
			"compose.yaml": appHeader + `    container_name: ${SERVICE_NAME}
    labels:
      - t=${TOKEN}
    env_file:
      - ./conf-${TOKEN}.env
      - ./missing-${TOKEN}.env
` + appNetworks,
		}
		if present {
			files["conf-"+token+".env"] = "K=v\n"
		}
		rep := validateFixture(t, appRepo(t, files), homelab.Options{})
		b, err := json.Marshal(rep)
		if err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, s := range rep.Stacks {
			for _, f := range s.Findings {
				fmt.Fprintf(&text, "%+v\n", f)
			}
			for _, v := range append(s.Interpolation.Vars, s.Container.Vars...) {
				fmt.Fprintf(&text, "%s %s %d\n", v.Key, v.Source, v.Line)
			}
		}
		if strings.Contains(string(b), token) || strings.Contains(text.String(), token) {
			t.Errorf("present=%v: the token reached the report:\n%s", present, text.String())
		}
		if present {
			if k, _ := stackByName(t, rep, "app").Container.Lookup("K"); k.Source != "./conf-${TOKEN}.env" {
				t.Errorf("K source = %q, want the raw env_file path", k.Source)
			}
		}
	}
}

func TestHostPathErrorNamesRawPath(t *testing.T) {
	r := loadFixture(t, fixtureRoot)
	boom := errors.New("stat /mnt/to/apps/appconfigs/ok-control/html: permission denied")
	_, err := homelab.ValidateRepo(t.Context(), r, &compose.Parser{}, homelab.Options{
		HomelabStorage: homelabStorage,
		HostPathExists: func(string) (bool, error) { return false, boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ValidateRepo() error = %v, want it to wrap the callback error", err)
	}
	if strings.Contains(err.Error(), "/mnt/to/") {
		t.Errorf("error %q contains a resolved path", err)
	}
	if !strings.Contains(err.Error(), `"/mnt/fast/appdata/authelia/config" at compose/authelia/compose.yaml line 34`) {
		t.Errorf("error %q does not name the raw compose path and line", err)
	}
}

func TestUnreadableEnvFile(t *testing.T) {
	r := appRepo(t, map[string]string{
		".env":        "SERVICE_NAME=app\n",
		"example.env": "SERVICE_NAME=app\n",
		"compose.yaml": appHeader + `    command: echo $$FROM_LINK
    env_file:
      - ${HOMELAB_STORAGE}/compose/app/.env
      - ./linked.env
` + appNetworks,
	})
	r.unreadable["compose/app/linked.env"] = true
	rep := validateFixture(t, r, homelab.Options{})
	s := stackByName(t, rep, "app")
	// One ENV004 error; no ENV002 for $$FROM_LINK, whose scope is unknown.
	if diff := cmp.Diff([]string{"ENV004 error 9"}, findingsOf(t, rep)); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(s.Findings[0].Message, "./linked.env cannot be read") || s.Status != homelab.StatusBroken {
		t.Errorf("finding = %+v, status %s", s.Findings[0], s.Status)
	}
}

func TestUnparseableEnvFile(t *testing.T) {
	r := appRepo(t, map[string]string{
		".env":         "SERVICE_NAME=app\nPASSWORD=\"never-closed\n",
		"example.env":  "SERVICE_NAME=app\nPASSWORD=x\n",
		"compose.yaml": appHeader + "    container_name: ${SERVICE_NAME}\n" + appNetworks,
	})
	rep := validateFixture(t, r, homelab.Options{})
	s := stackByName(t, rep, "app")
	var got []homelab.Finding
	for _, f := range s.Findings {
		got = append(got, homelab.Finding{Code: f.Code, Severity: f.Severity, File: f.File, Line: f.Line})
	}
	// The keys after the unterminated value are ignored, so PASSWORD is also reported missing.
	want := []homelab.Finding{
		{Code: homelab.ENV004, Severity: homelab.SeverityError, File: "compose/app/.env", Line: 2},
		{Code: homelab.ENV005, Severity: homelab.SeverityWarning, File: "compose/app/example.env", Line: 2},
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(homelab.Finding{}, "Message", "Fix")); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s\n%+v", diff, s.Findings)
	}
}

func TestEmptyValueSemantics(t *testing.T) {
	r := appRepo(t, map[string]string{
		".env":        "SERVICE_NAME=app\nEMPTY=\n",
		"example.env": "SERVICE_NAME=app\nEMPTY=\n",
		"compose.yaml": appHeader + `    container_name: ${SERVICE_NAME}
    labels:
      - a=${EMPTY:-${UNSET_INNER}}
      - b=${EMPTY-${NEVER_EVALUATED}}
      - c=${EMPTY:?must be set}
      - d=${EMPTY?only unset fails}
` + appNetworks,
	})
	rep := validateFixture(t, r, homelab.Options{})
	var got []string
	for _, f := range stackByName(t, rep, "app").Findings {
		name, _, _ := strings.Cut(f.Message, " ")
		got = append(got, fmt.Sprintf("%s %d %s", f.Code, f.Line, name))
	}
	want := []string{"ENV001 8 UNSET_INNER", "ENV001 10 EMPTY"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
}

func TestLiteralPathEscape(t *testing.T) {
	r := appRepo(t, map[string]string{
		".env":        "SERVICE_NAME=app\n",
		"example.env": "SERVICE_NAME=app\n",
		"compose.yaml": appHeader + `    volumes:
      - /mnt/fast/appdata/../../../etc:/x
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/to/apps/appconfigs/../../../etc:/y
` + appNetworks,
	})
	rep := validateFixture(t, r, homelab.Options{})
	want := []string{
		"PATH002 warning 7", "PATH003 error 7",
		"PATH002 warning 9", "PATH003 error 9",
	}
	if diff := cmp.Diff(want, findingsOf(t, rep)); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
}

func TestNetworkNameField(t *testing.T) {
	r := appRepo(t, map[string]string{
		".env":        "SERVICE_NAME=app\n",
		"example.env": "SERVICE_NAME=app\n",
		"compose.yaml": `services:
  app:
    image: x
    networks: [proxy_net, other]
    deploy: {resources: {limits: {memory: 64M}}}
networks:
  proxy_net:
    name: proxy
    external: true
  other:
    name: backend
    external: true
`,
	})
	rep := validateFixture(t, r, homelab.Options{})
	// proxy_net is the contract network proxy; other is named backend, which is not.
	if diff := cmp.Diff([]string{"NET001 error 4"}, findingsOf(t, rep)); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
	if msg := stackByName(t, rep, "app").Findings[0].Message; !strings.Contains(msg, "backend") {
		t.Errorf("NET001 message %q does not name backend", msg)
	}
}
