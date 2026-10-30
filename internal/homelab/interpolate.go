package homelab

import "strings"

// This file implements the Compose interpolation grammar: $VAR, ${VAR}, ${VAR:-d}, ${VAR-d},
// ${VAR:+a}, ${VAR+a}, ${VAR:?e}, ${VAR?e}, nesting inside the operand, and $$ as a literal $.
// It mirrors compose-go/v2/template and is differentially tested against it; see D29 in
// docs/decisions.md for why that package is not called at runtime.

// VarRef is one reference found in a template string.
type VarRef struct {
	Name string
	// Op is the operator: "", ":-", "-", ":+", "+", ":?" or "?".
	Op string
	// HasDefault is set for :- and -; Default holds the operand, unresolved.
	HasDefault bool
	Default    string
	// Required is set for :? and ?.
	Required bool
	// Optional is set for :+ and +, and for a reference nested in an alternative or error text.
	Optional bool
	// Fallback lists the enclosing references whose default holds this one, outermost first.
	Fallback []Fallback
	// Offset is the byte offset of the reference's "$" in the scanned string.
	Offset int
}

// Fallback is an enclosing reference whose default text holds a nested reference. The nested
// reference is evaluated only when the enclosing one is unset — or, for :- (Colon), unset or
// empty.
type Fallback struct {
	Name  string
	Colon bool
}

// EscapedName is a name read through $$NAME or $${NAME}: Compose leaves $NAME in place.
type EscapedName struct {
	Name string
	// Offset is the byte offset of the first "$" of "$$".
	Offset int
}

// TemplateScan is what ScanTemplate finds in one string.
type TemplateScan struct {
	Refs    []VarRef
	Escaped []EscapedName
	// Invalid holds the byte offsets of "${" sequences Compose rejects as an invalid template:
	// ${}, ${FOO BAR}, an unterminated ${FOO.
	Invalid []int
}

// tokenKind classifies a piece of a template string.
type tokenKind int

const (
	tokenLiteral tokenKind = iota
	tokenEscaped           // $$: a literal $, possibly followed by a container-scope name
	tokenRef
	tokenInvalid // a "${" Compose rejects; kept as literal text
)

type token struct {
	kind tokenKind
	off  int    // byte offset of the token in the string
	text string // tokenLiteral, tokenInvalid: the text; tokenEscaped: the name after $$, if any
	// (the name is also left in the following literal)
	name   string // tokenRef
	op     string // tokenRef: "", ":-", "-", ":+", "+", ":?", "?"
	arg    string // tokenRef: the operand after op, unparsed
	argOff int    // tokenRef: byte offset of arg in the string
}

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

func scanName(s string) string {
	if s == "" || !isNameStart(s[0]) {
		return ""
	}
	i := 1
	for i < len(s) && isNameChar(s[i]) {
		i++
	}
	return s[:i]
}

// closingBrace returns the index of the brace that closes the "${" at the start of s, or -1.
// It counts braces the way compose-go's getFirstBraceClosingIndex does.
func closingBrace(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		case '{':
			depth++
			i++
		}
	}
	return -1
}

// splitOp splits the text after a braced name into its operator and operand. ok is false when
// the text is neither empty nor an operator, which Compose rejects as an invalid template.
func splitOp(rest string) (op, arg string, ok bool) {
	if rest == "" {
		return "", "", true
	}
	for _, o := range []string{":-", ":+", ":?", "-", "+", "?"} {
		if strings.HasPrefix(rest, o) {
			return o, rest[len(o):], true
		}
	}
	return "", "", false
}

// bracedRef is a parsed "${...}": the operand's offset is relative to the "$", and length
// covers the whole reference.
type bracedRef struct {
	name, op, arg  string
	argOff, length int
}

// braced parses the "${...}" starting at s[0]; ok is false for a template Compose rejects.
func braced(s string) (ref bracedRef, ok bool) {
	end := closingBrace(s[1:])
	if end >= 0 {
		end++ // index of the closing brace in s
	} else if last := strings.LastIndexByte(s, '}'); last > 1 {
		// Compose's pattern is greedy up to the last "}" when the braces do not balance.
		end = last
	} else {
		return bracedRef{}, false
	}
	body := s[2:end]
	name := scanName(body)
	if name == "" {
		return bracedRef{}, false
	}
	op, arg, ok := splitOp(body[len(name):])
	if !ok {
		return bracedRef{}, false
	}
	return bracedRef{name: name, op: op, arg: arg, argOff: 2 + len(name) + len(op), length: end + 1}, true
}

