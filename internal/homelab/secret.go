package homelab

import (
	"fmt"
	"log/slog"
)

// redacted is what every printing or encoding path of a Secret produces.
const redacted = "[REDACTED]"

// Secret holds a sensitive value. Every way of printing or encoding it redacts.
//
// It is a struct holding a pointer rather than a string type: a string type converts with
// string(s) and leaks through every API that takes a string, and the pointer means that fmt
// and slog, which cannot call methods on an unexported field of another struct, print an
// address instead of the value.
type Secret struct{ value *string }

// NewSecret wraps v as a Secret.
func NewSecret(v string) Secret { return Secret{value: &v} }

// Reveal returns the plaintext. It is the only way out; grep for it in review.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// Format implements fmt.Formatter so that every verb prints the redaction marker.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, redacted) }

// LogValue implements slog.LogValuer so that slog handlers print the redaction marker.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler so that JSON encoding emits the redaction marker.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
