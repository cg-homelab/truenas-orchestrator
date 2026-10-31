package truenas

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func TestNewError(t *testing.T) {
	tests := []struct {
		name     string
		wire     string
		want     *Error
		wantIs   error
		wantText string
	}{
		{
			name: "validation errors with fields",
			wire: `{"code":-32602,"message":"Invalid params","data":{"error":22,"errname":"EINVAL",` +
				`"reason":"[EINVAL] user_create.uid: Uid 1000 is already used.\n[EINVAL] user_create.username: taken\n",` +
				`"trace":{"frames":[{"locals":{"k":"LOCALVALUE"}}]},"py_exception":"gASV",` +
				`"extra":[["user_create.uid","Uid 1000 is already used.",17],["user_create.username","taken",22]]}}`,
			want: &Error{
				Method: "user.create", Code: -32602, Errno: 22, Errname: "EINVAL",
				Reason: "[EINVAL] user_create.uid: Uid 1000 is already used.; [EINVAL] user_create.username: taken",
				Fields: []FieldError{
					{Field: "user_create.uid", Message: "Uid 1000 is already used.", Errno: 17},
					{Field: "user_create.username", Message: "taken", Errno: 22},
				},
			},
			wantIs:   homelab.ErrValidation,
			wantText: "truenas: user.create: EINVAL (user_create.uid: Uid 1000 is already used.; user_create.username: taken)",
		},
		{
			name: "already exists: every field EEXIST",
			wire: `{"code":-32602,"message":"Invalid params","data":{"error":22,"errname":"EINVAL","reason":"x",` +
				`"extra":[["group_create.gid","Gid 1000 is already used.",17],["group_create.name","exists",17]]}}`,
			want: &Error{
				Method: "user.create", Code: -32602, Errno: 22, Errname: "EINVAL", Reason: "x",
				Fields: []FieldError{
					{Field: "group_create.gid", Message: "Gid 1000 is already used.", Errno: 17},
					{Field: "group_create.name", Message: "exists", Errno: 17},
				},
			},
			wantIs:   homelab.ErrConflict,
			wantText: "truenas: user.create: EINVAL (group_create.gid: Gid 1000 is already used.; group_create.name: exists)",
		},
		{
			name:     "validation without fields",
			wire:     `{"code":-32602,"message":"Invalid params","data":{"error":22,"errname":"EINVAL","reason":"bad","extra":null}}`,
			want:     &Error{Method: "user.create", Code: -32602, Errno: 22, Errname: "EINVAL", Reason: "bad"},
			wantIs:   homelab.ErrValidation,
			wantText: "truenas: user.create: EINVAL: bad",
		},
		{
			name:     "malformed errname falls back to the errno name",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":2,"errname":"bad-name <x>","reason":"gone"}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 2, Errname: "ENOENT", Reason: "gone"},
			wantIs:   homelab.ErrNotFound,
			wantText: "truenas: user.create: ENOENT: gone",
		},
		{
			name:     "malformed errname with an unknown errno",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":99,"errname":"1-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789","reason":"r"}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 99, Reason: "r"},
			wantText: "truenas: user.create: CALL_ERROR: r",
		},
		{
			name:     "call error ENOENT",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":2,"errname":"ENOENT","reason":"Path /x not found","trace":null,"extra":null}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 2, Errname: "ENOENT", Reason: "Path /x not found"},
			wantIs:   homelab.ErrNotFound,
			wantText: "truenas: user.create: ENOENT: Path /x not found",
		},
		{
			name:     "EEXIST",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":17,"errname":"EEXIST","reason":"exists","extra":{"odd":"shape"}}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 17, Errname: "EEXIST", Reason: "exists"},
			wantIs:   homelab.ErrConflict,
			wantText: "truenas: user.create: EEXIST: exists",
		},
		{
			name:     "EACCES",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":13,"errname":"EACCES","reason":"Not authorized"}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 13, Errname: "EACCES", Reason: "Not authorized"},
			wantIs:   homelab.ErrForbidden,
			wantText: "truenas: user.create: EACCES: Not authorized",
		},
		{
			name:     "not authenticated",
			wire:     `{"code":-32001,"message":"Method call error","data":{"error":207,"errname":"ENOTAUTHENTICATED","reason":"Not authenticated"}}`,
			want:     &Error{Method: "user.create", Code: -32001, Errno: 207, Errname: "ENOTAUTHENTICATED", Reason: "Not authenticated"},
			wantIs:   homelab.ErrUnauthenticated,
			wantText: "truenas: user.create: ENOTAUTHENTICATED: Not authenticated",
		},
		{
			name:     "method missing",
			wire:     `{"code":-32601,"message":"Method does not exist"}`,
			want:     &Error{Method: "user.create", Code: -32601, Reason: "Method does not exist"},
			wantIs:   homelab.ErrUnavailable,
			wantText: "truenas: user.create: METHOD_NOT_FOUND: Method does not exist: middleware API drift; run just smoke",
		},
		{
			name:     "too many calls",
			wire:     `{"code":-32000,"message":"Maximum number of concurrent calls (20) has exceeded"}`,
			want:     &Error{Method: "user.create", Code: -32000, Reason: "Maximum number of concurrent calls (20) has exceeded"},
			wantIs:   homelab.ErrUnavailable,
			wantText: "truenas: user.create: TOO_MANY_CONCURRENT_CALLS: Maximum number of concurrent calls (20) has exceeded",
		},
		{
			name:     "non-object data",
			wire:     `{"code":-32600,"message":"Invalid request","data":"text"}`,
			want:     &Error{Method: "user.create", Code: -32600, Reason: "Invalid request"},
			wantText: "truenas: user.create: INVALID_REQUEST: Invalid request",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w wireError
			if err := json.Unmarshal([]byte(tt.wire), &w); err != nil {
				t.Fatal(err)
			}
			got := newError("user.create", &w)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("newError() mismatch (-want +got):\n%s", diff)
			}
			if tt.wantIs != nil && !errors.Is(got, tt.wantIs) {
				t.Errorf("errors.Is(newError(), %v) = false", tt.wantIs)
			}
			if errors.Is(tt.wantIs, homelab.ErrConflict) && errors.Is(got, homelab.ErrValidation) {
				t.Errorf("an already-exists error also unwraps to ErrValidation")
			}
			if tt.wantIs == nil && got.Unwrap() != nil {
				t.Errorf("Unwrap() = %v, want nil", got.Unwrap())
			}
			if got.Error() != tt.wantText {
				t.Errorf("Error() = %q, want %q", got.Error(), tt.wantText)
			}
		})
	}
}

