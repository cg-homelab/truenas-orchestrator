package homelab

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
// usable: an unknown key, an unknown validator type, a value of the wrong type for a known
// key (requires = "a"), and a value that breaks a primitive's rules (a mode that is not octal,
// a secret with both generate and value_from, an on_exists outside keep/overwrite/prompt). After a type error the file is decoded table by table, keeping every
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
		issues := append(setupIssues(err, src, nil), validatorIssues(f, src)...)
		return f, append(issues, fieldIssueList(f, src)...), nil
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
	issues = append(issues, validatorIssues(f, src)...)
	return f, append(issues, fieldIssueList(f, src)...), nil
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

// Default modes for entries that declare none (go-guidelines section 8).
const (
	defaultDirMode    FileMode = 0o750
	defaultSecretMode FileMode = 0o600
	defaultConfigMode FileMode = 0o640
)

// Generate encodings and on_exists values of the primitive registry.
const (
	encodingBase64       = "base64"
	encodingHex          = "hex"
	encodingAlphanumeric = "alphanumeric"

	onExistsKeep      = "keep"
	onExistsOverwrite = "overwrite"
	onExistsPrompt    = "prompt"

	maxGenerateBytes = 1024
)

// envKeyRe is a variable name as Compose and ParseDotEnv accept it in a .env file.
var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// fieldIssue is a setup.toml value that decodes but breaks the rules of its primitive.
type fieldIssue struct {
	table string // dirs, secrets, config_files or prompts
	index int    // 0-based entry in the array of tables
	key   string // key within the entry (mode, generate.bytes); "" for the entry as a whole
	msg   string // what is wrong, never the value
}

// text renders the issue for a finding or a plan problem: "setup.toml [[dirs]] #2 mode must ...".
func (fi fieldIssue) text() string {
	if fi.key == "" {
		return fmt.Sprintf("setup.toml [[%s]] #%d %s", fi.table, fi.index+1, fi.msg)
	}
	return fmt.Sprintf("setup.toml [[%s]] #%d %s %s", fi.table, fi.index+1, fi.key, fi.msg)
}

// fieldIssueList converts field issues into SETUP002 issues with the line of the offending key.
func fieldIssueList(f SetupFile, src []byte) []SetupIssue {
	var out []SetupIssue
	for _, fi := range setupFieldIssues(f) {
		key, first := fi.table, ""
		if fi.key != "" {
			key += "." + fi.key
			first, _, _ = strings.Cut(fi.key, ".")
		}
		out = append(out, SetupIssue{Key: key, Line: entryLine(src, fi.table, fi.index, first), Message: fi.text()})
	}
	return out
}

// setupFieldIssues checks every entry of dirs, secrets, config_files and prompts against the
// rules of its primitive. ParseSetup reports the result as SETUP002; PlanSetup refuses to plan a
// file that has any.
func setupFieldIssues(f SetupFile) []fieldIssue {
	var out []fieldIssue
	add := func(table string, i int, key, msg string) {
		out = append(out, fieldIssue{table: table, index: i, key: key, msg: msg})
	}
	for i, d := range f.Dirs {
		if strings.TrimSpace(d.Path) == "" {
			add("dirs", i, "path", "must not be empty")
		}
		if d.Mode != "" {
			if _, ok := parseMode(d.Mode); !ok {
				add("dirs", i, "mode", modeMessage)
			}
		}
	}
	for i, s := range f.Secrets {
		if strings.TrimSpace(s.File) == "" {
			add("secrets", i, "file", "must not be empty")
		}
		if s.Mode != "" {
			m, ok := parseMode(s.Mode)
			switch {
			case !ok:
				add("secrets", i, "mode", modeMessage)
			case m&0o077 != 0:
				add("secrets", i, "mode", "must not grant group or other access (e.g. 0600 or 0400)")
			}
		}
		switch {
		case (s.Generate == nil) == (s.ValueFrom == ""):
			add("secrets", i, "", "needs exactly one of generate and value_from")
		case s.Generate != nil:
			for _, gi := range generateIssues(*s.Generate) {
				add("secrets", i, gi[0], gi[1])
			}
		case !envKeyRe.MatchString(s.ValueFrom):
			add("secrets", i, "value_from", "must be a variable name ([A-Za-z_][A-Za-z0-9_]*)")
		}
	}
	for i, c := range f.ConfigFiles {
		if strings.TrimSpace(c.To) == "" {
			add("config_files", i, "to", "must not be empty")
		}
		switch {
		case (c.From == "") == !c.CreateEmpty:
			add("config_files", i, "", "needs exactly one of from and create_empty = true")
		case c.From != "" && !relInside(c.From):
			add("config_files", i, "from", "must be a relative path inside the app directory (no leading /, no .. segment)")
		case c.From != "" && isEnvFileName(c.From):
			add("config_files", i, "from", "must not be an env file (.env or .global.env); secrets reach the host only through [[secrets]]")
		}
		switch c.OnExists {
		case "", onExistsKeep, onExistsOverwrite, onExistsPrompt:
		default:
			add("config_files", i, "on_exists", "must be one of keep, overwrite, prompt")
		}
	}
	for i, p := range f.Prompts {
		if !envKeyRe.MatchString(p.Key) {
			add("prompts", i, "key", "must be a variable name ([A-Za-z_][A-Za-z0-9_]*)")
		}
		if p.Generate != nil {
			for _, gi := range generateIssues(*p.Generate) {
				add("prompts", i, gi[0], gi[1])
			}
		}
	}
	return out
}

