package composefile

import (
	"errors"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func quoted(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.DoubleQuotedStyle}
}

func seqOf(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

func path(p string) []string { return strings.Split(p, ".") }

// doc trims the leading newline so test YAML can start on its own line.
func doc(s string) string { return strings.TrimPrefix(s, "\n") }

func TestApplyEditsGolden(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		edits []edit
		want  string
	}{
		{
			name: "replace inline scalar keeps comment and spacing",
			in: doc(`
services:
  web:
    image: "nginx:1.27"   # pinned
    restart: always
`),
			edits: []edit{{path("services.web.image"), scalar("nginx:1.28")}},
			want: doc(`
services:
  web:
    image: nginx:1.28   # pinned
    restart: always
`),
		},
		{
			name: "replace compact block sequence keeps surrounding blank lines",
			in: doc(`
# My lab
services:
  web:
    ports:
    - "8080:80"
    - 8443:443

    environment:
      TZ: Europe/Berlin
`),
			edits: []edit{{path("services.web.ports"), seqOf(quoted("9090:80"))}},
			want: doc(`
# My lab
services:
  web:
    ports:
    - "9090:80"

    environment:
      TZ: Europe/Berlin
`),
		},
		{
			name: "replace indented sequence keeps key-line comment",
			in: doc(`
services:
  web:
    ports: # published
      - "80:80"
    restart: always
`),
			edits: []edit{{path("services.web.ports"), seqOf(quoted("81:80"), quoted("443:443"))}},
			want: doc(`
services:
  web:
    ports: # published
      - "81:80"
      - "443:443"
    restart: always
`),
		},
		{
			name: "insert new key after last entry, before trailing blank lines",
			in: doc(`
services:
  web:
    image: nginx

  db:
    image: postgres
`),
			edits: []edit{{path("services.web.restart"), scalar("unless-stopped")}},
			want: doc(`
services:
  web:
    image: nginx
    restart: unless-stopped

  db:
    image: postgres
`),
		},
		{
			name: "create nested path",
			in: doc(`
services:
  web:
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("512M")}},
			want: doc(`
services:
  web:
    image: nginx
    deploy:
      resources:
        limits:
          memory: 512M
`),
		},
		{
			name: "four-space indentation is respected",
			in: doc(`
services:
    web:
        image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.cpus"), quoted("0.5")}},
			want: doc(`
services:
    web:
        image: nginx
        deploy:
            resources:
                limits:
                    cpus: "0.5"
`),
		},
		{
			name: "delete key with block value",
			in: doc(`
services:
  web:
    image: nginx
    volumes:
      - data:/data   # app data
      - ./conf:/conf
    restart: always
`),
			edits: []edit{{path("services.web.volumes"), nil}},
			want: doc(`
services:
  web:
    image: nginx
    restart: always
`),
		},
		{
			name:  "deleting a missing key is a no-op",
			in:    "services:\n  web:\n    image: nginx\n",
			edits: []edit{{path("services.web.restart"), nil}},
			want:  "services:\n  web:\n    image: nginx\n",
		},
		{
			name: "deleting the only key leaves an empty mapping",
			in: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          memory: 512M
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), nil}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits: {}
`),
		},
		{
			name: "edit inside flow mapping rewrites it in block style",
			in: doc(`
services:
  web:
    deploy: {resources: {limits: {cpus: "1"}}}
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("1G")}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          cpus: "1"
          memory: 1G
    image: nginx
`),
		},
		{
			name: "null value becomes nested mapping",
			in: doc(`
services:
  web:
    deploy:
    image: nginx
`),
			edits: []edit{{path("services.web.deploy.resources.limits.memory"), scalar("1G")}},
			want: doc(`
services:
  web:
    deploy:
      resources:
        limits:
          memory: 1G
    image: nginx
`),
		},
		{
			name: "block scalar replaced by inline scalar",
			in: doc(`
services:
  db:
    command: >
      postgres -c max_connections=200
    image: postgres
`),
			edits: []edit{{path("services.db.command"), scalar("postgres")}},
			want: doc(`
services:
  db:
    command: postgres
    image: postgres
`),
		},
		{
			name:  "insert into file without trailing newline",
			in:    "services:\n  web:\n    image: nginx",
			edits: []edit{{path("services.web.restart"), scalar("always")}},
			want:  "services:\n  web:\n    image: nginx\n    restart: always",
		},
		{
			name: "top-level insert goes after trailing comments of deeper entries only",
			in: doc(`
services:
  web:
    image: nginx
# end of services
`),
			edits: []edit{{path("networks.lan.external"), scalar("true")}},
			want: doc(`
services:
  web:
    image: nginx
networks:
  lan:
    external: true
# end of services
`),
		},
		{
			name:  "insert after multi-line flow sequence closing at key indent",
			in:    "services:\n  web:\n    command: [\n      \"a\",\n    ]\n",
			edits: []edit{{path("services.web.restart"), scalar("always")}},
			want:  "services:\n  web:\n    command: [\n      \"a\",\n    ]\n    restart: always\n",
		},
		{
			name:  "replace multi-line flow sequence closing at key indent",
			in:    "services:\n  web:\n    command: [\n      \"a\",\n    ]\n    image: x\n",
			edits: []edit{{path("services.web.command"), scalar("b")}},
			want:  "services:\n  web:\n    command: b\n    image: x\n",
		},
		{
			name:  "comment after a tab is kept",
			in:    "services:\n  web:\n    image: nginx\t# pinned\n",
			edits: []edit{{path("services.web.image"), scalar("httpd")}},
			want:  "services:\n  web:\n    image: httpd\t# pinned\n",
		},
		{
			name:  "insert after flow sequence closing shallower than its key",
			in:    "web:\n  a:\n    b: [\n  ]\n",
			edits: []edit{{path("web.a.c"), scalar("1")}},
			want:  "web:\n  a:\n    b: [\n  ]\n    c: 1\n",
		},
		{
			name:  "replace flow sequence closing shallower than its key",
			in:    "web:\n  a:\n    b: [\n  1]\n  c: 2\n",
			edits: []edit{{path("web.a.b"), scalar("x")}},
			want:  "web:\n  a:\n    b: x\n  c: 2\n",
		},
		{
			name:  "tab-separated comment is kept when value becomes a block",
			in:    "services:\n  web:\n    b: nginx\t# pinned\n",
			edits: []edit{{path("services.web.b"), &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("k"), scalar("v")}}}},
			want:  "services:\n  web:\n    b: # pinned\n      k: v\n",
		},
		{
			name:  "apostrophe in flow sequence: replace",
			in:    "services:\n  web:\n    command: [echo, don't]\n    entrypoint: [it's]\n    image: x\n",
			edits: []edit{{path("services.web.command"), scalar("hi")}},
			want:  "services:\n  web:\n    command: hi\n    entrypoint: [it's]\n    image: x\n",
		},
		{
			name:  "apostrophe in flow sequence: delete",
			in:    "services:\n  web:\n    command: [echo, don't]\n    entrypoint: [it's]\n    image: x\n",
			edits: []edit{{path("services.web.command"), nil}},
			want:  "services:\n  web:\n    entrypoint: [it's]\n    image: x\n",
		},
		{
			name:  "apostrophe in flow sequence: insert",
			in:    "web:\n  command: [echo, don't]\n",
			edits: []edit{{path("web.c"), scalar("1")}},
			want:  "web:\n  command: [echo, don't]\n  c: 1\n",
		},
		{
			name:  "double quote inside plain flow scalar: insert",
			in:    "web:\n  command: [a\"b]\n",
			edits: []edit{{path("web.c"), scalar("1")}},
			want:  "web:\n  command: [a\"b]\n  c: 1\n",
		},
		{
			name:  "tagged flow sequence: insert",
			in:    "web:\n  a: !!seq [1]\n",
			edits: []edit{{path("web.z"), scalar("1")}},
			want:  "web:\n  a: !!seq [1]\n  z: 1\n",
		},
		{
			name:  "colon then quote inside plain flow scalar: replace",
			in:    "web:\n  a: [echo:'hi]\n  b: [c']\n  d: 1\n",
			edits: []edit{{path("web.a"), scalar("R")}},
			want:  "web:\n  a: R\n  b: [c']\n  d: 1\n",
		},
		{
			name:  "colon then quote inside plain flow scalar: delete",
			in:    "web:\n  a: [echo:'hi]\n  b: [c']\n  d: 1\n",
			edits: []edit{{path("web.a"), nil}},
			want:  "web:\n  b: [c']\n  d: 1\n",
		},
		{
			name:  "quoted scalar after tag in flow sequence: delete",
			in:    "web:\n  a: [!!str 'a]\n  b']\n  b: 1\n",
			edits: []edit{{path("web.a"), nil}},
			want:  "web:\n  b: 1\n",
		},
		{
			name:  "apostrophe in flow sequence keeps trailing comment",
			in:    "web:\n  a: [it's] # keep\n  b: 1\n",
			edits: []edit{{path("web.a"), scalar("R")}},
			want:  "web:\n  a: R # keep\n  b: 1\n",
		},
		{
			name:  "colon then quote in flow sequence keeps trailing comment",
			in:    "web:\n  a: [echo:'hi] # keep\n  b: 1\n",
			edits: []edit{{path("web.a"), scalar("R")}},
			want:  "web:\n  a: R # keep\n  b: 1\n",
		},
		{
			name:  "apostrophe in flow sequence keeps comment holding quote and bracket",
			in:    "web:\n  a: [it's] # c']\n  b: 1\n",
			edits: []edit{{path("web.a"), scalar("R")}},
			want:  "web:\n  a: R # c']\n  b: 1\n",
		},
		{
			name:  "NaN elsewhere in the file does not fail verification",
			in:    "x-cfg:\n  ratio: .nan\nservices:\n  web:\n    image: a\n",
			edits: []edit{{path("services.web.image"), scalar("b")}},
			want:  "x-cfg:\n  ratio: .nan\nservices:\n  web:\n    image: b\n",
		},
		{
			name:  "comment directly after comma in flow sequence: delete",
			in:    "web:\n  a: [x,#c]\n y]\n  b: 1\n",
			edits: []edit{{path("web.a"), nil}},
			want:  "web:\n  b: 1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyEdits([]byte(tt.in), tt.edits)
			if err != nil {
				t.Fatalf("applyEdits: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestApplyEditsRefuses(t *testing.T) {
	tests := []struct {
		name string
		in   string
		e    edit
		want error
	}{
		{
			name: "anchored value",
			in:   "x-base: &base\n  restart: always\nservices:\n  web:\n    <<: *base\n",
			e:    edit{path("x-base.restart"), scalar("no")},
			want: errSharedNode,
		},
		{
			name: "alias value",
			in:   "x-env: &env\n  A: b\nservices:\n  web:\n    environment: *env\n",
			e:    edit{path("services.web.environment.A"), scalar("c")},
			want: errSharedNode,
		},
		{
			name: "path through a scalar",
			in:   "services:\n  web:\n    image: nginx\n",
			e:    edit{path("services.web.image.tag"), scalar("x")},
			want: errPathNotMapping,
		},
		{
			name: "CRLF line endings",
			in:   "services:\r\n  web:\r\n    image: nginx\r\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "lone CR line break",
			in:   "services:\n  web:\n    a: [x, #\ry]\n    image: nginx\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "NEL line break",
			in:   "services:\n  web:\n    a: [x, #\u0085y]\n    image: nginx\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "line separator",
			in:   "services:\n  web:\n    a: [x, #\u2028y]\n    image: nginx\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "paragraph separator",
			in:   "services:\n  web:\n    a: [x, #\u2029y]\n    image: nginx\n",
			e:    edit{path("services.web.image"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "flow mapping root",
			in:   "{services: {}}\n",
			e:    edit{path("services.web"), scalar("x")},
			want: errUnsupportedLayout,
		},
		{
			name: "anchor inside flow mapping",
			in:   "services:\n  a:\n    deploy: {resources: &q {limits: {cpus: \"1\"}}}\n  b:\n    deploy: {resources: *q}\n",
			e:    edit{path("services.a.deploy.resources.limits.memory"), scalar("1G")},
			want: errSharedNode,
		},
		{
			name: "alias inside flow mapping",
			in:   "x-r: &r {reservations: {cpus: \"1\"}}\nservices:\n  a:\n    deploy: {resources: *r}\n",
			e:    edit{path("services.a.deploy.resources.limits.memory"), scalar("1G")},
			want: errSharedNode,
		},
		{
			name: "path through scalar inside flow mapping",
			in:   "services:\n  a:\n    deploy: {resources: 5}\n",
			e:    edit{path("services.a.deploy.resources.limits.memory"), scalar("1G")},
			want: errPathNotMapping,
		},
		{
			name: "replacing value with nested anchor",
			in:   "services:\n  a:\n    environment: {A: &v foo}\n  b:\n    environment: {A: *v}\n",
			e:    edit{path("services.a.environment"), &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("A"), scalar("bar")}}},
			want: errSharedNode,
		},
		{
			name: "deleting value with nested anchor",
			in:   "services:\n  a:\n    environment:\n      A: &v foo\n    image: x\n  b:\n    environment:\n      A: *v\n",
			e:    edit{path("services.a.environment"), nil},
			want: errSharedNode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := applyEdits([]byte(tt.in), []edit{tt.e})
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}
