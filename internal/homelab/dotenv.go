package homelab

import (
	"strings"
)

// DotEnv is a parsed env file (.global.env, <app>/.env, example.env or any env_file: target).
type DotEnv struct {
	// Entries holds one entry per key, in order of first appearance; a repeated key keeps the
	// last value and line, as Compose does.
	Entries []DotEnvEntry
}

// DotEnvEntry is one KEY=VALUE statement.
type DotEnvEntry struct {
	Key   string
	Value Secret
	// Line is the 1-based line the statement starts on.
	Line int
}

// Lookup returns the entry for key.
func (d DotEnv) Lookup(key string) (DotEnvEntry, bool) {
	for _, e := range d.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return DotEnvEntry{}, false
}

// ParseDotEnv parses env-file bytes with compose-go's dotenv rules for what the homelab repo
// uses: KEY=VALUE statements, blank lines and # comments, an optional "export " prefix, and
// values that are
//   - unquoted: up to the end of the line, with a trailing " # comment" and spaces removed;
//   - single-quoted: up to the first unescaped ', literally (\' is a quote);
//   - double-quoted: up to the first unescaped ", with \n \t \r \\ \" \$ unescaped.
//
// Quoted values may span lines. Text after the closing quote is ignored. Values are not
// interpolated. Statements without "=" or with an empty key are skipped.
//
// An unterminated quoted value is a *ParseError naming only its line, never the value; the
// entries before it are returned with it.
func ParseDotEnv(src []byte) (DotEnv, error) {
	var d DotEnv
	index := map[string]int{}
	text := strings.TrimPrefix(string(src), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	line := 1
	for text != "" {
		var raw string
		raw, text, _ = strings.Cut(text, "\n")
		start := line
		line++
		stmt := strings.TrimSpace(raw)
		if stmt == "" || strings.HasPrefix(stmt, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(stmt, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			stmt = strings.TrimSpace(rest)
		}
		key, value, ok := strings.Cut(stmt, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		value = strings.TrimLeft(value, " \t")

		var v string
		if value != "" && (value[0] == '\'' || value[0] == '"') {
			q := value[0]
			body := value[1:]
			for {
				end := closingQuote(body, q)
				if end >= 0 {
					v = unquoteValue(body[:end], q)
					break
				}
				if text == "" {
					return d, &ParseError{Line: start, Message: "unterminated quoted value"}
				}
				var more string
				more, text, _ = strings.Cut(text, "\n")
				line++
				body += "\n" + more
			}
		} else {
			v, _, _ = strings.Cut(value, " #")
			v = strings.TrimRight(v, " \t")
		}

		e := DotEnvEntry{Key: key, Value: NewSecret(v), Line: start}
		if i, seen := index[key]; seen {
			d.Entries[i] = e
			continue
		}
		index[key] = len(d.Entries)
		d.Entries = append(d.Entries, e)
	}
	return d, nil
}

// closingQuote returns the index of the first q in s not preceded by an escaping backslash.
func closingQuote(s string, q byte) int {
	escaped := false
	for i := 0; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\':
			escaped = true
		case s[i] == q:
			return i
		}
	}
	return -1
}

// unquoteValue resolves escapes inside a quoted value.
func unquoteValue(s string, q byte) string {
	if q == '\'' {
		return strings.ReplaceAll(s, `\'`, `'`)
	}
	return strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\r`, "\r", `\"`, `"`, `\$`, "$", `\\`, `\`).Replace(s)
}