const modeMessage = "must be an octal permission mode such as 0750 (3 or 4 digits, no setuid, setgid or sticky bit)"

// generateIssues checks a generate = { bytes, encoding } spec; each issue is {key, message}.
func generateIssues(g SetupGenerate) [][2]string {
	var out [][2]string
	if g.Bytes < 1 || g.Bytes > maxGenerateBytes {
		out = append(out, [2]string{"generate.bytes", fmt.Sprintf("must be between 1 and %d", maxGenerateBytes)})
	}
	switch g.Encoding {
	case "", encodingBase64, encodingHex, encodingAlphanumeric:
	default:
		out = append(out, [2]string{"generate.encoding", "must be one of base64, hex, alphanumeric"})
	}
	return out
}

// parseMode parses a 3 or 4 digit octal permission mode. A fourth digit must be 0: setuid,
// setgid and sticky bits are not permission bits and are refused rather than silently dropped.
func parseMode(s string) (FileMode, bool) {
	if len(s) < 3 || len(s) > 4 || (len(s) == 4 && s[0] != '0') {
		return 0, false
	}
	var m FileMode
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return 0, false
		}
		m = m<<3 | FileMode(s[i]-'0')
	}
	return m, true
}

// modeOr returns the mode s declares, or def when s is empty. s must have passed parseMode.
func modeOr(s string, def FileMode) FileMode {
	if m, ok := parseMode(s); ok {
		return m
	}
	return def
}

// relInside reports whether p is a relative slash path with no ".." segment.
func relInside(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	return !slices.Contains(strings.Split(p, "/"), "..")
}

// entryLine returns the 1-based line of key inside the index-th [[table]] entry, the entry's
// header line when key is "" or absent, or the line tomlKeyLine finds for table when the file
// has no such header (an inline array of tables).
func entryLine(src []byte, table string, index int, key string) int {
	header := regexp.MustCompile(`^\s*\[\[\s*` + regexp.QuoteMeta(table) + `\s*\]\]`)
	anyTable := regexp.MustCompile(`^\s*\[`)
	keyRe := regexp.MustCompile(`^\s*"?` + regexp.QuoteMeta(key) + `"?\s*=`)
	n, headerLine := -1, 0
	for i, line := range strings.Split(string(src), "\n") {
		if header.MatchString(line) {
			n++
			if n > index {
				break
			}
			if n == index {
				headerLine = i + 1
			}
			continue
		}
		if n != index {
			continue
		}
		if anyTable.MatchString(line) {
			break
		}
		if key != "" && keyRe.MatchString(line) {
			return i + 1
		}
	}
	if headerLine == 0 {
		return tomlKeyLine(src, table)
	}
	return headerLine
}
