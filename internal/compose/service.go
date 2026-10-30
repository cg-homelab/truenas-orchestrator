package compose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

// walker accumulates references into a ComposeFile while the document is walked.
type walker struct {
	cf *homelab.ComposeFile
	// lines is the source split into lines, to place references inside multi-line scalars.
	lines []string
}

// lineOf returns the 1-based source line of the k-th (0-based) occurrence of needle in scalar
// n. A scalar's Line is where it starts; in a block (| or >) or a folded flow scalar the
// reference can sit several lines below. Folding never splits a reference, so counting
// occurrences in the source from the scalar's start finds the right line. It falls back to
// n.Line.
func (w *walker) lineOf(n *yaml.Node, needle string, k int) int {
	const maxRows = 500
	seen := 0
	for row := n.Line - 1; row >= 0 && row < len(w.lines) && row < n.Line-1+maxRows; row++ {
		text := w.lines[row]
		if row == n.Line-1 && n.Column-1 <= len(text) {
			text = text[n.Column-1:]
		}
		c := strings.Count(text, needle)
		if seen+c > k {
			return row + 1
		}
		seen += c
	}
	return n.Line
}

// refLine places the token of length size at offset off in n.Value.
func (w *walker) refLine(n *yaml.Node, off, size int) int {
	end := min(off+size, len(n.Value))
	needle := n.Value[off:end]
	return w.lineOf(n, needle, strings.Count(n.Value[:off], needle))
}

// refs walks every scalar value under n (keys are never interpolated) and records its
// references and invalid templates. Aliases are not followed, so a reference is recorded once,
// at the line where it is written.
func (w *walker) refs(n *yaml.Node, path, service string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := k.Value
			if path != "" {
				child = path + "." + k.Value
			}
			svc := service
			if path == "services" {
				svc = k.Value
			}
			w.refs(v, child, svc)
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			w.refs(item, path+"["+strconv.Itoa(i)+"]", service)
		}
	case yaml.ScalarNode:
		if n.Tag != "!!str" || !strings.Contains(n.Value, "$") {
			return
		}
		ts := homelab.ScanTemplate(n.Value)
		for _, v := range ts.Refs {
			size := 1 + len(v.Name)
			if strings.HasPrefix(n.Value[v.Offset:], "${") {
				size++
			}
			w.cf.Refs = append(w.cf.Refs, homelab.Ref{
				Name: v.Name, Line: w.refLine(n, v.Offset, size), Path: path, Service: service,
				Op: v.Op, HasDefault: v.HasDefault, Default: v.Default, Required: v.Required,
				Optional: v.Optional, Fallback: v.Fallback,
			})
		}
		for _, off := range ts.Invalid {
			w.cf.SchemaErrors = append(w.cf.SchemaErrors, homelab.SchemaError{
				Message: fmt.Sprintf("%s has an invalid interpolation template (Compose rejects ${} with no closing brace, no name, or a name followed by something other than an operator)", path),
				Line:    w.refLine(n, off, 2),
			})
		}
	case yaml.DocumentNode, yaml.AliasNode:
	}
}

// shellNames are the interpreters whose "-c" argument is a shell script.
func isShell(s string) bool {
	switch s {
	case "sh", "bash", "ash", "/bin/sh", "/bin/bash", "/bin/ash", "/usr/bin/env sh":
		return true
	}
	return false
}

// isDashC matches -c and combined short flags ending in it, such as -ec or -xc.
var dashCRe = regexp.MustCompile(`^-[a-z]*c$`)

// shellScripts returns the items of a command:, entrypoint: or healthcheck.test node that a
// shell evaluates, with their YAML paths: the whole string form (healthcheck: CMD-SHELL
// implied), the items after -c when the list starts with a shell, and the items after CMD-SHELL.
func shellScripts(n *yaml.Node, path string, healthcheck bool) (scripts []*yaml.Node, paths []string) {
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.ScalarNode {
		if n.Tag == "!!null" {
			return nil, nil
		}
		return []*yaml.Node{n}, []string{path}
	}
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, nil
	}
	items := make([]*yaml.Node, len(n.Content))
	for i, it := range n.Content {
		items[i] = resolve(it)
	}
	start := -1
	switch {
	case healthcheck && items[0].Value == "CMD-SHELL":
		start = 1
	case healthcheck && items[0].Value == "CMD" && len(items) > 1:
		start = scriptStart(items[1:]) + 1
		if start == 0 {
			start = -1
		}
	case !healthcheck:
		start = scriptStart(items)
	}
	if start < 0 {
		return nil, nil
	}
	for i := start; i < len(items); i++ {
		if items[i].Kind == yaml.ScalarNode {
			scripts = append(scripts, items[i])
			paths = append(paths, path+"["+strconv.Itoa(i)+"]")
		}
	}
	return scripts, paths
}

// scriptStart returns the index after "-c" when items start with a shell, or -1.
func scriptStart(items []*yaml.Node) int {
	if !isShell(items[0].Value) {
		return -1
	}
	for i := 1; i < len(items); i++ {
		if dashCRe.MatchString(items[i].Value) {
			return i + 1
		}
	}
	return -1
}

