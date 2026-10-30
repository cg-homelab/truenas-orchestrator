package homelab

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SetupFile is the parsed setup.toml of one app: the primitive registry in
// docs/architecture/setup-toml.md. M1 only reads it, for [env_validators], requires and the
// declared paths that downgrade PATH001; M3 runs it.
type SetupFile struct {
	App           SetupApp                      `toml:"app"`
	Dirs          []SetupDir                    `toml:"dirs"`
	Secrets       []SetupSecret                 `toml:"secrets"`
	ConfigFiles   []SetupConfigFile             `toml:"config_files"`
	Prompts       []SetupPrompt                 `toml:"prompts"`
	EnvValidators map[string]ValueValidatorSpec `toml:"env_validators"`
	Provision     SetupProvision                `toml:"provision"`
}

// SetupApp is the [app] table.
type SetupApp struct {
	Name     string   `toml:"name"`
	Requires []string `toml:"requires"`
}

// SetupDir is one [[dirs]] entry.
type SetupDir struct {
	Path string `toml:"path"`
	Mode string `toml:"mode"`
}

// SetupGenerate is a generate = { bytes, encoding } spec.
type SetupGenerate struct {
	Bytes    int    `toml:"bytes"`
	Encoding string `toml:"encoding"`
}

// SetupSecret is one [[secrets]] entry.
type SetupSecret struct {
	File      string         `toml:"file"`
	Generate  *SetupGenerate `toml:"generate"`
	ValueFrom string         `toml:"value_from"`
	Mode      string         `toml:"mode"`
}

// SetupConfigFile is one [[config_files]] entry.
type SetupConfigFile struct {
	From        string `toml:"from"`
	To          string `toml:"to"`
	OnExists    string `toml:"on_exists"`
	CreateEmpty bool   `toml:"create_empty"`
}

// SetupPrompt is one [[prompts]] entry.
type SetupPrompt struct {
	Key      string         `toml:"key"`
	Label    string         `toml:"label"`
	Secret   bool           `toml:"secret"`
	Generate *SetupGenerate `toml:"generate"`
}

// SetupProvision is the [provision] table.
type SetupProvision struct {
	Postgres []SetupProvisionPostgres `toml:"postgres"`
}

// SetupProvisionPostgres is one [[provision.postgres]] entry.
type SetupProvisionPostgres struct {
	Stack        string `toml:"stack"`
	Database     string `toml:"database"`
	User         string `toml:"user"`
	PasswordFrom string `toml:"password_from"`
}

// SetupIssue is a problem found while decoding setup.toml (SETUP002).
type SetupIssue struct {
	// Key is the dotted key path, e.g. dirs.recursive_chown.
	Key     string
	Line    int
	Message string
}

// ParseSetup decodes setup.toml strictly against the primitive registry.
//
// A TOML syntax error (SETUP005) is returned as a *ParseError. Everything else is an issue
// (SETUP002) and decoding continues where possible, so requires and [env_validators] stay
// usable: an unknown key, an unknown validator type, and a value of the wrong type for a known
// key (requires = "a"). After a type error the file is decoded table by table, keeping every
// table that decodes.
func ParseSetup(src []byte) (SetupFile, []SetupIssue, error) {
	var raw map[string]any
	if err := toml.Unmarshal(src, &raw); err != nil {
		pe := &ParseError{Message: "not valid TOML"}
		var de *toml.DecodeError
		if errors.As(err, &de) {
			pe.Line, _ = de.Position()
			pe.Message = strings.TrimPrefix(de.Error(), "toml: ")
		}
		return SetupFile{}, nil, pe
	}

	var f SetupFile
	err := decodeStrict(src, &f)
	var strict *toml.StrictMissingError
	var de *toml.DecodeError
	// StrictMissingError unwraps to DecodeErrors, so it is tested first.
	if err == nil || errors.As(err, &strict) || !errors.As(err, &de) {
		return f, append(setupIssues(err, src, nil), validatorIssues(f, src)...), nil
	}

	// A type error stops go-toml at the first one. Report it, then decode each top-level table
	// on its own so the others still count.
	row, _ := de.Position()
	first := typeIssue(strings.Join(de.Key(), "."), row)
	issues := []SetupIssue{first}
	f = SetupFile{}
	for _, k := range sortedKeys(raw) {
		b, err := toml.Marshal(map[string]any{k: raw[k]})
		if err != nil {
			continue
		}
		var part SetupFile
		issues = append(issues, setupIssues(decodeStrict(b, &part), src, &first)...)
		_ = toml.Unmarshal(b, &f) // lenient merge; the strict pass above reported its problems
	}
	return f, append(issues, validatorIssues(f, src)...), nil
}

