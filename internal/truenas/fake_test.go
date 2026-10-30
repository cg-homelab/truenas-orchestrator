package truenas

import (
	"context"
	"errors"
	"testing"
)

func TestFakeSystemVersion(t *testing.T) {
	tests := []struct {
		name string
		fake Fake
		want string
	}{
		{"zero value reports the default", Fake{}, DefaultFakeVersion},
		{"configured version", Fake{Version: "TrueNAS-SCALE-24.10.2"}, "TrueNAS-SCALE-24.10.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fake.SystemVersion(t.Context())
			if err != nil {
				t.Fatalf("SystemVersion() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("SystemVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFakeSystemVersionCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var f Fake
	if _, err := f.SystemVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("SystemVersion(cancelled ctx) error = %v, want context.Canceled", err)
	}
}
