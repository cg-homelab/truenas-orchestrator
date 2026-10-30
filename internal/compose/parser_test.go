package compose

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.yaml.in/yaml/v3"

	"github.com/cg-homelab/truenas-orchestrator/internal/homelab"
)

func mustParse(t *testing.T, src string) homelab.ComposeFile {
	t.Helper()
	var p Parser
	cf, err := p.Parse("compose/app/compose.yaml", []byte(src))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return cf
}

func TestParseRefs(t *testing.T) {
	src := `x-common: &common
  TZ: ${TZ}
services:
  app:
    container_name: "${SERVICE_NAME}"
    image: alpine:${TAG:-3.22}
    command: ["sh", "-c", "echo $$HOME ${GREETING:?say hi}"]
    environment:
      <<: *common
      LOG: ${LOG_LEVEL-info}
      ESCAPED: $$NOT_A_REF
    volumes:
      - ${APPCONFIG_STORAGE:-${HOMELAB_STORAGE}}/app:/data
    labels:
      traefik.http.routers.${SERVICE_NAME}.rule: Host(` + "`${SERVICE_NAME}.${DOMAIN_NAME}`" + `)
    ports:
      - 8080:80 # ${COMMENTED} is a comment
secrets:
  key:
    file: $SECRETS_DIR/key
`
	cf := mustParse(t, src)
	type ref struct {
		Name, Path, Service string
		Line                int
		HasDefault          bool
		Required            bool
		Fallback            []homelab.Fallback
	}
	var got []ref
	for _, r := range cf.Refs {
		got = append(got, ref{r.Name, r.Path, r.Service, r.Line, r.HasDefault, r.Required, r.Fallback})
	}
	want := []ref{
		{"TZ", "x-common.TZ", "", 2, false, false, nil},
		{"SERVICE_NAME", "services.app.container_name", "app", 5, false, false, nil},
		{"TAG", "services.app.image", "app", 6, true, false, nil},
		{"GREETING", "services.app.command[2]", "app", 7, false, true, nil},
		{"LOG_LEVEL", "services.app.environment.LOG", "app", 10, true, false, nil},
		{"HOMELAB_STORAGE", "services.app.volumes[0]", "app", 13, false, false, []homelab.Fallback{{Name: "APPCONFIG_STORAGE", Colon: true}}},
		{"APPCONFIG_STORAGE", "services.app.volumes[0]", "app", 13, true, false, nil},
		// Label keys are not interpolated; only the value's references count.
		{"SERVICE_NAME", "services.app.labels.traefik.http.routers.${SERVICE_NAME}.rule", "app", 15, false, false, nil},
		{"DOMAIN_NAME", "services.app.labels.traefik.http.routers.${SERVICE_NAME}.rule", "app", 15, false, false, nil},
		{"SECRETS_DIR", "secrets.key.file", "", 20, false, false, nil},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Refs mismatch (-want +got):\n%s", diff)
	}

	var container []string
	for _, r := range cf.ContainerRefs {
		container = append(container, r.Name+"@"+r.Path)
	}
	// environment: values are never shell-evaluated, so $$NOT_A_REF is not a container read.
	wantContainer := []string{"HOME@services.app.command[2]"}
	if diff := cmp.Diff(wantContainer, container); diff != "" {
		t.Errorf("ContainerRefs mismatch (-want +got):\n%s", diff)
	}
}