func decodeStrict(src []byte, v *SetupFile) error {
	dec := toml.NewDecoder(bytes.NewReader(src))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func typeIssue(key string, line int) SetupIssue {
	return SetupIssue{
		Key: key, Line: line,
		Message: fmt.Sprintf("setup.toml key %s has the wrong type for the primitive registry", key),
	}
}

// setupIssues converts a strict-decode error into issues. Lines come from the error when src is
// what was decoded (skip == nil), else from a scan of src for the key. skip is an issue already
// reported.
func setupIssues(err error, src []byte, skip *SetupIssue) []SetupIssue {
	line := func(de *toml.DecodeError, key []string) int {
		if skip == nil {
			row, _ := de.Position()
			return row
		}
		return tomlKeyLine(src, key[len(key)-1])
	}
	var out []SetupIssue
	var strict *toml.StrictMissingError
	var de *toml.DecodeError
	switch {
	case errors.As(err, &strict):
		for i := range strict.Errors {
			e := &strict.Errors[i]
			key := strings.Join(e.Key(), ".")
			out = append(out, SetupIssue{
				Key: key, Line: line(e, e.Key()),
				Message: fmt.Sprintf("setup.toml has unknown key %s, which is not a registered primitive", key),
			})
		}
	case errors.As(err, &de):
		key := strings.Join(de.Key(), ".")
		if skip == nil || key != skip.Key {
			out = append(out, typeIssue(key, line(de, de.Key())))
		}
	}
	return out
}

// validatorIssues reports [env_validators] entries whose type is not a registered validator.
func validatorIssues(f SetupFile, src []byte) []SetupIssue {
	var issues []SetupIssue
	for _, key := range sortedKeys(f.EnvValidators) {
		spec := f.EnvValidators[key]
		if knownValidator(spec.Type) {
			continue
		}
		issues = append(issues, SetupIssue{
			Key:     "env_validators." + key + ".type",
			Line:    tomlKeyLine(src, key),
			Message: fmt.Sprintf("setup.toml [env_validators] %s has unknown type %q", key, spec.Type),
		})
	}
	return issues
}

// tomlKeyLine returns the 1-based line of the first line assigning key (key = ...) or opening a
// table named key ([key], [[key]], [a.key]), or 0. go-toml does not expose value positions on a
// successful decode, so this is a line scan; it is used only to point a finding at a line.
func tomlKeyLine(src []byte, key string) int {
	k := regexp.QuoteMeta(key)
	re := regexp.MustCompile(`^\s*(\[{1,2}\s*([\w"-]+\.)*"?` + k + `"?\s*\]{1,2}|"?` + k + `"?\s*=)`)
	for i, line := range strings.Split(string(src), "\n") {
		if re.MatchString(line) {
			return i + 1
		}
	}
	return 0
}

// DeclaredPaths returns every host path setup.toml will create, uninterpolated: [[dirs]] path,
// [[secrets]] file and [[config_files]] to.
func (f SetupFile) DeclaredPaths() []string {
	var out []string
	for _, d := range f.Dirs {
		out = append(out, d.Path)
	}
	for _, s := range f.Secrets {
		out = append(out, s.File)
	}
	for _, c := range f.ConfigFiles {
		out = append(out, c.To)
	}
	return out
}
