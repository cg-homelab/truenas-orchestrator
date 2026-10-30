package homelab

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// UpdateDotEnv returns src with each key in set assigned its value and every statement of each
// key in unset removed. Every other byte is preserved: comments, blank lines, key order, the
// quoting of untouched entries, an "export " prefix and a trailing " # comment" on a rewritten
// line, and line endings.
//
// A key already present has its last statement — the effective one, as ParseDotEnv reads it —
// rewritten in place; earlier duplicates are left alone. An absent key is appended at the end,
// in sorted order, with the file's line ending (that of its first line, or "\n"). A key in both
// set and unset is set. Values are written bare when that round-trips through ParseDotEnv and
// double-quoted otherwise.
//
// src that ParseDotEnv rejects (an unterminated quoted value) is returned as its *ParseError,
// because a rewrite could swallow the lines after it. A key that is not a variable name is an
// error wrapping ErrValidation. Errors never contain a value.
func UpdateDotEnv(src []byte, set map[string]Secret, unset []string) ([]byte, error) {
	for k := range set {
		if !envKeyRe.MatchString(k) {
			return nil, fmt.Errorf("env key %q is not a variable name: %w", k, ErrValidation)
		}
	}
	stmts, err := scanDotEnv(src)
	if err != nil {
		return nil, err
	}
	last := map[string]int{}
	for i, st := range stmts {
		last[st.key] = i
	}
	drop := map[string]bool{}
	for _, k := range unset {
		if _, ok := set[k]; !ok {
			drop[k] = true
		}
	}

	var out bytes.Buffer
	pos := 0
	for i, st := range stmts {
		v, isSet := set[st.key]
		switch {
		case drop[st.key]:
			out.Write(src[pos:st.lineStart])
			pos = st.next
		case isSet && last[st.key] == i:
			// A non-blank remainder (an inline comment, or text after a closing quote) forces
			// quotes, so the new value cannot run into it.
			rest := strings.TrimSpace(string(src[st.valEnd:st.end])) != ""
			out.Write(src[pos:st.valStart])
			out.WriteString(encodeEnvValue(v.Reveal(), rest))
			pos = st.valEnd
		}
	}
	out.Write(src[pos:])

	var add []string
	for k := range set {
		if _, ok := last[k]; !ok {
			add = append(add, k)
		}
	}
	if len(add) == 0 {
		return out.Bytes(), nil
	}
	slices.Sort(add)
	eol := lineEnding(src)
	if out.Len() > 0 && !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		out.WriteString(eol)
	}
	for _, k := range add {
		out.WriteString(k + "=" + encodeEnvValue(set[k].Reveal(), false) + eol)
	}
	b := out.Bytes()
	if len(src) > 0 && !bytes.HasSuffix(src, []byte("\n")) {
		b = b[:len(b)-len(eol)] // the file had no trailing newline; keep it that way
	}
	return b, nil
}

// lineEnding returns the line ending of src's first line, or "\n".
func lineEnding(src []byte) string {
	if i := bytes.IndexByte(src, '\n'); i > 0 && src[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// encodeEnvValue renders v for the right-hand side of KEY=. It is bare unless force is set or v
// holds whitespace, #, $, a quote or a backslash; then it is double-quoted with the escapes
// unquoteValue resolves.
func encodeEnvValue(v string, force bool) string {
	if !force && !strings.ContainsFunc(v, unicode.IsSpace) && !strings.ContainsAny(v, "#$'\"\\") {
		return v
	}
	return `"` + envEscaper.Replace(v) + `"`
}

var envEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

// envStmt locates one KEY=VALUE statement in the source bytes.
type envStmt struct {
	key string
	// lineStart is the offset of the statement's first line; end is the end of its last line,
	// before the line ending; next is the offset just after that line ending.
	lineStart, end, next int
	// valStart and valEnd delimit the value as written, quotes included. An unquoted value
	// excludes a trailing " # comment" and trailing blanks.
	valStart, valEnd int
}

// srcLine is one line of the source: its content excludes the "\n" or "\r\n" that ends it.
type srcLine struct {
	start, end, next int
}

// splitLines splits src as ParseDotEnv does after it replaces "\r\n" with "\n".
func splitLines(src []byte) []srcLine {
	var lines []srcLine
	pos := 0
	if bytes.HasPrefix(src, []byte("\ufeff")) {
		pos = len("\ufeff")
	}
	for pos < len(src) {
		i := bytes.IndexByte(src[pos:], '\n')
		if i < 0 {
			lines = append(lines, srcLine{start: pos, end: len(src), next: len(src)})
			break
		}
		end := pos + i
		if end > pos && src[end-1] == '\r' {
			end--
		}
		lines = append(lines, srcLine{start: pos, end: end, next: pos + i + 1})
		pos += i + 1
	}
	return lines
}

// scanDotEnv finds every statement ParseDotEnv would read, with byte offsets. It mirrors
// ParseDotEnv statement for statement; a differential test keeps the two in step.
func scanDotEnv(src []byte) ([]envStmt, error) {
	lines := splitLines(src)
	var out []envStmt
	for li := 0; li < len(lines); li++ {
		ln := lines[li]
		raw := string(src[ln.start:ln.end])
		trimmed := strings.TrimLeftFunc(raw, unicode.IsSpace)
		off := ln.start + len(raw) - len(trimmed)
		stmt := strings.TrimRightFunc(trimmed, unicode.IsSpace)
		if stmt == "" || strings.HasPrefix(stmt, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(stmt, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			r := strings.TrimLeftFunc(rest, unicode.IsSpace)
			off += len("export") + len(rest) - len(r)
			stmt = strings.TrimRightFunc(r, unicode.IsSpace)
		}
		eq := strings.IndexByte(stmt, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(stmt[:eq])
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		value := stmt[eq+1:]
		v := strings.TrimLeft(value, " \t")
		st := envStmt{key: key, lineStart: ln.start, valStart: off + eq + 1 + len(value) - len(v)}
		startLine := li
		if v != "" && (v[0] == '\'' || v[0] == '"') {
			// body maps back to src in segments: the first line's text after the opening quote,
			// then each continuation line, joined by "\n" as ParseDotEnv joins them.
			body := v[1:]
			segStart, segSrc := 0, st.valStart+1
			for {
				if e := closingQuote(body, v[0]); e >= 0 {
					st.valEnd = segSrc + (e - segStart) + 1
					break
				}
				if li+1 >= len(lines) {
					return nil, &ParseError{Line: startLine + 1, Message: "unterminated quoted value"}
				}
				li++
				more := lines[li]
				segStart, segSrc = len(body)+1, more.start
				body += "\n" + string(src[more.start:more.end])
			}
		} else {
			val, _, _ := strings.Cut(v, " #")
			st.valEnd = st.valStart + len(strings.TrimRight(val, " \t"))
		}
		st.end, st.next = lines[li].end, lines[li].next
		out = append(out, st)
	}
	return out, nil
}