func TestParseEnvironmentForms(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []homelab.EnvEntry
	}{
		{
			name: "map",
			src:  "services:\n  app:\n    environment:\n      A: one\n      B:\n      C: ${X}\n",
			want: []homelab.EnvEntry{
				{Key: "A", Value: "one", HasValue: true, Line: 4},
				{Key: "B", Line: 5},
				{Key: "C", Value: "${X}", HasValue: true, Line: 6},
			},
		},
		{
			name: "list",
			src:  "services:\n  app:\n    environment:\n      - A=one\n      - B\n      - C=x=y\n      - D=\n",
			want: []homelab.EnvEntry{
				{Key: "A", Value: "one", HasValue: true, Line: 4},
				{Key: "B", Line: 5},
				{Key: "C", Value: "x=y", HasValue: true, Line: 6},
				{Key: "D", HasValue: true, Line: 7},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := mustParse(t, tt.src)
			if diff := cmp.Diff(tt.want, cf.Services[0].Environment); diff != "" {
				t.Errorf("Environment mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseNetworkForms(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		want       []homelab.Scalar
		schemaLine int // 0 when valid
	}{
		{
			name: "list",
			src:  "services:\n  app:\n    image: x\n    networks:\n      - home\n      - proxy\n",
			want: []homelab.Scalar{{Value: "home", Line: 5}, {Value: "proxy", Line: 6}},
		},
		{
			name: "map",
			src:  "services:\n  app:\n    image: x\n    networks:\n      home:\n      proxy:\n        aliases: [p]\n",
			want: []homelab.Scalar{{Value: "home", Line: 5}, {Value: "proxy", Line: 6}},
		},
		{
			name:       "bare string is COMPOSE001 and still read",
			src:        "services:\n  app:\n    image: x\n    networks: databases\n",
			want:       []homelab.Scalar{{Value: "databases", Line: 4}},
			schemaLine: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := mustParse(t, tt.src)
			if diff := cmp.Diff(tt.want, cf.Services[0].Networks); diff != "" {
				t.Errorf("Networks mismatch (-want +got):\n%s", diff)
			}
			gotLine := 0
			if len(cf.SchemaErrors) > 0 {
				gotLine = cf.SchemaErrors[0].Line
				if !strings.Contains(cf.SchemaErrors[0].Message, "services.app.networks") {
					t.Errorf("SchemaErrors[0].Message = %q, want it to name services.app.networks", cf.SchemaErrors[0].Message)
				}
			}
			if gotLine != tt.schemaLine {
				t.Errorf("schema error line = %d (%+v), want %d", gotLine, cf.SchemaErrors, tt.schemaLine)
			}
		})
	}
}

func TestParseTopLevel(t *testing.T) {
	src := `services:
  app:
    image: x
    env_file:
      - ${HOMELAB_STORAGE}/compose/.global.env
      - path: ./optional.env
        required: false
    volumes:
      - ${APPCONFIG_STORAGE:-/mnt/x}/app:/data:ro
      - named:/named
      - /anonymous
      - ./rel:/rel
      - type: bind
        source: /mnt/long
        target: /long
      - type: volume
        source: vol
        target: /vol
    deploy:
      resources:
        limits:
          memory: 64M
  nolimits:
    image: y
    deploy:
      resources: {}
networks:
  home:
    external: true
  bridge:
    driver: bridge
secrets:
  token:
    file: /mnt/fast/token
volumes:
  named:
`
	cf := mustParse(t, src)
	if len(cf.SchemaErrors) != 0 {
		t.Errorf("SchemaErrors = %+v, want none", cf.SchemaErrors)
	}
	app := cf.Services[0]
	wantEnvFiles := []homelab.EnvFileRef{
		{Path: homelab.Scalar{Value: "${HOMELAB_STORAGE}/compose/.global.env", Line: 5}, Required: true},
		{Path: homelab.Scalar{Value: "./optional.env", Line: 6}, Required: false},
	}
	if diff := cmp.Diff(wantEnvFiles, app.EnvFiles); diff != "" {
		t.Errorf("EnvFiles mismatch (-want +got):\n%s", diff)
	}
	wantVolumes := []homelab.Scalar{
		{Value: "${APPCONFIG_STORAGE:-/mnt/x}/app", Line: 9},
		{Value: "./rel", Line: 12},
		{Value: "/mnt/long", Line: 14},
	}
	if diff := cmp.Diff(wantVolumes, app.Volumes); diff != "" {
		t.Errorf("Volumes mismatch (-want +got):\n%s", diff)
	}
	if !app.HasLimits || cf.Services[1].HasLimits {
		t.Errorf("HasLimits = %v, %v; want true, false", app.HasLimits, cf.Services[1].HasLimits)
	}
	if got, want := cf.Services[1].Line, 23; got != want {
		t.Errorf("Services[1].Line = %d, want %d", got, want)
	}
	wantNets := []homelab.NetworkDecl{{Key: "home", Name: "home", External: true, Line: 28}, {Key: "bridge", Name: "bridge", Line: 30}}
	if diff := cmp.Diff(wantNets, cf.Networks); diff != "" {
		t.Errorf("Networks mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]homelab.Scalar{{Value: "/mnt/fast/token", Line: 34}}, cf.SecretFiles); diff != "" {
		t.Errorf("SecretFiles mismatch (-want +got):\n%s", diff)
	}
}

func TestParseMergeKeys(t *testing.T) {
	src := `x-base: &base
  image: x
  networks: [home]
  deploy: {resources: {limits: {memory: 64M}}}
services:
  app:
    <<: *base
    networks: [proxy]
`
	cf := mustParse(t, src)
	svc := cf.Services[0]
	if svc.Image.Value != "x" || !svc.HasLimits {
		t.Errorf("merged service = %+v, want image and limits from the anchor", svc)
	}
	if diff := cmp.Diff([]homelab.Scalar{{Value: "proxy", Line: 8}}, svc.Networks); diff != "" {
		t.Errorf("explicit key must override the merged one (-want +got):\n%s", diff)
	}
}

func TestParseCommandForms(t *testing.T) {
	cf := mustParse(t, `services:
  app:
    image: x
    command: echo $$A
    entrypoint: ["/bin/sh", "-c"]
    healthcheck:
      test: ["CMD-SHELL", "test -n \"$${B}\""]
`)
	svc := cf.Services[0]
	got := [][]homelab.Scalar{svc.Command, svc.Entrypoint, svc.Healthcheck}
	want := [][]homelab.Scalar{
		{{Value: "echo $$A", Line: 4}},
		{{Value: "/bin/sh", Line: 5}, {Value: "-c", Line: 5}},
		{{Value: "CMD-SHELL", Line: 7}, {Value: `test -n "$${B}"`, Line: 7}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command forms mismatch (-want +got):\n%s", diff)
	}
	var names []string
	for _, r := range cf.ContainerRefs {
		names = append(names, r.Name)
	}
	if diff := cmp.Diff([]string{"A", "B"}, names); diff != "" {
		t.Errorf("ContainerRefs mismatch (-want +got):\n%s", diff)
	}
}

func TestParseSchemaAndSyntaxErrors(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		parseLine  int // >0: expect a *homelab.ParseError at this line
		schemaLine int // >0: expect one schema error at this line
	}{
		{
			name:      "bad indentation",
			src:       "services:\n  app:\n    networks:\n      - home\n     - databases\n",
			parseLine: 2,
		},
		{
			name:      "unclosed flow sequence",
			src:       "services:\n  app:\n    environment: [ unclosed\n",
			parseLine: 2, // yaml/v3 reports the line of the enclosing mapping
		},
		{
			name:       "unknown service key",
			src:        "services:\n  app:\n    image: x\n    imagee: y\n",
			schemaLine: 4,
		},
		{
			name:       "top level is a list",
			src:        "- a\n- b\n",
			schemaLine: 1,
		},
		{name: "empty document", src: "# services:\n"},
		{name: "interpolated typed fields are valid", src: "services:\n  app:\n    image: x\n    ports: [\"${HTTP_PORT}:80\"]\n    mem_limit: ${MEM:-64m}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Parser
			cf, err := p.Parse("compose/app/compose.yaml", []byte(tt.src))
			if tt.parseLine > 0 {
				var pe *homelab.ParseError
				if !errors.As(err, &pe) {
					t.Fatalf("Parse() error = %v, want *homelab.ParseError", err)
				}
				if pe.Line != tt.parseLine {
					t.Errorf("ParseError.Line = %d, want %d (%v)", pe.Line, tt.parseLine, pe)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			var lines []int
			for _, se := range cf.SchemaErrors {
				lines = append(lines, se.Line)
			}
			var want []int
			if tt.schemaLine > 0 {
				want = []int{tt.schemaLine}
			}
			if diff := cmp.Diff(want, lines, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("schema error lines mismatch (-want +got):\n%s\n%+v", diff, cf.SchemaErrors)
			}
		})
	}
}

func TestParseContainerRefForms(t *testing.T) {
	tests := []struct {
		name string
		svc  string
		want []string // name@line
	}{
		{
			name: "argon2 hash in environment is not a read",
			svc:  "    environment:\n      HASH: $$argon2id$$v=19$$m=65536,t=3,p=4$$c2FsdA\n",
		},
		{
			name: "string command",
			svc:  "    command: echo $$A\n",
			want: []string{"A@4"},
		},
		{
			name: "exec list without a shell is not evaluated",
			svc:  "    command: [\"echo\", \"$$A\"]\n",
		},
		{
			name: "sh -c list, only the script",
			svc:  "    command: [\"sh\", \"-c\", \"echo $$A\"]\n",
			want: []string{"A@4"},
		},
		{
			name: "bash -ec entrypoint",
			svc:  "    entrypoint: [\"/bin/bash\", \"-ec\", \"echo $${B}\"]\n",
			want: []string{"B@4"},
		},
		{
			name: "for loop and assignment are the script's own",
			svc:  "    command: [\"sh\", \"-c\", \"for i in 1 2; do echo $$i; done; X=1; echo $$X $$Y\"]\n",
			want: []string{"Y@4"},
		},
		{
			name: "healthcheck CMD is not evaluated, CMD-SHELL is",
			svc: "    healthcheck:\n      test: [\"CMD\", \"test\", \"$$A\"]\n" +
				"  b:\n    image: x\n    healthcheck:\n      test: [\"CMD-SHELL\", \"test -n $$B\"]\n",
			want: []string{"B@9"},
		},
		{
			name: "block scalar script gets the line of the read",
			svc:  "    command:\n      - sh\n      - -c\n      - |\n        echo start\n        echo $$LATE\n",
			want: []string{"LATE@9"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := mustParse(t, "services:\n  a:\n    image: x\n"+tt.svc)
			var got []string
			for _, r := range cf.ContainerRefs {
				got = append(got, r.Name+"@"+strconv.Itoa(r.Line))
			}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ContainerRefs mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseRefLinesInMultiLineScalars(t *testing.T) {
	src := `services:
  app:
    image: x
    labels:
      literal: |
        first ${A}
        second
        third ${B} and ${A}
      folded: >
        one
        ${C}
      plain: start
        ${D}
`
	cf := mustParse(t, src)
	var got []string
	for _, r := range cf.Refs {
		got = append(got, r.Name+"@"+strconv.Itoa(r.Line))
	}
	want := []string{"A@6", "B@8", "A@8", "C@11", "D@13"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ref lines mismatch (-want +got):\n%s", diff)
	}
}

func TestParseInvalidTemplateIsSchemaError(t *testing.T) {
	cf := mustParse(t, "services:\n  app:\n    image: x\n    labels:\n      - ok=${A}\n      - bad=${FOO BAR}\n      - also=${\n")
	var lines []int
	for _, se := range cf.SchemaErrors {
		lines = append(lines, se.Line)
	}
	if diff := cmp.Diff([]int{6, 7}, lines); diff != "" {
		t.Errorf("schema error lines mismatch (-want +got):\n%s\n%+v", diff, cf.SchemaErrors)
	}
	if len(cf.Refs) != 1 || cf.Refs[0].Name != "A" {
		t.Errorf("Refs = %+v, want only A", cf.Refs)
	}
}

func TestSchemaErrorLineDottedKeys(t *testing.T) {
	src := `services:
  web.v2:
    image: x
    labels:
      traefik.http.routers.web.rule: Host(x)
    deploy:
      resources:
        limits:
          cpus: [1]
`
	cf := mustParse(t, src)
	if len(cf.SchemaErrors) != 1 || cf.SchemaErrors[0].Line != 9 {
		t.Errorf("SchemaErrors = %+v, want one at line 9 (under the dotted service name)", cf.SchemaErrors)
	}
	root := func() *yaml.Node {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Content[0]
	}()
	if got := schemaErrorLine(root, "services.web.v2.labels.traefik.http.routers.web.rule is bad"); got != 5 {
		t.Errorf("schemaErrorLine(dotted label) = %d, want 5", got)
	}
}

func TestParseNetworkNameAndBools(t *testing.T) {
	cf := mustParse(t, `services:
  app:
    image: x
    networks: [proxy_net]
    env_file:
      - path: ./a.env
        required: False
      - path: ./b.env
        required: TRUE
networks:
  proxy_net:
    name: proxy
    external: True
`)
	want := []homelab.NetworkDecl{{Key: "proxy_net", Name: "proxy", External: true, Line: 11}}
	if diff := cmp.Diff(want, cf.Networks); diff != "" {
		t.Errorf("Networks mismatch (-want +got):\n%s", diff)
	}
	var req []bool
	for _, ef := range cf.Services[0].EnvFiles {
		req = append(req, ef.Required)
	}
	if diff := cmp.Diff([]bool{false, true}, req); diff != "" {
		t.Errorf("env_file required mismatch (-want +got):\n%s", diff)
	}
}
