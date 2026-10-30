package server

import (
	"context"
	"net/http"
	"runtime"
	"runtime/debug"

	"github.com/danielgtaylor/huma/v2"
)

// devVersion is reported when the binary carries no version or VCS information.
const devVersion = "dev"

// readBuildInfo reads the binary's build info. version, when non-empty, overrides the module
// version. Missing values become "dev".
func readBuildInfo(version string) HealthView {
	info := HealthView{Version: version, Commit: devVersion, GoVersion: runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				info.Commit = s.Value
			}
		}
	}
	if info.Version == "" {
		info.Version = devVersion
	}
	return info
}

// HealthView is the health endpoint's response body.
type HealthView struct {
	Version   string `json:"version" doc:"Orchestrator version, or dev for an unversioned build"`
	Commit    string `json:"commit" doc:"VCS revision the binary was built from, or dev"`
	GoVersion string `json:"go_version" doc:"Go toolchain the binary was built with"`
}

// GetHealthOutput is the get-health response.
type GetHealthOutput struct {
	Body HealthView
}

var opGetHealth = huma.Operation{
	OperationID: "get-health",
	Method:      http.MethodGet,
	Path:        "/api/health",
	Summary:     "Report liveness and build information",
	Tags:        []string{"system"},
}

func handleGetHealth(info HealthView) func(context.Context, *struct{}) (*GetHealthOutput, error) {
	return func(context.Context, *struct{}) (*GetHealthOutput, error) {
		return &GetHealthOutput{Body: info}, nil
	}
}
