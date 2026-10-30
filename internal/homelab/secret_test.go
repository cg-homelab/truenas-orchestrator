package homelab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

const plaintext = "hunter2-plaintext"

// holder has the Secret in an unexported field, where fmt cannot call its methods.
type holder struct {
	name   string
	secret Secret
}

// exported has the Secret in an exported field.
type exported struct {
	Name   string
	Secret Secret
}

func TestSecretNeverPrintsPlaintext(t *testing.T) {
	s := NewSecret(plaintext)
	h := holder{name: "db", secret: s}
	e := exported{Name: "db", Secret: s}

	tests := []struct {
		name string
		got  func(t *testing.T) string
	}{
		{"%v", func(*testing.T) string { return fmt.Sprintf("%v", s) }},
		{"%+v", func(*testing.T) string { return fmt.Sprintf("%+v", s) }},
		{"%#v", func(*testing.T) string { return fmt.Sprintf("%#v", s) }},
		{"%s", func(*testing.T) string { return fmt.Sprintf("%s", s) }},
		{"%d", func(*testing.T) string { return fmt.Sprintf("%d", s) }},
		{"%q", func(*testing.T) string { return fmt.Sprintf("%q", s) }},
		{"%x", func(*testing.T) string { return fmt.Sprintf("%x", s) }},
		{"pointer %v", func(*testing.T) string { return fmt.Sprintf("%v", &s) }},
		{"fmt.Sprint", func(*testing.T) string { return fmt.Sprint(s) }},
		{"fmt.Sprintln", func(*testing.T) string { return fmt.Sprintln(s) }},
		{"unexported field %v", func(*testing.T) string { return fmt.Sprintf("%v", h) }},
		{"unexported field %+v", func(*testing.T) string { return fmt.Sprintf("%+v", h) }},
		{"unexported field %#v", func(*testing.T) string { return fmt.Sprintf("%#v", h) }},
		{"exported field %+v", func(*testing.T) string { return fmt.Sprintf("%+v", e) }},
		{"exported field %#v", func(*testing.T) string { return fmt.Sprintf("%#v", e) }},
		{"json.Marshal", func(t *testing.T) string { return marshal(t, s) }},
		{"json.Marshal struct", func(t *testing.T) string { return marshal(t, e) }},
		{"slog text", func(*testing.T) string { return logWith(slog.NewTextHandler, s, h, e) }},
		{"slog json", func(*testing.T) string { return logWith(slog.NewJSONHandler, s, h, e) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.got(t)
			if strings.Contains(got, plaintext) {
				t.Errorf("output contains the plaintext: %q", got)
			}
		})
	}
}

func TestSecretRedactionMarker(t *testing.T) {
	s := NewSecret(plaintext)
	if got := fmt.Sprint(s); got != redacted {
		t.Errorf("fmt.Sprint(secret) = %q, want %q", got, redacted)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal(secret): %v", err)
	}
	if got, want := string(b), `"`+redacted+`"`; got != want {
		t.Errorf("json.Marshal(secret) = %s, want %s", got, want)
	}
}

func TestSecretReveal(t *testing.T) {
	if got := NewSecret(plaintext).Reveal(); got != plaintext {
		t.Errorf("NewSecret(%q).Reveal() = %q", plaintext, got)
	}
	if got := (Secret{}).Reveal(); got != "" {
		t.Errorf("Secret{}.Reveal() = %q, want empty", got)
	}
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

func logWith[H slog.Handler](
	newHandler func(w io.Writer, opts *slog.HandlerOptions) H, s Secret, h holder, e exported,
) string {
	var buf bytes.Buffer
	logger := slog.New(newHandler(&buf, nil))
	logger.Info("secret check", "value", s, "holder", h, "exported", e, "group", slog.GroupValue(slog.Any("inner", s)))
	return buf.String()
}
