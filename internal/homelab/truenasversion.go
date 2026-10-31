package homelab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TrueNASVersion is a parsed TrueNAS version string.
type TrueNASVersion struct {
	// Major, Minor, Patch and Build: 25.10.0.1 is 25, 10, 0, 1; missing parts are 0.
	Major, Minor, Patch, Build int
	// Pre is "RC.1", "BETA.1" or "MASTER-..."; empty for a release.
	Pre string
	// Raw is the string that was parsed.
	Raw string
}

var truenasVersionRE = regexp.MustCompile(`^(?:TrueNAS-(?:SCALE-)?)?(\d+)\.(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-(.+))?$`)

// ParseTrueNASVersion accepts "TrueNAS-25.10.1", "TrueNAS-SCALE-24.10.2", "TrueNAS-27.0.0-RC.1",
// "TrueNAS-25.10-RC.1" and a bare "25.10.0.1".
func ParseTrueNASVersion(s string) (TrueNASVersion, error) {
	m := truenasVersionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return TrueNASVersion{}, fmt.Errorf("unrecognised TrueNAS version %q: %w", truncateRunes(s, 64), ErrValidation)
	}
	var nums [4]int
	for i := range nums {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return TrueNASVersion{}, fmt.Errorf("unrecognised TrueNAS version %q: %w", truncateRunes(s, 64), ErrValidation)
		}
		nums[i] = n
	}
	return TrueNASVersion{
		Major: nums[0], Minor: nums[1], Patch: nums[2], Build: nums[3],
		Pre: m[5], Raw: s,
	}, nil
}

// String formats v as dotted numbers with the pre-release suffix, e.g. "25.10.0.1" or "27.0.0-RC.1".
func (v TrueNASVersion) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Build != 0 {
		s += "." + strconv.Itoa(v.Build)
	}
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// VersionRange is a half-open range of TrueNAS versions: (major, minor) >= (MinMajor, MinMinor)
// and major < BelowMajor.
type VersionRange struct {
	MinMajor, MinMinor, BelowMajor int
}

// String formats r for messages, e.g. "TrueNAS 25.10 through 27.x".
func (r VersionRange) String() string {
	return fmt.Sprintf("TrueNAS %d.%d through %d.x", r.MinMajor, r.MinMinor, r.BelowMajor-1)
}

// Contains reports whether v lies in r.
func (r VersionRange) Contains(v TrueNASVersion) bool {
	if v.Major >= r.BelowMajor {
		return false
	}
	return v.Major > r.MinMajor || (v.Major == r.MinMajor && v.Minor >= r.MinMinor)
}

// SupportedTrueNAS is the range this orchestrator supports (D36): (major, minor) >= (25, 10) and
// major < 28.
var SupportedTrueNAS = VersionRange{MinMajor: 25, MinMinor: 10, BelowMajor: 28}

// CheckTrueNASVersion returns nil when raw is a supported version, or an *UnsupportedVersionError,
// which wraps ErrValidation.
func CheckTrueNASVersion(raw string) error {
	v, err := ParseTrueNASVersion(raw)
	if err != nil {
		return &UnsupportedVersionError{Detected: truncateRunes(raw, 64), Supported: SupportedTrueNAS.String()}
	}
	if !SupportedTrueNAS.Contains(v) {
		return &UnsupportedVersionError{Detected: stripTrueNASPrefix(v.Raw), Supported: SupportedTrueNAS.String()}
	}
	return nil
}

// UnsupportedVersionError reports a TrueNAS version outside SupportedTrueNAS.
type UnsupportedVersionError struct {
	// Detected is the version as parsed, e.g. "25.04.2.6"; the raw string truncated to 64 runes
	// if it was unparseable. Empty when the version is unknown and ServedAPIs is set.
	Detected string
	// Supported is SupportedTrueNAS.String().
	Supported string
	// ServedAPIs lists the API versions the server offers, set when the pinned endpoint is missing.
	ServedAPIs []string
}

// Error implements error.
func (e *UnsupportedVersionError) Error() string {
	if e.Detected == "" && len(e.ServedAPIs) > 0 {
		return fmt.Sprintf("TrueNAS serves API versions %s, none of which is v25.10.0: this orchestrator supports %s",
			strings.Join(e.ServedAPIs, ", "), e.Supported)
	}
	return fmt.Sprintf("TrueNAS %s is not supported: this orchestrator supports %s",
		e.Detected, e.Supported)
}

// Unwrap returns ErrValidation.
func (e *UnsupportedVersionError) Unwrap() error { return ErrValidation }

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func stripTrueNASPrefix(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "TrueNAS-")
	return strings.TrimPrefix(s, "SCALE-")
}
