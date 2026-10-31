package homelab

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseTrueNASVersion(t *testing.T) {
	tests := []struct {
		in   string
		want TrueNASVersion
	}{
		{"TrueNAS-25.10.1", TrueNASVersion{Major: 25, Minor: 10, Patch: 1}},
		{"TrueNAS-SCALE-24.10.2", TrueNASVersion{Major: 24, Minor: 10, Patch: 2}},
		{"TrueNAS-27.0.0-RC.1", TrueNASVersion{Major: 27, Pre: "RC.1"}},
		{"TrueNAS-25.10-RC.1", TrueNASVersion{Major: 25, Minor: 10, Pre: "RC.1"}},
		{"TrueNAS-25.10.0.1", TrueNASVersion{Major: 25, Minor: 10, Build: 1}},
		{"25.10.0.1", TrueNASVersion{Major: 25, Minor: 10, Build: 1}},
		{"TrueNAS-27.0.0-MASTER-20260101-1234", TrueNASVersion{Major: 27, Pre: "MASTER-20260101-1234"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseTrueNASVersion(tt.in)
			if err != nil {
				t.Fatalf("ParseTrueNASVersion(%q): %v", tt.in, err)
			}
			tt.want.Raw = tt.in
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
	for _, in := range []string{"", "garbage", "TrueNAS-", "25", "TrueNAS-SCALE-x.y"} {
		if _, err := ParseTrueNASVersion(in); !errors.Is(err, ErrValidation) {
			t.Errorf("ParseTrueNASVersion(%q) error = %v, want ErrValidation", in, err)
		}
	}
}

func TestCheckTrueNASVersion(t *testing.T) {
	tests := []struct {
		in      string
		wantErr string // empty: supported
	}{
		{"TrueNAS-25.10.1", ""},
		{"TrueNAS-25.10-RC.1", ""},
		{"25.10.0.1", ""},
		{"TrueNAS-27.0.0-RC.1", ""},
		{"TrueNAS-27.0.0-MASTER-20260101-1234", ""},
		{"TrueNAS-SCALE-24.10.2", "TrueNAS 24.10.2 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"},
		{"25.04.2.6", "TrueNAS 25.04.2.6 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"},
		{"28.0.0-RC.1", "TrueNAS 28.0.0-RC.1 is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"},
		{"", "TrueNAS  is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"},
		{"garbage", "TrueNAS garbage is not supported: this orchestrator supports TrueNAS 25.10 through 27.x"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			err := CheckTrueNASVersion(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("message = %q, want %q", err.Error(), tt.wantErr)
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("error does not wrap ErrValidation")
			}
			var uv *UnsupportedVersionError
			if !errors.As(err, &uv) {
				t.Errorf("error is not an *UnsupportedVersionError")
			}
		})
	}
}

func TestCheckTrueNASVersionTruncates(t *testing.T) {
	err := CheckTrueNASVersion(strings.Repeat("é", 100))
	var uv *UnsupportedVersionError
	if !errors.As(err, &uv) {
		t.Fatalf("error = %v", err)
	}
	if got := len([]rune(uv.Detected)); got != 64 {
		t.Errorf("Detected has %d runes, want 64", got)
	}
}

func TestUnsupportedVersionErrorServedAPIs(t *testing.T) {
	err := &UnsupportedVersionError{
		Supported:  SupportedTrueNAS.String(),
		ServedAPIs: []string{"v24.10.0", "v25.04.0"},
	}
	want := "TrueNAS serves API versions v24.10.0, v25.04.0, none of which is v25.10.0: this orchestrator supports TrueNAS 25.10 through 27.x"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, ErrValidation) {
		t.Error("does not wrap ErrValidation")
	}
}

func TestVersionRangeString(t *testing.T) {
	if got, want := SupportedTrueNAS.String(), "TrueNAS 25.10 through 27.x"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
