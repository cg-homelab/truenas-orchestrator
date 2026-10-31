package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// startBootstrap posts run-bootstrap with digest and returns the response.
func (e *truenasEnv) startBootstrap(t *testing.T, digest string) response {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/environment/bootstrap", map[string]string{"plan_digest": digest}, e.cookie)
}

// runBootstrap plans, starts a bootstrap job with that plan's digest and waits for it.
func (e *truenasEnv) runBootstrap(t *testing.T) JobView {
	t.Helper()
	v := e.environment(t)
	r := e.startBootstrap(t, v.Digest)
	if r.status != http.StatusAccepted {
		t.Fatalf("POST bootstrap status = %d, want 202; body %s", r.status, r.body)
	}
	var started JobStarted
	decode(t, r, &started)
	return e.waitJob(t, started.JobID, e.cookie)
}

func stepLabels(steps []BootstrapStepView) []string {
	out := []string{}
	for _, s := range steps {
		out = append(out, s.Kind+" "+s.Target)
	}
	return out
}

func TestBootstrapEndToEnd(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	e.saveKey(t)
	b := e.base
	var bodies []string

	v := e.environment(t)
	wantSteps := []string{
		"group_create homelab", "user_create homelab", "dataset_create tank/apps", "dataset_create tank/apps/homelab",
		"dataset_create tank/apps/appconfigs", "dataset_create tank/databases",
		"permissions_set " + b + "/apps/homelab", "permissions_set " + b + "/apps/appconfigs", "permissions_set " + b + "/databases",
		"app_create homelab-networks",
	}
	if diff := cmp.Diff(wantSteps, stepLabels(v.Steps)); diff != "" {
		t.Fatalf("plan steps mismatch (-want +got):\n%s", diff)
	}
	if !v.Runnable || v.Ready || v.Digest == "" || v.Connection.State != connOK || len(v.Problems) != 0 {
		t.Fatalf("view runnable %v ready %v digest %q connection %+v problems %q", v.Runnable, v.Ready, v.Digest, v.Connection, v.Problems)
	}
	perm := v.Steps[6]
	if diff := cmp.Diff(BootstrapStepView{
		Kind: "permissions_set", Target: b + "/apps/homelab", Action: "create",
		Detail: map[string]string{"from_owner": "", "from_mode": "", "from_acl": "", "to_owner": "1000:1000", "to_mode": "0750", "stripacl": "true"},
	}, perm); diff != "" {
		t.Errorf("permissions step mismatch (-want +got):\n%s", diff)
	}
	if last := v.Items[len(v.Items)-1]; last.Kind != "deploy_key" || last.State != "missing" {
		t.Errorf("last item %+v, want the deploy key missing", last)
	}
	if len(e.tn.mutations()) != 0 {
		t.Fatalf("get-environment mutated: %v", e.tn.mutations())
	}

	// A stale digest is refused before any job starts.
	r := e.startBootstrap(t, strings.Repeat("0", 64))
	bodies = append(bodies, r.body)
	if r.status != http.StatusConflict || !strings.Contains(r.body, msgPlanChanged) {
		t.Errorf("POST with a stale digest = %d %s, want 409", r.status, r.body)
	}
	r = e.startBootstrap(t, "")
	if r.status != http.StatusUnprocessableEntity {
		t.Errorf("POST without a digest = %d %s, want 422", r.status, r.body)
	}

	j := e.runBootstrap(t)
	if j.State != "succeeded" || j.Kind != jobKindBootstrap {
		t.Fatalf("bootstrap job = %+v, want a succeeded %s job", j, jobKindBootstrap)
	}
	wantLog := []string{
		"create group homelab gid 1000",
		"create user homelab uid 1000 gid 1000",
		"create dataset tank/apps at " + b + "/apps",
		"create dataset tank/apps/homelab at " + b + "/apps/homelab recordsize 128K",
		"create dataset tank/apps/appconfigs at " + b + "/apps/appconfigs recordsize 128K",
		"create dataset tank/databases at " + b + "/databases recordsize 16K",
		"create permissions " + b + "/apps/homelab 1000:1000 0750",
		"create permissions " + b + "/apps/appconfigs 1000:1000 0750",
		"create permissions " + b + "/databases 1000:1000 0750",
		"create app homelab-networks (networks home,proxy,isolated,databases)",
		"10 created, 0 changed, 0 unchanged",
	}
	if diff := cmp.Diff(wantLog, j.Log); diff != "" {
		t.Errorf("job log mismatch (-want +got):\n%s", diff)
	}
	if n := len(e.tn.mutations()); n != 10 {
		t.Errorf("mutations %v, want 10", e.tn.mutations())
	}

	// Criterion 4: nothing left to do, and running again changes nothing.
	v = e.environment(t)
	if len(v.Steps) != 0 || !v.Runnable || v.Ready {
		t.Errorf("after bootstrap: steps %v runnable %v ready %v problems %q, want no steps, runnable, not ready (no deploy key)",
			stepLabels(v.Steps), v.Runnable, v.Ready, v.Problems)
	}
	j = e.runBootstrap(t)
	if j.State != "succeeded" || !slices.Equal(j.Log, []string{"nothing to do"}) {
		t.Errorf("second bootstrap job %s log %q, want succeeded with nothing to do", j.State, j.Log)
	}
	if n := len(e.tn.mutations()); n != 10 {
		t.Errorf("the second run mutated: %v", e.tn.mutations()[10:])
	}

	// The deploy key completes readiness.
	if r := e.do(t, http.MethodPost, "/api/environment/deploy-key", nil, e.cookie); r.status != http.StatusCreated {
		t.Fatalf("POST deploy-key status = %d; body %s", r.status, r.body)
	}
	if v = e.environment(t); !v.Ready {
		t.Errorf("after the deploy key: ready %v, items %+v", v.Ready, v.Items)
	}

	entries, auditBody := e.auditEntries(t, e.cookie)
	slices.Reverse(entries) // oldest first
	wantActions := []string{
		auditTrueNASSettingsWrite, auditGroupCreate, auditUserCreate, auditDatasetCreate, auditDatasetCreate,
		auditDatasetCreate, auditDatasetCreate, auditACLSet, auditACLSet, auditACLSet, auditAppCreate,
		auditNetworkCreate, auditNetworkCreate, auditNetworkCreate, auditNetworkCreate, auditBootstrapRun,
		auditBootstrapRun, auditDeployKeyCreate,
	}
	if diff := cmp.Diff(wantActions, actions(entries)); diff != "" {
		t.Fatalf("audit actions mismatch (-want +got):\n%s", diff)
	}
	jobID := entries[1].Details["job_id"]
	if jobID == "" {
		t.Fatalf("group.create entry has no job_id: %+v", entries[1])
	}
	for _, tt := range []struct {
		i      int
		target string
		want   map[string]string
	}{
		{1, "homelab", map[string]string{"job_id": jobID, "outcome": "created", "gid": "1000"}},
		{2, "homelab", map[string]string{"job_id": jobID, "outcome": "created", "uid": "1000", "gid": "1000"}},
		{6, "tank/databases", map[string]string{
			"job_id": jobID, "outcome": "created", "mountpoint": b + "/databases", "role": "database", "acltype": "POSIX",
			"aclmode": "DISCARD", "atime": "OFF", "compression": "LZ4", "recordsize": "16K",
		}},
		{7, b + "/apps/homelab", map[string]string{
			"job_id": jobID, "outcome": "created", "uid": "1000", "gid": "1000", "mode": "0750", "from_owner": "", "from_mode": "", "stripacl": "true",
		}},
		{10, "homelab-networks", map[string]string{"job_id": jobID, "outcome": "created"}},
		{11, "home", map[string]string{"job_id": jobID, "app": "homelab-networks"}},
		{15, "environment", map[string]string{
			"job_id": jobID, "state": "succeeded", "steps": "10", "created": "10", "changed": "0", "unchanged": "0",
		}},
	} {
		en := entries[tt.i]
		if diff := cmp.Diff(tt.want, en.Details); diff != "" || en.Target != tt.target || en.User != testUser {
			t.Errorf("audit entry %d %s %s by %s; details mismatch (-want +got):\n%s", tt.i, en.Action, en.Target, en.User, diff)
		}
	}
	if entries[16].Details["steps"] != "0" {
		t.Errorf("second bootstrap.run = %+v, want 0 steps", entries[16])
	}
	if dials, closes := e.tn.sessions(); dials != closes {
		t.Errorf("dials %d closes %d: every session must be closed", dials, closes)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"bootstrap finished","job_id":"`) {
		t.Errorf("logs lack the bootstrap finished line")
	}
	assertNoKey(t, "the audit log", auditBody)
	assertNoKey(t, "the server log", logs)
	assertNoKey(t, "a job log", strings.Join(j.Log, "\n"))
	assertNoKey(t, "a response", strings.Join(bodies, "\n"))
}