// tokenize splits s into literals, escaped dollars, references and invalid templates.
func tokenize(s string) []token {
	var toks []token
	lit := 0
	flush := func(end int) {
		if end > lit {
			toks = append(toks, token{kind: tokenLiteral, off: lit, text: s[lit:end]})
		}
	}
	i := 0
	for i < len(s) {
		if s[i] != '$' || i+1 >= len(s) {
			i++
			continue
		}
		next := s[i+1]
		switch {
		case next == '$':
			flush(i)
			after := s[i+2:]
			name := scanName(after)
			if name == "" && strings.HasPrefix(after, "{") {
				if n := scanName(after[1:]); n != "" && strings.HasPrefix(after[1+len(n):], "}") {
					name = n
				}
			}
			toks = append(toks, token{kind: tokenEscaped, off: i, text: name})
			i += 2
			lit = i
		case isNameStart(next):
			flush(i)
			name := scanName(s[i+1:])
			toks = append(toks, token{kind: tokenRef, off: i, name: name})
			i += 1 + len(name)
			lit = i
		case next == '{':
			br, ok := braced(s[i:])
			if !ok {
				flush(i)
				toks = append(toks, token{kind: tokenInvalid, off: i, text: "${"})
				i += 2
				lit = i
				continue
			}
			flush(i)
			toks = append(toks, token{kind: tokenRef, off: i, name: br.name, op: br.op, arg: br.arg, argOff: i + br.argOff})
			i += br.length
			lit = i
		default:
			i++
		}
	}
	flush(len(s))
	return toks
}

// ScanTemplate returns the interpolation references in s, in order, the names read through the
// escaped form $$NAME or $${NAME}, and the positions of invalid templates.
func ScanTemplate(s string) TemplateScan {
	var ts TemplateScan
	scanInto(s, 0, nil, false, &ts)
	return ts
}

func scanInto(s string, base int, fallback []Fallback, optional bool, ts *TemplateScan) {
	for _, t := range tokenize(s) {
		switch t.kind {
		case tokenLiteral:
		case tokenInvalid:
			ts.Invalid = append(ts.Invalid, base+t.off)
		case tokenEscaped:
			if t.text != "" {
				ts.Escaped = append(ts.Escaped, EscapedName{Name: t.text, Offset: base + t.off})
			}
		case tokenRef:
			r := VarRef{Name: t.name, Op: t.op, Optional: optional, Fallback: fallback, Offset: base + t.off}
			switch t.op {
			case ":-", "-":
				r.HasDefault = true
				r.Default = t.arg
				inner := append(append([]Fallback(nil), fallback...), Fallback{Name: t.name, Colon: t.op == ":-"})
				scanInto(t.arg, base+t.argOff, inner, optional, ts)
			case ":+", "+":
				r.Optional = true
				scanInto(t.arg, base+t.argOff, fallback, true, ts)
			case ":?", "?":
				r.Required = true
				scanInto(t.arg, base+t.argOff, fallback, true, ts)
			}
			ts.Refs = append(ts.Refs, r)
		}
	}
}

// Interpolate substitutes s against lookup with Compose semantics. missing lists the names
// whose absence changed the result (a plain or required reference that was unset, or empty for
// :?); when it is non-empty the returned string is what Compose would produce with blanks, and
// callers should treat it as unresolved. An invalid template is kept as literal text.
func Interpolate(s string, lookup func(string) (string, bool)) (out string, missing []string) {
	var b strings.Builder
	for _, t := range tokenize(s) {
		switch t.kind {
		case tokenLiteral, tokenInvalid:
			b.WriteString(t.text)
		case tokenEscaped:
			b.WriteString("$") // the name after $$, if any, follows as literal text
		case tokenRef:
			v, ok := lookup(t.name)
			switch t.op {
			case "":
				if !ok {
					missing = append(missing, t.name)
				}
				b.WriteString(v)
			case ":-", "-":
				if !ok || (t.op == ":-" && v == "") {
					d, m := Interpolate(t.arg, lookup)
					missing = append(missing, m...)
					v = d
				}
				b.WriteString(v)
			case ":+", "+":
				if ok && (t.op == "+" || v != "") {
					a, m := Interpolate(t.arg, lookup)
					missing = append(missing, m...)
					v = a
				}
				b.WriteString(v)
			case ":?", "?":
				if !ok || (t.op == ":?" && v == "") {
					missing = append(missing, t.name)
				}
				b.WriteString(v)
			}
		}
	}
	return b.String(), missing
}
