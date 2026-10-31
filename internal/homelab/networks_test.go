package homelab

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNetworksAppCompose(t *testing.T) {
	var doc struct {
		Networks map[string]struct {
			Name     string `yaml:"name"`
			Internal bool   `yaml:"internal"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal([]byte(NetworksAppCompose), &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Networks) != len(HomelabNetworks) {
		t.Errorf("declares %d networks, want %d", len(doc.Networks), len(HomelabNetworks))
	}
	for _, name := range HomelabNetworks {
		n, ok := doc.Networks[name]
		if !ok {
			t.Errorf("network %q missing", name)
			continue
		}
		if n.Name != name {
			t.Errorf("network %q has name %q", name, n.Name)
		}
		if want := name == "isolated"; n.Internal != want {
			t.Errorf("network %q internal = %v, want %v", name, n.Internal, want)
		}
	}
	for name := range doc.Networks {
		if !slices.Contains(HomelabNetworks, name) {
			t.Errorf("unexpected network %q", name)
		}
	}
}