func TestBootstrapRefusals(t *testing.T) {
	t.Run("not runnable is a 422 listing problems", func(t *testing.T) {
		e := newTrueNASEnv(t, truenasOptions{})
		e.saveKey(t)
		e.tn.docker.Status = "UNCONFIGURED"
		v := e.environment(t)
		if v.Runnable || len(v.Problems) == 0 {
			t.Fatalf("view runnable %v problems %q, want problems", v.Runnable, v.Problems)
		}
		r := e.startBootstrap(t, v.Digest)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "Docker is UNCONFIGURED") {
			t.Errorf("POST = %d %s, want 422 naming docker", r.status, r.body)
		}
		if len(e.tn.mutations()) != 0 {
			t.Errorf("mutated: %v", e.tn.mutations())
		}
	})
	t.Run("not connected", func(t *testing.T) {
		e := newTrueNASEnv(t, truenasOptions{})
		r := e.startBootstrap(t, strings.Repeat("a", 64))
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "no TrueNAS API key is saved") {
			t.Errorf("POST without a key = %d %s, want 422", r.status, r.body)
		}
		e.saveKey(t)
		e.tn.mu.Lock()
		e.tn.dialErr = homelab.ErrUnavailable
		e.tn.mu.Unlock()
		if r := e.startBootstrap(t, strings.Repeat("a", 64)); r.status != http.StatusBadGateway {
			t.Errorf("POST while unreachable = %d %s, want 502", r.status, r.body)
		}
	})
	t.Run("global env missing", func(t *testing.T) {
		e := newTrueNASEnv(t, truenasOptions{globalEnv: func(string) string { return "-" }})
		e.saveKey(t)
		v := e.environment(t)
		if v.Runnable || len(v.Problems) == 0 || !strings.Contains(v.Problems[0], "compose/.global.env does not exist") {
			t.Fatalf("view runnable %v problems %q, want the missing file first", v.Runnable, v.Problems)
		}
		if r := e.startBootstrap(t, v.Digest); r.status != http.StatusUnprocessableEntity {
			t.Errorf("POST = %d %s, want 422", r.status, r.body)
		}
	})
	t.Run("global env invalid", func(t *testing.T) {
		e := newTrueNASEnv(t, truenasOptions{globalEnv: func(string) string { return "PUID=1000\nPGID=\"unterminated\n" }})
		e.saveKey(t)
		v := e.environment(t)
		if v.Runnable || !slices.Contains(v.Problems, "compose/.global.env: line 2: unterminated quoted value") {
			t.Errorf("view runnable %v problems %q, want the parse error", v.Runnable, v.Problems)
		}
	})
	t.Run("self app is the networks app (criterion 8)", func(t *testing.T) {
		e := newTrueNASEnv(t, truenasOptions{selfApp: homelab.NetworksApp})
		e.saveKey(t)
		v := e.environment(t)
		if v.Runnable {
			t.Fatalf("plan runnable with ORCH_SELF_APP=%s", homelab.NetworksApp)
		}
		r := e.startBootstrap(t, v.Digest)
		if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "never modifies its own app") {
			t.Errorf("POST = %d %s, want 422 naming the self-app rule", r.status, r.body)
		}
		if len(e.tn.mutations()) != 0 {
			t.Errorf("the fake received %v", e.tn.mutations())
		}
	})
}

