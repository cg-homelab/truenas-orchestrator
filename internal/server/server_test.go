package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
)

const indexHTML = "<!doctype html><title>spa</title>"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	spa := fstest.MapFS{
		"index.html":          {Data: []byte(indexHTML)},
		"_app/immutable/a.js": {Data: []byte("console.log(1)")},
	}
	srv := httptest.NewServer(NewServer(Deps{
		Logger: slog.New(slog.DiscardHandler),
		SPA:    spa,
	}))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, method, path string) (status int, contentType, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	status, ctype, body := get(t, srv, http.MethodGet, "/api/health")
	if status != http.StatusOK {
		t.Fatalf("GET /api/health status = %d, want 200; body %s", status, body)
	}
	if !strings.HasPrefix(ctype, "application/json") {
		t.Errorf("GET /api/health Content-Type = %q, want application/json", ctype)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode health body %q: %v", body, err)
	}
	for k, v := range got {
		if v == "" {
			t.Errorf("health %q is empty", k)
		}
	}
	if diff := cmp.Diff([]string{"commit", "go_version", "version"}, slices.Sorted(maps.Keys(got))); diff != "" {
		t.Errorf("health keys mismatch (-want +got):\n%s", diff)
	}
}

func TestRouting(t *testing.T) {
	srv := newTestServer(t)
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantType   string // prefix
		wantBody   string // substring
	}{
		{"unknown api path is a json 404", http.MethodGet, "/api/nope", http.StatusNotFound, "application/problem+json", `"status":404`},
		{"unknown nested api path is a json 404", http.MethodGet, "/api/stacks/x/y", http.StatusNotFound, "application/problem+json", `"status":404`},
		{"wrong method on api path is a json 404", http.MethodPost, "/api/health", http.StatusNotFound, "application/problem+json", `"status":404`},
		{"root serves index.html", http.MethodGet, "/", http.StatusOK, "text/html", indexHTML},
		{"spa route falls back to index.html", http.MethodGet, "/some/spa/route", http.StatusOK, "text/html", indexHTML},
		{"static asset is served", http.MethodGet, "/_app/immutable/a.js", http.StatusOK, "text/javascript", "console.log(1)"},
		{"post to spa path is rejected", http.MethodPost, "/some/spa/route", http.StatusMethodNotAllowed, "text/plain", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, ctype, body := get(t, srv, tt.method, tt.path)
			if status != tt.wantStatus {
				t.Errorf("%s %s status = %d, want %d; body %s", tt.method, tt.path, status, tt.wantStatus, body)
			}
			if !strings.HasPrefix(ctype, tt.wantType) {
				t.Errorf("%s %s Content-Type = %q, want prefix %q", tt.method, tt.path, ctype, tt.wantType)
			}
			if !strings.Contains(body, tt.wantBody) {
				t.Errorf("%s %s body = %q, want it to contain %q", tt.method, tt.path, body, tt.wantBody)
			}
		})
	}
}

func TestNilSPAServes404(t *testing.T) {
	srv := httptest.NewServer(NewServer(Deps{}))
	t.Cleanup(srv.Close)
	if status, _, _ := get(t, srv, http.MethodGet, "/"); status != http.StatusNotFound {
		t.Errorf("GET / with nil SPA status = %d, want 404", status)
	}
}

func TestRecoverPanics(t *testing.T) {
	h := recoverPanics(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status after panic = %d, want 500", rec.Code)
	}
}

func TestOpenAPIHasHealth(t *testing.T) {
	api := NewAPI(http.NewServeMux(), Deps{})
	b, err := api.OpenAPI().MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	if got := doc.Paths["/api/health"]["get"].OperationID; got != "get-health" {
		t.Errorf("operationId of GET /api/health = %q, want get-health", got)
	}
	if strings.Contains(string(b), "$schema") {
		t.Errorf("openapi document contains $schema links; CreateHooks should be nil")
	}
}

func TestOpenAPIVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"zero deps is dev, not build info", "", "dev"},
		{"explicit version", "v1.2.3", "v1.2.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewAPI(http.NewServeMux(), Deps{Version: tt.version})
			if got := api.OpenAPI().Info.Version; got != tt.want {
				t.Errorf("NewAPI(Deps{Version: %q}).OpenAPI().Info.Version = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}