// assignedInScript reports whether script sets name itself (NAME=..., or for NAME in ...), in
// which case $NAME reads the script's own variable, not container scope.
func assignedInScript(script, name string) bool {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(?:^|[\s;&|(])` + q + `=|\bfor\s+` + q + `\s+in\b`)
	return re.MatchString(script)
}

// containerRefs records the $$NAME reads in the shell-evaluated parts of a service's command:,
// entrypoint: or healthcheck.test.
func (w *walker) containerRefs(service string, n *yaml.Node, path string, healthcheck bool) {
	nodes, paths := shellScripts(n, path, healthcheck)
	for i, sn := range nodes {
		for _, e := range homelab.ScanTemplate(sn.Value).Escaped {
			if assignedInScript(sn.Value, e.Name) {
				continue
			}
			size := 2 + len(e.Name)
			if strings.HasPrefix(sn.Value[e.Offset+2:], "{") {
				size++
			}
			w.cf.ContainerRefs = append(w.cf.ContainerRefs, homelab.Ref{
				Name: e.Name, Line: w.refLine(sn, e.Offset, size), Path: paths[i], Service: service,
			})
		}
	}
}

// service extracts the structural model of one service.
func (w *walker) service(key, n *yaml.Node) homelab.Service {
	s := homelab.Service{Name: key.Value, Line: key.Line}
	for _, kv := range pairs(n) {
		v := resolve(kv.val)
		switch kv.key.Value {
		case "container_name":
			s.ContainerName = scalar(v)
		case "image":
			s.Image = scalar(v)
		case "env_file":
			s.EnvFiles = envFiles(v)
		case "environment":
			s.Environment = environment(v)
		case "networks":
			s.Networks = networks(v)
		case "volumes":
			s.Volumes = volumes(v)
		case "command":
			s.Command = scalars(v)
			w.containerRefs(s.Name, v, "services."+s.Name+".command", false)
		case "entrypoint":
			s.Entrypoint = scalars(v)
			w.containerRefs(s.Name, v, "services."+s.Name+".entrypoint", false)
		case "healthcheck":
			s.Healthcheck = scalars(get(v, "test"))
			w.containerRefs(s.Name, get(v, "test"), "services."+s.Name+".healthcheck.test", true)
		case "deploy":
			limits := get(get(v, "resources"), "limits")
			s.HasLimits = limits != nil && limits.Kind == yaml.MappingNode && len(limits.Content) > 0
		}
	}
	return s
}

func scalar(n *yaml.Node) homelab.Scalar {
	if n == nil || n.Kind != yaml.ScalarNode {
		return homelab.Scalar{}
	}
	return homelab.Scalar{Value: n.Value, Line: n.Line}
}

// envFiles reads env_file: as a string, a list of strings, or a list of {path, required}.
func envFiles(n *yaml.Node) []homelab.EnvFileRef {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return []homelab.EnvFileRef{{Path: scalar(n), Required: true}}
	}
	var out []homelab.EnvFileRef
	for _, item := range n.Content {
		item = resolve(item)
		if item.Kind == yaml.ScalarNode {
			out = append(out, homelab.EnvFileRef{Path: scalar(item), Required: true})
			continue
		}
		p := get(item, "path")
		if p == nil {
			continue
		}
		out = append(out, homelab.EnvFileRef{Path: scalar(p), Required: yamlBool(get(item, "required"), true)})
	}
	return out
}

// environment reads environment: in the map form (KEY: value) or the list form (- KEY=value).
func environment(n *yaml.Node) []homelab.EnvEntry {
	if n == nil {
		return nil
	}
	var out []homelab.EnvEntry
	for _, kv := range pairs(n) {
		v := resolve(kv.val)
		e := homelab.EnvEntry{Key: kv.key.Value, Line: kv.key.Line}
		if v != nil && v.Kind == yaml.ScalarNode && v.Tag != "!!null" {
			e.Value, e.HasValue = v.Value, true
		}
		out = append(out, e)
	}
	if n.Kind != yaml.SequenceNode {
		return out
	}
	for _, item := range n.Content {
		item = resolve(item)
		if item.Kind != yaml.ScalarNode {
			continue
		}
		k, v, ok := strings.Cut(item.Value, "=")
		out = append(out, homelab.EnvEntry{Key: k, Value: v, HasValue: ok, Line: item.Line})
	}
	return out
}

// networks reads a service's networks: as a list, a mapping, or (invalid, reported as
// COMPOSE001) a bare string, which is read as one network so the NET checks still run.
func networks(n *yaml.Node) []homelab.Scalar {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.MappingNode {
		var out []homelab.Scalar
		for _, kv := range pairs(n) {
			out = append(out, homelab.Scalar{Value: kv.key.Value, Line: kv.key.Line})
		}
		return out
	}
	return scalars(n)
}

// volumes returns the host sides of bind mounts.
func volumes(n *yaml.Node) []homelab.Scalar {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []homelab.Scalar
	for _, item := range n.Content {
		item = resolve(item)
		if item.Kind == yaml.ScalarNode {
			parts := splitVolume(item.Value)
			if len(parts) < 2 || parts[0] == "" {
				continue // anonymous volume
			}
			if host := parts[0]; isHostPathCandidate(host) { // otherwise a named volume
				out = append(out, homelab.Scalar{Value: host, Line: item.Line})
			}
			continue
		}
		t, src := get(item, "type"), get(item, "source")
		if t != nil && t.Value == "bind" && src != nil && src.Kind == yaml.ScalarNode {
			out = append(out, homelab.Scalar{Value: src.Value, Line: src.Line})
		}
	}
	return out
}

// isHostPathCandidate reports whether a short-syntax source is a path rather than a volume
// name: absolute, relative, home-relative, or interpolated (classified after resolution).
func isHostPathCandidate(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") ||
		strings.HasPrefix(s, "$")
}

// splitVolume splits short volume syntax on ":" outside ${...}, so ${VAR:-d} stays whole.
func splitVolume(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == ':' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
