package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestTrueNASSettings(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	var bodies []string
	do := func(method string, body any) response {
		t.Helper()
		r := e.do(t, method, "/api/truenas/settings", body, e.cookie)
		bodies = append(bodies, r.body)
		return r
	}
	getSettings := func() TrueNASSettings {
		t.Helper()
		r := do(http.MethodGet, nil)
		if r.status != http.StatusOK {
			t.Fatalf("GET settings status = %d; body %s", r.status, r.body)
		}
		var s TrueNASSettings
		decode(t, r, &s)
		return s
	}

	if diff := cmp.Diff(TrueNASSettings{URL: "wss://truenas.test", SelfApp: homelab.DefaultSelfApp}, getSettings()); diff != "" {
		t.Errorf("settings before saving mismatch (-want +got):\n%s", diff)
	}

	// Refusals store nothing.
	refusals := []struct {
		name       string
		dialErr    error
		version    string
		body       map[string]string
		wantStatus int
		wantMsg    string
	}{
		{
			name: "wrong key", body: map[string]string{"username": "truenas_admin", "api_key": "1-wrong"},
			wantStatus: http.StatusUnprocessableEntity, wantMsg: "TrueNAS rejected the API key for user truenas_admin",
		},
		{
			name: "unsupported version", version: "TrueNAS-25.04.2.6",
			body:       map[string]string{"username": "truenas_admin", "api_key": testAPIKey},
			wantStatus: http.StatusUnprocessableEntity,
			wantMsg:    "TrueNAS 25.04.2.6 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x",
		},
		{
			name: "unreachable", dialErr: fmt.Errorf("TrueNAS unreachable at wss://truenas.test: x509: unknown authority: %w", homelab.ErrUnavailable),
			body:       map[string]string{"username": "truenas_admin", "api_key": testAPIKey},
			wantStatus: http.StatusBadGateway, wantMsg: "TrueNAS unreachable at wss://truenas.test",
		},
		{
			name: "no username", body: map[string]string{"username": "", "api_key": testAPIKey},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "no key", body: map[string]string{"username": "truenas_admin"},
			wantStatus: http.StatusUnprocessableEntity,
		},
	}
	for _, tt := range refusals {
		t.Run(tt.name, func(t *testing.T) {
			e.tn.mu.Lock()
			e.tn.dialErr, e.tn.version = tt.dialErr, "TrueNAS-25.10.1"
			if tt.version != "" {
				e.tn.version = tt.version
			}
			e.tn.mu.Unlock()
			r := do(http.MethodPut, tt.body)
			if r.status != tt.wantStatus || !strings.Contains(r.body, tt.wantMsg) {
				t.Errorf("PUT settings status = %d body %s, want %d with %q", r.status, r.body, tt.wantStatus, tt.wantMsg)
			}
			if getSettings().Configured {
				t.Errorf("a refused key was stored")
			}
		})
	}
	e.tn.mu.Lock()
	e.tn.dialErr, e.tn.version = nil, "TrueNAS-25.10.1"
	e.tn.mu.Unlock()

	r := do(http.MethodPut, map[string]string{"username": "truenas_admin", "api_key": testAPIKey})
	if r.status != http.StatusOK {
		t.Fatalf("PUT settings status = %d, want 200; body %s", r.status, r.body)
	}
	var saved TrueNASSettingsSaved
	decode(t, r, &saved)
	if diff := cmp.Diff(TrueNASSettingsSaved{Configured: true, Version: "TrueNAS-25.10.1"}, saved); diff != "" {
		t.Errorf("PUT response mismatch (-want +got):\n%s", diff)
	}
	if got := getSettings(); !got.Configured || got.Username != "truenas_admin" {
		t.Errorf("settings after saving = %+v, want configured for truenas_admin", got)
	}
	creds, err := e.store.TrueNASCredentials(t.Context())
	if err != nil || creds.APIKey.Reveal() != testAPIKey {
		t.Errorf("stored credentials %v, %v, want the key", creds, err)
	}
	if dials, closes := e.tn.sessions(); dials != closes+3 { // the three refused dials returned no session
		t.Errorf("dials %d closes %d: every session must be closed", dials, closes)
	}

	if r := do(http.MethodDelete, nil); r.status != http.StatusNoContent {
		t.Fatalf("DELETE settings status = %d, want 204; body %s", r.status, r.body)
	}
	if getSettings().Configured {
		t.Errorf("settings still configured after delete")
	}
	if r := do(http.MethodDelete, nil); r.status != http.StatusNoContent {
		t.Errorf("second DELETE status = %d, want 204", r.status)
	}

	entries, body := e.auditEntries(t, e.cookie)
	if diff := cmp.Diff([]string{auditTrueNASSettingsDelete, auditTrueNASSettingsWrite}, actions(entries)); diff != "" {
		t.Errorf("audit actions mismatch (-want +got):\n%s", diff)
	}
	for _, en := range entries {
		if diff := cmp.Diff(map[string]string{"username": "truenas_admin"}, en.Details); diff != "" || en.Target != "truenas" || en.User != testUser {
			t.Errorf("audit entry %+v; details mismatch (-want +got):\n%s", en, diff)
		}
	}
	// Criterion 6: the key never reaches a response, the log or the audit trail.
	assertNoKey(t, "the audit log", body)
	assertNoKey(t, "a response", strings.Join(bodies, "\n"))
	assertNoKey(t, "the server log", e.logs.String())
}

