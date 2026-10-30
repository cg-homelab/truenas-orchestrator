// Package compose parses compose.yaml bytes into the homelab model and renders manifests and
// overlays as bytes. It is I/O-free: the caller reads and writes files.
package compose

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/schema"
	"go.yaml.in/yaml/v3"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// Parser parses compose files. The zero value is ready to use.
type Parser struct{}

var _ homelab.ComposeParser = (*Parser)(nil)

// yamlLineRe extracts the line from a go.yaml.in/yaml/v3 error message.
var yamlLineRe = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// Parse parses the compose file at path (relative to the repo root) from src.
//
// A YAML syntax error is returned as a *homelab.ParseError. Compose-spec violations, decided by
// compose-go/v2/schema.Validate on the raw document (never the compose-go loader), are returned
// in ComposeFile.SchemaErrors with the line of the offending node. The structural model is
// extracted leniently, so a schema-invalid file still yields its services, networks and refs.
func (p *Parser) Parse(path string, src []byte) (homelab.ComposeFile, error) {
	cf := homelab.ComposeFile{Path: path}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return cf, parseError(err)
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = resolve(doc.Content[0])
	}
	if root != nil && root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		root = nil
	}
	if root != nil && root.Kind != yaml.MappingNode {
		cf.SchemaErrors = append(cf.SchemaErrors, homelab.SchemaError{
			Message: "the top level must be a mapping", Line: root.Line,
		})
		return cf, nil
	}

	raw := map[string]any{}
	if root != nil {
		if err := root.Decode(&raw); err != nil {
			return cf, parseError(err)
		}
	}
	if err := schema.Validate(raw); err != nil {
		msg := err.Error()
		// compose-go reports a key the spec does not allow as "<path> false schema".
		if loc, ok := strings.CutSuffix(msg, " false schema"); ok {
			msg = loc + " is not allowed by the Compose spec"
		}
		cf.SchemaErrors = append(cf.SchemaErrors, homelab.SchemaError{
			Message: msg, Line: schemaErrorLine(root, err.Error()),
		})
	}
	if root == nil {
		return cf, nil
	}

	w := walker{cf: &cf, lines: strings.Split(string(src), "\n")}
	w.refs(root, "", "")
	for _, kv := range pairs(root) {
		switch kv.key.Value {
		case "services":
			for _, s := range pairs(resolve(kv.val)) {
				cf.Services = append(cf.Services, w.service(s.key, resolve(s.val)))
			}
		case "networks":
			for _, n := range pairs(resolve(kv.val)) {
				cf.Networks = append(cf.Networks, networkDecl(n.key, resolve(n.val)))
			}
		case "secrets":
			for _, s := range pairs(resolve(kv.val)) {
				if f := get(resolve(s.val), "file"); f != nil && f.Kind == yaml.ScalarNode {
					cf.SecretFiles = append(cf.SecretFiles, homelab.Scalar{Value: f.Value, Line: f.Line})
				}
			}
		}
	}
	return cf, nil
}

// parseError converts a YAML error into a *homelab.ParseError with its line.
func parseError(err error) *homelab.ParseError {
	msg := err.Error()
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		msg = te.Errors[0]
	}
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		return &homelab.ParseError{Line: line, Message: m[2]}
	}
	return &homelab.ParseError{Message: strings.TrimPrefix(msg, "yaml: ")}
}

// schemaErrorLine maps a compose-go schema message, which starts with the dotted instance path
// ("services.db.networks must be a array or mapping"), to the line of that node. Keys may
// themselves contain dots (traefik labels, service names), so at each mapping the longest run
// of path segments that names a key wins. It returns the deepest node it can reach, or 0.
func schemaErrorLine(root *yaml.Node, msg string) int {
	if root == nil {
		return 0
	}
	loc, _, _ := strings.Cut(msg, " ")
	parts := strings.Split(loc, ".")
	line := 0
	n := root
	for i := 0; i < len(parts) && parts[i] != ""; {
		var next, key *yaml.Node
		consumed := 1
		if n.Kind == yaml.MappingNode {
			kvs := pairs(n)
		search:
			for j := len(parts); j > i; j-- {
				want := strings.Join(parts[i:j], ".")
				for _, kv := range kvs {
					if kv.key.Value == want {
						key, next, consumed = kv.key, resolve(kv.val), j-i
						break search
					}
				}
			}
		} else if idx, err := strconv.Atoi(parts[i]); err == nil && n.Kind == yaml.SequenceNode && idx >= 0 && idx < len(n.Content) {
			next = resolve(n.Content[idx])
		}
		if next == nil {
			break
		}
		line = next.Line
		if key != nil && next.Kind != yaml.ScalarNode {
			line = key.Line
		}
		n = next
		i += consumed
	}
	return line
}

// pair is one key/value of a mapping.
type pair struct{ key, val *yaml.Node }

// resolve follows aliases.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// pairs returns the entries of a mapping, with merge keys (<<) expanded and explicit keys
// taking precedence over merged ones. It returns nil for anything that is not a mapping.
func pairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out, merged []pair
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value == "<<" && k.Tag == "!!merge" {
			src := resolve(v)
			if src.Kind == yaml.SequenceNode {
				for _, item := range src.Content {
					merged = append(merged, pairs(resolve(item))...)
				}
			} else {
				merged = append(merged, pairs(src)...)
			}
			continue
		}
		seen[k.Value] = true
		out = append(out, pair{k, v})
	}
	for _, m := range merged {
		if !seen[m.key.Value] {
			seen[m.key.Value] = true
			out = append(out, m)
		}
	}
	return out
}

// get returns the value for key in a mapping, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	for _, kv := range pairs(n) {
		if kv.key.Value == key {
			return resolve(kv.val)
		}
	}
	return nil
}

// scalars returns a scalar as one item, or the scalar items of a sequence.
func scalars(n *yaml.Node) []homelab.Scalar {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" {
			return nil
		}
		return []homelab.Scalar{{Value: n.Value, Line: n.Line}}
	}
	if n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []homelab.Scalar
	for _, item := range n.Content {
		if item = resolve(item); item.Kind == yaml.ScalarNode {
			out = append(out, homelab.Scalar{Value: item.Value, Line: item.Line})
		}
	}
	return out
}

func networkDecl(key, val *yaml.Node) homelab.NetworkDecl {
	d := homelab.NetworkDecl{Key: key.Value, Name: key.Value, Line: key.Line}
	if name := get(val, "name"); name != nil && name.Kind == yaml.ScalarNode && name.Value != "" {
		d.Name = name.Value
	}
	if ext := get(val, "external"); ext != nil {
		// A mapping is the legacy external: { name: ... } form.
		d.External = ext.Kind == yaml.MappingNode || yamlBool(ext, false)
	}
	return d
}

// yamlBool decodes a YAML boolean scalar (true, True, TRUE, false, ...), or returns def when n
// is absent or not a boolean.
func yamlBool(n *yaml.Node, def bool) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return def
	}
	var b bool
	if err := n.Decode(&b); err != nil {
		return def
	}
	return b
}