func TestBootstrapJobQueueing(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	e.saveKey(t)
	e.tn.mu.Lock()
	e.tn.block, e.tn.entered = make(chan struct{}), make(chan struct{})
	e.tn.mu.Unlock()
	v := e.environment(t)
	r := e.startBootstrap(t, v.Digest)
	if r.status != http.StatusAccepted {
		t.Fatalf("first POST = %d %s", r.status, r.body)
	}
	var started JobStarted
	decode(t, r, &started)
	<-e.tn.entered
	if r := e.startBootstrap(t, v.Digest); r.status != http.StatusConflict || !strings.Contains(r.body, "already running") {
		t.Errorf("second POST while running = %d %s, want 409", r.status, r.body)
	}
	close(e.tn.block)
	if j := e.waitJob(t, started.JobID, e.cookie); j.State != "succeeded" {
		t.Errorf("job = %s %v", j.State, j.Error)
	}
}

func TestBootstrapJobFailureIsRecorded(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	e.saveKey(t)
	e.tn.mu.Lock()
	e.tn.createDatasetErr = errors.New("truenas: pool.dataset.create: EINVAL: Path " + e.base + "/apps already exists")
	e.tn.mu.Unlock()
	j := e.runBootstrapWith(t, e.environment(t).Digest)
	if j.State != "failed" || j.Error == nil || !strings.Contains(*j.Error, "bootstrap: dataset tank/apps: truenas: pool.dataset.create: EINVAL") {
		t.Fatalf("job %s error %v, want failed naming the dataset", j.State, j.Error)
	}
	if last := j.Log[len(j.Log)-1]; last != "stopped: 2 created, 0 changed, 0 unchanged" {
		t.Errorf("last log line %q", last)
	}
	entries, _ := e.auditEntries(t, e.cookie)
	if diff := cmp.Diff([]string{auditBootstrapRun, auditUserCreate, auditGroupCreate, auditTrueNASSettingsWrite}, actions(entries)); diff != "" {
		t.Fatalf("audit actions mismatch (-want +got):\n%s", diff)
	}
	if d := entries[0].Details; d["state"] != "failed" || d["created"] != "2" {
		t.Errorf("bootstrap.run details %v, want failed with 2 created", d)
	}
}

// runBootstrapWith starts a bootstrap job with digest and waits for it.
func (e *truenasEnv) runBootstrapWith(t *testing.T, digest string) JobView {
	t.Helper()
	r := e.startBootstrap(t, digest)
	if r.status != http.StatusAccepted {
		t.Fatalf("POST bootstrap status = %d, want 202; body %s", r.status, r.body)
	}
	var started JobStarted
	decode(t, r, &started)
	return e.waitJob(t, started.JobID, e.cookie)
}

func TestBootstrapWhileShuttingDown(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	e.saveKey(t)
	v := e.environment(t)
	e.stopJobs()
	if r := e.startBootstrap(t, v.Digest); r.status != http.StatusServiceUnavailable {
		t.Errorf("POST after shutdown = %d %s, want 503", r.status, r.body)
	}
}