func TestTrueNASSettingsKeyNeverEchoedOnBadInput(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{})
	for _, body := range []any{
		map[string]any{"username": "truenas_admin", "api_key": 12345, "note": testAPIKey},
		map[string]any{"username": strings.Repeat("u", 65), "api_key": testAPIKey},
		rawBody(`{"username":"truenas_admin","api_key":"` + testAPIKey + `"`),
	} {
		r := e.do(t, http.MethodPut, "/api/truenas/settings", body, e.cookie)
		if r.status < 400 {
			t.Errorf("PUT %v status = %d, want an error", body, r.status)
		}
		assertNoKey(t, "an error response", r.body)
	}
	assertNoKey(t, "the server log", e.logs.String())
}

func TestTrueNASSettingsWithoutDialer(t *testing.T) {
	e := newTrueNASEnv(t, truenasOptions{noDialer: true})
	r := e.do(t, http.MethodPut, "/api/truenas/settings", map[string]string{"username": "truenas_admin", "api_key": testAPIKey}, e.cookie)
	if r.status != http.StatusUnprocessableEntity || !strings.Contains(r.body, "ORCH_TRUENAS_URL") {
		t.Errorf("PUT settings without a dialer = %d %s, want 422 naming ORCH_TRUENAS_URL", r.status, r.body)
	}
	var s TrueNASSettings
	decode(t, e.do(t, http.MethodGet, "/api/truenas/settings", nil, e.cookie), &s)
	if s.URL != "" || s.Configured {
		t.Errorf("settings without a dialer = %+v, want no url and not configured", s)
	}
	v := e.environment(t)
	if v.Connection.State != connNotConfigured || !strings.Contains(v.Connection.Message, "ORCH_TRUENAS_URL") || v.Ready || v.Runnable {
		t.Errorf("connection without a dialer = %+v ready %v runnable %v, want not_configured", v.Connection, v.Ready, v.Runnable)
	}
}

func TestEnvironmentConnectionStates(t *testing.T) {
	tests := []struct {
		name        string
		saveKey     bool
		dialErr     error
		version     string
		wantState   string
		wantMsg     string
		wantVersion string
	}{
		{name: "no key saved", wantState: connNotConfigured, wantMsg: "no TrueNAS API key is saved"},
		{
			name: "key revoked", saveKey: true,
			dialErr:   fmt.Errorf("the API key is expired or revoked; TrueNAS revokes a key used over plain HTTP: %w", homelab.ErrUnauthenticated),
			wantState: connAuthFailed, wantMsg: "expired or revoked",
		},
		{
			name: "unsupported version", saveKey: true, version: "TrueNAS-28.0.0-RC.1",
			wantState: connUnsupportedVersion, wantVersion: "28.0.0-RC.1",
			wantMsg: "TrueNAS 28.0.0-RC.1 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x",
		},
		{
			name: "unreachable", saveKey: true, dialErr: fmt.Errorf("TrueNAS unreachable at wss://truenas.test: %w", homelab.ErrUnavailable),
			wantState: connUnreachable, wantMsg: "TrueNAS unreachable at wss://truenas.test",
		},
		{
			name: "something else", saveKey: true, dialErr: errors.New("boom with internal detail"),
			wantState: connError, wantMsg: msgDialFailed,
		},
		{name: "connected", saveKey: true, wantState: connOK, wantMsg: "connected to TrueNAS 25.10.1", wantVersion: "25.10.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTrueNASEnv(t, truenasOptions{})
			if tt.saveKey {
				e.saveKey(t)
			}
			e.tn.mu.Lock()
			e.tn.dialErr = tt.dialErr
			if tt.version != "" {
				e.tn.version = tt.version
			}
			e.tn.mu.Unlock()
			v := e.environment(t)
			want := ConnectionView{State: tt.wantState, DetectedVersion: tt.wantVersion, Supported: "TrueNAS 25.10 through 27.x"}
			got := v.Connection
			if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(ConnectionView{}, "Message")); diff != "" || !strings.Contains(got.Message, tt.wantMsg) {
				t.Errorf("connection message %q, want %q; mismatch (-want +got):\n%s", got.Message, tt.wantMsg, diff)
			}
			if tt.wantState != connOK && (v.Ready || v.Runnable || len(v.Items) != 0 || len(v.Steps) != 0 || v.Digest != "") {
				t.Errorf("not connected but view = %+v", v)
			}
			if strings.Contains(e.logs.String(), "boom") != (tt.wantState == connError) {
				t.Errorf("an unexpected dial error must be logged, and only then")
			}
		})
	}
}
