package truenas

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// JSON-RPC 2.0 and TrueNAS error codes (api_client jsonrpc.py, ws_handler/rpc.py).
const (
	codeParseError        = -32700
	codeInvalidRequest    = -32600
	codeMethodNotFound    = -32601
	codeInvalidParams     = -32602
	codeInternalError     = -32603
	codeTooManyCalls      = -32000
	codeMethodCallError   = -32001
	errnoPerm             = 1
	errnoNoEnt            = 2
	errnoAccess           = 13
	errnoExist            = 17
	errnoXDev             = 18
	errnoInval            = 22
	errnoNotAuthenticated = 207 // ENOTAUTHENTICATED, middleware-specific (api_client exc.py)
)

// Error is a JSON-RPC error reply from the middleware, or a failed job. Unwrap maps it to the
// homelab sentinels, so callers branch with errors.Is and read fields with errors.As.
//
// The middleware's trace and py_exception members are never decoded: they hold stack frames with
// local variables.
type Error struct {
	// Method is the method that failed.
	Method string
	// Code is the JSON-RPC error code; 0 for a failed job.
	Code int
	// Errno is the TrueNAS errno (data.error), 0 when absent.
	Errno int
	// Errname is the errno's name, e.g. "EINVAL".
	Errname string
	// Reason is the middleware's message (data.reason, or the JSON-RPC message without data).
	Reason string
	// Fields lists per-attribute validation errors (data.extra).
	Fields []FieldError
}

// FieldError is one entry of a validation error: the attribute, its message and errno.
type FieldError struct {
	Field   string
	Message string
	Errno   int
}

// Error formats e as "truenas: <method>: <ERRNAME>: <reason>", with the field errors in
// parentheses instead of the reason when there are any (the reason then repeats them).
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("truenas: ")
	b.WriteString(e.Method)
	b.WriteString(": ")
	b.WriteString(e.name())
	if len(e.Fields) == 0 && e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Code == codeMethodNotFound {
		b.WriteString(": middleware API drift; run just smoke")
	}
	if len(e.Fields) > 0 {
		b.WriteString(" (")
		for i, f := range e.Fields {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(f.Field)
			b.WriteString(": ")
			b.WriteString(f.Message)
		}
		b.WriteString(")")
	}
	return b.String()
}

func (e *Error) name() string {
	if e.Errname != "" {
		return e.Errname
	}
	if n := errnoName(e.Errno); n != "" {
		return n
	}
	switch e.Code {
	case codeParseError:
		return "PARSE_ERROR"
	case codeInvalidRequest:
		return "INVALID_REQUEST"
	case codeMethodNotFound:
		return "METHOD_NOT_FOUND"
	case codeInvalidParams:
		return "INVALID_PARAMS"
	case codeInternalError:
		return "INTERNAL_ERROR"
	case codeTooManyCalls:
		return "TOO_MANY_CONCURRENT_CALLS"
	case codeMethodCallError:
		return "CALL_ERROR"
	}
	return "ERROR"
}

// Unwrap returns the homelab sentinel for e, or nil when none applies. A validation error whose
// every field is EEXIST ("already exists", "already used") is ErrConflict: the object is there.
func (e *Error) Unwrap() error {
	switch e.Code {
	case codeMethodNotFound, codeTooManyCalls:
		return homelab.ErrUnavailable
	case codeInvalidParams:
		if e.allFieldsExist() {
			return homelab.ErrConflict
		}
		return homelab.ErrValidation
	}
	switch e.Errno {
	case errnoInval:
		return homelab.ErrValidation
	case errnoNoEnt:
		return homelab.ErrNotFound
	case errnoExist:
		return homelab.ErrConflict
	case errnoAccess, errnoPerm:
		return homelab.ErrForbidden
	case errnoNotAuthenticated:
		return homelab.ErrUnauthenticated
	}
	return nil
}

func (e *Error) allFieldsExist() bool {
	if len(e.Fields) == 0 {
		return false
	}
	for _, f := range e.Fields {
		if f.Errno != errnoExist {
			return false
		}
	}
	return true
}

// errnameRE is the shape of a real errno name. A server-supplied errname outside it is replaced
// by the name of its errno, so a reflected value can never reach a log line through it.
var errnameRE = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

func errnoName(n int) string {
	switch n {
	case errnoPerm:
		return "EPERM"
	case errnoNoEnt:
		return "ENOENT"
	case errnoAccess:
		return "EACCES"
	case errnoExist:
		return "EEXIST"
	case errnoXDev:
		return "EXDEV"
	case errnoInval:
		return "EINVAL"
	case errnoNotAuthenticated:
		return "ENOTAUTHENTICATED"
	}
	return ""
}

// wireError is the JSON-RPC error member. Data stays raw: only TrueNAS errors carry an object.
type wireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// wireErrorData is the TrueNAS error data object, minus trace and py_exception, which are never
// decoded.
type wireErrorData struct {
	Error   int             `json:"error"`
	Errname string          `json:"errname"`
	Reason  string          `json:"reason"`
	Extra   json.RawMessage `json:"extra"`
}

// newError converts a wire error into an *Error for method.
func newError(method string, w *wireError) *Error {
	e := &Error{Method: method, Code: w.Code, Reason: cleanReason(w.Message)}
	var d wireErrorData
	if len(w.Data) > 0 && json.Unmarshal(w.Data, &d) == nil {
		e.Errno = d.Error
		e.Errname = d.Errname
		if !errnameRE.MatchString(e.Errname) {
			e.Errname = errnoName(e.Errno)
		}
		if d.Reason != "" {
			e.Reason = cleanReason(d.Reason)
		}
		e.Fields = decodeFields(d.Extra)
	}
	return e
}

// decodeFields reads ValidationErrors' extra, a list of [attribute, errmsg, errno] triples. A
// CallError's extra has no fixed shape, so anything else is ignored.
func decodeFields(raw json.RawMessage) []FieldError {
	if len(raw) == 0 {
		return nil
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	var out []FieldError
	for _, entry := range entries {
		var triple []json.RawMessage
		if json.Unmarshal(entry, &triple) != nil || len(triple) < 2 {
			continue
		}
		var f FieldError
		if json.Unmarshal(triple[0], &f.Field) != nil || json.Unmarshal(triple[1], &f.Message) != nil {
			continue
		}
		f.Message = cleanReason(f.Message)
		if len(triple) > 2 {
			_ = json.Unmarshal(triple[2], &f.Errno) // errno is optional; zero when not a number
		}
		out = append(out, f)
	}
	return out
}

// cleanReason trims a middleware message to one line.
func cleanReason(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "; ")
}

// scrubError removes every occurrence of secret from e's text fields. Login failures carry no
// reason, so this is defence in depth for the one call whose params hold the API key.
func scrubError(e *Error, secret string) {
	if secret == "" {
		return
	}
	e.Reason = strings.ReplaceAll(e.Reason, secret, "[REDACTED]")
	e.Errname = strings.ReplaceAll(e.Errname, secret, "[REDACTED]")
	for i := range e.Fields {
		e.Fields[i].Message = strings.ReplaceAll(e.Fields[i].Message, secret, "[REDACTED]")
		e.Fields[i].Field = strings.ReplaceAll(e.Fields[i].Field, secret, "[REDACTED]")
	}
}
