package truenas

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func rowChecks(r SmokeReport) []string {
	var out []string
	for _, row := range r.Rows {
		status := "PASS"
		if !row.OK {
			status = "FAIL"
		}
		out = append(out, status+" "+row.Check)
	}
	return out
}

func TestSmokeAgainstFixtures(t *testing.T) {
	s := newFakeServer(t)
	logger, logs := newTestLogger()
	dir := t.TempDir()
	var out bytes.Buffer
	report, err := Smoke(t.Context(), Config{URL: s.url, Logger: logger}, testCreds(), &out, dir)
	if err != nil {
		t.Fatalf("Smoke() error = %v\n%s", err, out.String())
	}
	want := []string{
		"PASS endpoint", "PASS auth", "PASS version", "PASS jobs", "PASS docker", "PASS datasets", "PASS users",
		"PASS groups", "PASS stat", "PASS stat-absent", "PASS validation", "PASS networks", "PASS apps",
	}
	if diff := cmp.Diff(want, rowChecks(report)); diff != "" {
		t.Errorf("Smoke() rows mismatch (-want +got):\n%s\n%s", diff, out.String())
	}
	if report.Failed != 0 {
		t.Errorf("Smoke() Failed = %d, want 0", report.Failed)
	}
	if lines := strings.Count(out.String(), "\n"); lines != len(want) {
		t.Errorf("Smoke() printed %d lines, want %d", lines, len(want))
	}
	if !strings.Contains(out.String(), "property values upper-case") {
		t.Errorf("datasets row does not report the property case:\n%s", out.String())
	}
	for _, m := range s.methods() {
		if strings.Contains(m, "create") || m == "filesystem.setperm" {
			t.Errorf("smoke made a mutating call: %s", m)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "25.10"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	all := logs.String() + out.String()
	for _, e := range entries {
		names = append(names, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, "25.10", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		all += string(data)
		for _, scrubbed := range []string{`"unixhash"`, `"smbhash"`, `"sshpubkey"`, `"password_history"`, `"trace"`, `"py_exception"`} {
			if bytes.Contains(data, []byte(scrubbed)) {
				t.Errorf("%s still contains %s", e.Name(), scrubbed)
			}
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(data, &env); err != nil || !strings.HasPrefix(string(env["_provenance"]), `"recorded from TrueNAS-25.10.1`) {
			t.Errorf("%s: not an envelope with provenance: %v", e.Name(), err)
		}
	}
	wantNames := []string{
		"api.versions.json", "app.query.json", "core.bulk.json", "core.set_options.json", "docker.network.query.json",
		"docker.status.json", "filesystem.stat.einval.json", "filesystem.stat.enoent.json", "filesystem.stat.json",
		"group.query.json", "pool.dataset.query.json", "system.version.json", "user.query.json",
	}
	if diff := cmp.Diff(wantNames, names); diff != "" {
		t.Errorf("recorded files mismatch (-want +got):\n%s", diff)
	}
	if strings.Contains(all, testKey) {
		t.Errorf("the API key appears in the smoke output, log or recording")
	}

	var groups struct {
		Result []json.RawMessage `json:"result"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "25.10", "group.query.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &groups); err != nil || len(groups.Result) != smokeTrim {
		t.Errorf("recorded group.query has %d entries, want %d (%v)", len(groups.Result), smokeTrim, err)
	}
}

func TestSmokeFailures(t *testing.T) {
	noGroupKeys := `{"jsonrpc":"2.0","result":[{"id":1,"uid":0,"username":"root","builtin":true,"local":true,"group":{"id":1,"gid":0}}]}`
	tests := []struct {
		name       string
		opts       []fakeServerOption
		wantErr    error
		wantRows   []string
		wantFailed int
		wantDetail string
	}{
		{
			name:       "renamed user group keys",
			opts:       []fakeServerOption{withFixture("user.query.renamed", noGroupKeys), withRoute("user.query", "user.query.renamed")},
			wantFailed: 1,
			wantDetail: "missing keys: group.bsdgrp_gid, group.bsdgrp_group; " + smokeAction,
		},
		{
			name:       "jobs answered in legacy mode",
			opts:       []fakeServerOption{withRoute("core.bulk", "filesystem.setperm.legacy")},
			wantFailed: 1,
			wantDetail: "reply is job id 1234",
		},
		{
			name:       "version out of range",
			opts:       []fakeServerOption{withRoute("system.version", "system.version.25.04")},
			wantFailed: 1,
			wantDetail: "TrueNAS 25.04.2.6 is not supported",
		},
		{
			name:       "method missing",
			opts:       []fakeServerOption{withRoute("docker.status", "error.method_not_found")},
			wantFailed: 1,
			wantDetail: "middleware API drift; run just smoke",
		},
		{
			name:       "rejected key stops the run",
			opts:       []fakeServerOption{withRoute("auth.login_ex", "auth.login_ex.auth_err")},
			wantErr:    homelab.ErrUnauthenticated,
			wantRows:   []string{"PASS endpoint", "FAIL auth"},
			wantFailed: 1,
			wantDetail: "TrueNAS rejected the API key for user truenas_admin",
		},
		{
			name:       "pinned API not served",
			opts:       []fakeServerOption{withUpgradeStatus(http.StatusNotFound, "api.versions.25.04")},
			wantErr:    homelab.ErrValidation,
			wantRows:   []string{"FAIL endpoint"},
			wantFailed: 1,
			wantDetail: "none of which is v25.10.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newFakeServer(t, tt.opts...)
			var out bytes.Buffer
			report, err := Smoke(t.Context(), Config{URL: s.url}, testCreds(), &out, "")
			if tt.wantErr == nil && err != nil {
				t.Fatalf("Smoke() error = %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Smoke() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantRows != nil {
				if diff := cmp.Diff(tt.wantRows, rowChecks(report)); diff != "" {
					t.Errorf("rows mismatch (-want +got):\n%s", diff)
				}
			}
			if report.Failed != tt.wantFailed {
				t.Errorf("Failed = %d, want %d\n%s", report.Failed, tt.wantFailed, out.String())
			}
			if !strings.Contains(out.String(), tt.wantDetail) {
				t.Errorf("output lacks %q:\n%s", tt.wantDetail, out.String())
			}
		})
	}
}