func TestScrubError(t *testing.T) {
	e := &Error{Errname: "K1", Reason: "bad K1", Fields: []FieldError{{Field: "a.K1", Message: "K1 rejected"}}}
	scrubError(e, "K1")
	want := &Error{Errname: "[REDACTED]", Reason: "bad [REDACTED]", Fields: []FieldError{{Field: "a.[REDACTED]", Message: "[REDACTED] rejected"}}}
	if diff := cmp.Diff(want, e); diff != "" {
		t.Errorf("scrubError() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeDatasetsUpperCasesProperties(t *testing.T) {
	raw := json.RawMessage(`[{"id":"tank/a","name":"tank/a","pool":"tank","encrypted":false,"locked":false,"mountpoint":null,
		"acltype":{"value":"posix"},"aclmode":{"value":"discard"},"atime":{"value":"off"},"compression":{"value":"zstd-3"},
		"recordsize":{"value":"128k"}},{"name":"tank/b","acltype":{"value":null}}]`)
	got, err := decodeDatasets(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []homelab.Dataset{
		{Name: "tank/a", Props: homelab.DatasetProps{ACLType: "POSIX", ACLMode: "DISCARD", Atime: "OFF", Compression: "ZSTD-3", RecordSize: "128K"}},
		{Name: "tank/b"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("decodeDatasets() mismatch (-want +got):\n%s", diff)
	}
}

func TestDecodeStatMasksTypeBits(t *testing.T) {
	got, err := decodeStat(json.RawMessage(`{"type":"SYMLINK","uid":1,"gid":2,"mode":41471,"acl":true,"is_mountpoint":false}`))
	if err != nil {
		t.Fatal(err)
	}
	want := homelab.PathStat{Exists: true, Type: homelab.PathSymlink, UID: 1, GID: 2, Mode: 0o777, ExtendedACL: true}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("decodeStat() mismatch (-want +got):\n%s", diff)
	}
}

func TestJobID(t *testing.T) {
	tests := []struct {
		raw    string
		want   int64
		wantOK bool
	}{
		{"1234", 1234, true},
		{" 7 ", 7, true},
		{"null", 0, false},
		{`{"name":"x"}`, 0, false},
		{`"12"`, 0, false},
		{"", 0, false},
		{"1.5", 0, false},
	}
	for _, tt := range tests {
		id, ok := jobID(json.RawMessage(tt.raw))
		if id != tt.want || ok != tt.wantOK {
			t.Errorf("jobID(%q) = %d, %v, want %d, %v", tt.raw, id, ok, tt.want, tt.wantOK)
		}
	}
}
