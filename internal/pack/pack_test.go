package pack

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const goodSkill = "---\nname: s\ndescription: does things\n---\n# S\n"

func validFS() fstest.MapFS {
	return fstest.MapFS{
		"pack.yaml": {Data: []byte(`schema: 1
name: demo
description: A demo pack.
toolchain:
  containerfile: Containerfile
egress:
  allow: [example.com, .example.org]
env:
  - name: DEMO_KEY
    description: test key
    required: true
  - name: DEMO_OPTIONAL
skills: [s]
mcp_servers:
  docs: {url: "https://example.com/mcp"}
  tool: {command: /usr/local/bin/tool, args: [mcp, --stdio]}
`)},
		"Containerfile":     {Data: []byte("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nRUN true\n")},
		"files/extra.txt":   {Data: []byte("x")},
		"skills/s/SKILL.md": {Data: []byte(goodSkill)},
		"skills/s/ref.md":   {Data: []byte("more")},
	}
}

func TestBuiltinsAreValid(t *testing.T) {
	entries := Builtins()
	got := map[string]*Pack{}
	for _, e := range entries {
		if e.Err != nil {
			t.Fatalf("built-in %s: %v", e.Name, e.Err)
		}
		got[e.Name] = e.Pack
	}
	for _, name := range []string{"go", "node"} {
		p := got[name]
		if p == nil || p.Containerfile == "" || len(p.Egress) == 0 || len(p.Skills) != 1 || p.Source != SourceBuiltin {
			t.Fatalf("built-in %s: %+v", name, p)
		}
		files, err := p.BuildFiles()
		if _, hasYAML := files[File]; err != nil || files["Containerfile"].Data == nil || hasYAML {
			t.Fatalf("build files of %s: %v %v", name, keys(files), err)
		}
	}
	if !strings.Contains(strings.Join(got["go"].Egress, ","), "proxy.golang.org") {
		t.Fatalf("go pack egress %v", got["go"].Egress)
	}
}

func TestLoadValid(t *testing.T) {
	p, err := load(validFS(), "demo", "test", "demo/pack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo" || p.Containerfile != "Containerfile" || len(p.Egress) != 2 || len(p.Env) != 2 ||
		!p.Env[0].Required || p.Env[1].Required || len(p.Skills) != 1 {
		t.Fatalf("%+v", p)
	}
	if names := p.ServerNames(); len(names) != 2 || names[0] != "docs" || p.MCPServers["tool"].Args[1] != "--stdio" {
		t.Fatalf("servers %+v", p.MCPServers)
	}
	files, err := p.BuildFiles()
	if err != nil || len(files) != 2 || string(files["files/extra.txt"].Data) != "x" {
		t.Fatalf("build files %v %v", keys(files), err)
	}

	withMem := fstest.MapFS{"pack.yaml": {Data: []byte("schema: 1\nname: mem\ndescription: d\nresources:\n  memory: 4g\n")}}
	if p, err := load(withMem, "mem", "test", "mem/pack.yaml"); err != nil || p.Memory != "4g" || p.Pids != 0 {
		t.Fatalf("resources.memory: %+v %v", p, err)
	}
	withPids := fstest.MapFS{"pack.yaml": {Data: []byte("schema: 1\nname: pids\ndescription: d\nresources:\n  pids: 2048\n")}}
	if p, err := load(withPids, "pids", "test", "pids/pack.yaml"); err != nil || p.Pids != 2048 || p.Memory != "" {
		t.Fatalf("resources.pids: %+v %v", p, err)
	}

	minimal := fstest.MapFS{"pack.yaml": {Data: []byte("schema: 1\nname: min\ndescription: Nothing but a name.\n")}}
	p, err = load(minimal, "min", "test", "min/pack.yaml")
	if err != nil || p.Containerfile != "" || p.Egress != nil {
		t.Fatalf("%+v %v", p, err)
	}
	if files, err := p.BuildFiles(); files != nil || err != nil {
		t.Fatal("a pack without a Containerfile has no build files")
	}
}

func TestLoadInvalid(t *testing.T) {
	base := "schema: 1\nname: demo\ndescription: d\n"
	cases := []struct {
		name  string
		yaml  string
		files map[string]string
		want  []string
	}{
		{"empty", "", nil, []string{"the file is empty"}},
		{"not yaml", "schema: [1\n", nil, []string{"not valid YAML"}},
		{"unknown field", base + "egres:\n  allow: [a.com]\n", nil, []string{`unknown field "egres"`, "line 4"}},
		{"nested unknown field", base + "egress:\n  allowed: [a.com]\n", nil, []string{`unknown field "allowed"`}},
		{"two documents", base + "---\nname: x\n", nil, []string{"more than one YAML document"}},
		{"wrong type", "schema: one\nname: demo\ndescription: d\n", nil, []string{"cannot unmarshal"}},
		{"no schema", "name: demo\ndescription: d\n", nil, []string{"schema: missing; add `schema: 1`"}},
		{"future schema", "schema: 2\nname: demo\ndescription: d\n", nil, []string{"schema: version 2 is not supported"}},
		{"no name", "schema: 1\ndescription: d\n", nil, []string{"name: missing; add `name: demo`"}},
		{"bad name", "schema: 1\nname: Demo\ndescription: d\n", nil, []string{`name: "Demo" must be lower-case`}},
		{"name mismatch", "schema: 1\nname: other\ndescription: d\n", nil, []string{`"other" does not match the pack directory "demo"`}},
		{"no description", "schema: 1\nname: demo\n", nil, []string{"description: missing"}},
		{"multi-line description", "schema: 1\nname: demo\ndescription: |\n  a\n  b\n", nil, []string{"must be one line"}},
		{"duplicate key", base + "name: demo\n", nil, []string{"already defined"}},
		{"bad memory", base + "resources:\n  memory: 4GB\n", nil, []string{`resources.memory: "4GB" must be a size between 256m and 256g`}},
		{"tiny memory", base + "resources:\n  memory: 64m\n", nil, []string{`resources.memory: "64m"`}},
		{"empty resources", base + "resources:\n  memory: ''\n", nil, []string{"resources: empty"}},
		{"few pids", base + "resources:\n  pids: 10\n", nil, []string{"resources.pids: 10 must be between 64 and 65536"}},
		{"many pids", base + "resources:\n  pids: 100000\n", nil, []string{"resources.pids: 100000"}},
		{"wildcard host", base + "egress:\n  allow: ['*.example.com']\n", nil, []string{`egress.allow[0]: "*.example.com" is not a host name`}},
		{"ip host", base + "egress:\n  allow: [1.2.3.4]\n", nil, []string{`egress.allow[0]: "1.2.3.4" is not a host name`}},
		{"url host", base + "egress:\n  allow: [example.com, 'https://a.com']\n", nil, []string{`egress.allow[1]: "https://a.com"`}},
		{"upper-case host", base + "egress:\n  allow: [Example.com]\n", nil, []string{`egress.allow[0]: "Example.com"`}},
		{"duplicate host", base + "egress:\n  allow: [a.com, a.com]\n", nil, []string{`egress.allow[1]: "a.com" is listed twice`}},
		{"bad env name", base + "env:\n  - name: my-key\n", nil, []string{`env[0].name: "my-key" must be upper-case`}},
		{"reserved env", base + "env:\n  - name: GITHUB_TOKEN\n  - name: CLAUDE_CODE_OAUTH_TOKEN\n  - name: HTTPS_PROXY\n  - name: LD_PRELOAD\n", nil,
			[]string{"env[0].name: GITHUB_TOKEN is reserved", "env[1].name: CLAUDE_CODE_OAUTH_TOKEN is reserved",
				"env[2].name: HTTPS_PROXY is reserved", "env[3].name: LD_PRELOAD is reserved"}},
		{"duplicate env", base + "env:\n  - name: A_KEY\n  - name: A_KEY\n", nil, []string{"env[1].name: A_KEY is listed twice"}},
		{"env value field", base + "env:\n  - name: A_KEY\n    value: hunter2\n", nil, []string{`unknown field "value"`}},
		{"missing skill", base + "skills: [nope]\n", nil, []string{"skills[0]: skills/nope/SKILL.md not found"}},
		{"traversing skill", base + "skills: ['../x']\n", nil, []string{`skills[0]: "../x" must be lower-case`}},
		{"skill without frontmatter", base + "skills: [s]\n", map[string]string{"skills/s/SKILL.md": "# S\n"},
			[]string{"must start with YAML frontmatter"}},
		{"skill without description", base + "skills: [s]\n", map[string]string{"skills/s/SKILL.md": "---\nname: s\n---\n"},
			[]string{"the frontmatter needs a description"}},
		{"skill name mismatch", base + "skills: [s]\n", map[string]string{"skills/s/SKILL.md": "---\nname: t\ndescription: x\n---\n"},
			[]string{"frontmatter name t differs"}},
		{"reserved server", base + "mcp_servers:\n  agent-office: {command: /bin/x}\n", nil, []string{`mcp_servers.agent-office: the name "agent-office" is reserved`}},
		{"bad server name", base + "mcp_servers:\n  a__b: {command: /bin/x}\n", nil, []string{"mcp_servers.a__b: the name must be"}},
		{"server both", base + "egress:\n  allow: [a.com]\nmcp_servers:\n  x: {command: /bin/x, url: 'https://a.com'}\n", nil, []string{"not both"}},
		{"server neither", base + "mcp_servers:\n  x: {args: [a]}\n", nil, []string{"set command"}},
		{"relative command", base + "mcp_servers:\n  x: {command: tool}\n", nil, []string{`command "tool" must be an absolute`}},
		{"http url", base + "egress:\n  allow: [a.com]\nmcp_servers:\n  x: {url: 'http://a.com/mcp'}\n", nil, []string{"must be an https:// URL"}},
		{"url with credentials", base + "egress:\n  allow: [a.com]\nmcp_servers:\n  x: {url: 'https://u:p@a.com/mcp'}\n", nil, []string{"without credentials"}},
		{"url other port", base + "egress:\n  allow: [a.com]\nmcp_servers:\n  x: {url: 'https://a.com:8443/mcp'}\n", nil, []string{"only allows port 443"}},
		{"url host not allowed", base + "mcp_servers:\n  x: {url: 'https://b.com/mcp'}\n", nil, []string{"the host b.com is not in egress.allow"}},
		{"no containerfile", base + "toolchain:\n  containerfile: Containerfile\n", nil, []string{"Containerfile not found"}},
		{"empty containerfile field", base + "toolchain: {}\n", nil, []string{"toolchain.containerfile: missing"}},
		{"traversing containerfile", base + "toolchain:\n  containerfile: ../Containerfile\n", nil, []string{"must be a relative path inside the pack"}},
		{"containerfile without arg", base + "toolchain:\n  containerfile: Containerfile\n", map[string]string{"Containerfile": "FROM ${BASE_IMAGE}\n"},
			[]string{"must declare `ARG BASE_IMAGE` before its first FROM"}},
		{"containerfile other base", base + "toolchain:\n  containerfile: Containerfile\n",
			map[string]string{"Containerfile": "ARG BASE_IMAGE\nFROM ${BASE_IMAGE} AS b\nFROM docker.io/library/golang:1.27\n"},
			[]string{"the last stage must start with `FROM ${BASE_IMAGE}`", "golang:1.27"}},
		{"containerfile entrypoint", base + "toolchain:\n  containerfile: Containerfile\n",
			map[string]string{"Containerfile": "ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nENTRYPOINT [\"/bin/tool\"]\n"},
			[]string{"the last stage sets an ENTRYPOINT"}},
		{"url host upper case", base + "egress:\n  allow: [a.com]\nmcp_servers:\n  x: {url: 'https://A.com/mcp'}\n", nil,
			[]string{"must name a lower-case host"}},
		{"containerfile no from", base + "toolchain:\n  containerfile: Containerfile\n", map[string]string{"Containerfile": "ARG BASE_IMAGE\nRUN true\n"},
			[]string{"has no FROM"}},
		{"oversized", base + "description: " + strings.Repeat("x", maxPackBytes) + "\n", nil, []string{"larger than 64 KiB"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fsys := fstest.MapFS{"pack.yaml": {Data: []byte(c.yaml)}}
			for k, v := range c.files {
				fsys[k] = &fstest.MapFile{Data: []byte(v)}
			}
			_, err := load(fsys, "demo", "test", "demo/pack.yaml")
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("want *Error, got %v", err)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "demo/pack.yaml: invalid stack pack") {
				t.Errorf("message does not name the file: %s", msg)
			}
			for _, w := range c.want {
				if !strings.Contains(msg, w) {
					t.Errorf("want %q in:\n%s", w, msg)
				}
			}
		})
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	fsys := fstest.MapFS{"pack.yaml": {Data: []byte("schema: 1\nname: Bad\negress:\n  allow: ['*']\nenv:\n  - name: PATH\nskills: [gone]\n")}}
	_, err := load(fsys, "bad", "test", "bad/pack.yaml")
	var pe *Error
	if !errors.As(err, &pe) || len(pe.Problems) != 5 {
		t.Fatalf("want 5 problems (name, description, egress, env, skill), got %v", err)
	}
	if !strings.Contains(err.Error(), "(5 problems, see docs/packs.md)") {
		t.Fatal(err)
	}
}

func TestContainerfileParsing(t *testing.T) {
	ok := []string{
		"ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n",
		"# syntax comment\nARG BASE_IMAGE=x\nFROM docker.io/library/golang:1.27 AS build\nRUN go build\nFROM --platform=linux/amd64 $BASE_IMAGE\nCOPY --from=build /x /x\n",
		"arg BASE_IMAGE\nfrom ${BASE_IMAGE}\nRUN a \\\n  && b\n",
		"ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nRUN a \\\n# a comment inside a continuation\n  && b\n",
		"ARG BASE_IMAGE\nFROM debian AS tools\nENTRYPOINT [\"x\"]\nFROM ${BASE_IMAGE}\n", // an earlier stage may
	}
	for _, src := range ok {
		if msg := checkContainerfile([]byte(src)); msg != "" {
			t.Errorf("%q: %s", src, msg)
		}
	}
	bad := []string{
		"FROM ${BASE_IMAGE}\nARG BASE_IMAGE\n",
		"ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\nFROM debian\n",
		"ARG BASE_IMAGE\nRUN echo \\\nFROM ${BASE_IMAGE}\n", // the FROM is part of the RUN line
	}
	for _, src := range bad {
		if msg := checkContainerfile([]byte(src)); msg == "" {
			t.Errorf("%q accepted", src)
		}
	}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func validTree() map[string]string {
	out := map[string]string{}
	for k, v := range validFS() {
		out[k] = string(v.Data)
	}
	return out
}

func TestLoadDirChecksTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeTree(t, dir, validTree())
	if _, err := LoadDir(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(filepath.Join(dir, "Containerfile"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "files", "link")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadDir(dir)
	for _, w := range []string{"Containerfile: is writable by group or others (chmod go-w", "files/link: is a symlink"} {
		if err == nil || !strings.Contains(err.Error(), w) {
			t.Errorf("want %q, got %v", w, err)
		}
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "the pack directory: is writable by group or others") {
		t.Errorf("group-writable pack dir: %v", err)
	}
}

func TestFindAndList(t *testing.T) {
	user := t.TempDir()
	writeTree(t, filepath.Join(user, "demo"), validTree())
	p, err := Find("demo", user)
	if err != nil || p.Source != filepath.Join(user, "demo") {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := Find("go", user); err != nil || p.Source != SourceBuiltin {
		t.Fatalf("built-in go: %+v %v", p, err)
	}
	_, err = Find("rust", user)
	if err == nil || !strings.Contains(err.Error(), `unknown stack pack "rust"; available: demo, go, node`) {
		t.Fatalf("unknown pack: %v", err)
	}
	if _, err := Find("../x", user); err == nil || !strings.Contains(err.Error(), "is invalid") {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := Find("go", filepath.Join(user, "missing")); err != nil {
		t.Fatalf("a missing user dir is fine: %v", err)
	}

	// A user pack may not shadow a built-in, and broken packs are listed with their error.
	writeTree(t, filepath.Join(user, "go"), map[string]string{"pack.yaml": "schema: 1\nname: go\ndescription: mine\n"})
	writeTree(t, filepath.Join(user, "broken"), map[string]string{"pack.yaml": "name: broken\n"})
	if _, err := Find("go", user); err == nil || !strings.Contains(err.Error(), `may not reuse the built-in name "go"`) {
		t.Fatalf("shadowing: %v", err)
	}
	entries, err := List(user)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
		if e.Name == "broken" && e.Err == nil {
			t.Fatal("broken pack listed without its error")
		}
	}
	if got := strings.Join(names, ","); got != "broken,demo,go,go,node" {
		t.Fatalf("list %s", got)
	}
	if err := os.Chmod(user, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("demo", user); err == nil || !strings.Contains(err.Error(), "the packs directory: is writable by group or others") {
		t.Fatalf("group-writable packs dir: %v", err)
	}
}

func TestWriteSkills(t *testing.T) {
	fsys := validFS()
	p, err := load(fsys, "demo", "test", "demo/pack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What agents get is what was checked at load, not the files as they are later.
	fsys["skills/s/SKILL.md"] = &fstest.MapFile{Data: []byte("changed after load")}
	dst := filepath.Join(t.TempDir(), "skills")
	if err := p.WriteSkills(dst); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"s/SKILL.md": goodSkill, "s/ref.md": "more"} {
		if b, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
}

func TestReadSecrets(t *testing.T) {
	p, err := load(validFS(), "demo", "test", "demo/pack.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = p.ReadSecrets(dir)
	if !errors.Is(err, ErrSecret) || !strings.Contains(err.Error(), "needs DEMO_KEY (test key); create "+filepath.Join(dir, "DEMO_KEY")) {
		t.Fatalf("missing required: %v", err)
	}
	if st := p.SecretStatus(dir); len(st) != 2 || st[0].Present || !st[0].Required {
		t.Fatalf("status %+v", st)
	}
	if err := os.WriteFile(filepath.Join(dir, "DEMO_KEY"), []byte("canary-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vals, skipped, err := p.ReadSecrets(dir)
	if err != nil || vals["DEMO_KEY"].Reveal() != "canary-value" || len(vals) != 1 || len(skipped) != 1 || skipped[0] != "DEMO_OPTIONAL" {
		t.Fatalf("%v %v %v", vals, skipped, err)
	}
	if st := p.SecretStatus(dir); !st[0].Present || st[1].Present {
		t.Fatalf("status %+v", st)
	}
	if err := os.WriteFile(filepath.Join(dir, "DEMO_OPTIONAL"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := p.SecretStatus(dir); st[1].Present || !strings.Contains(st[1].Problem, "chmod 600") || strings.Contains(st[1].Problem, "x\n") {
		t.Fatalf("an unusable file is not present: %+v", st[1])
	}
	if _, _, err := p.ReadSecrets(dir); !errors.Is(err, ErrSecret) || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a present optional file with a bad mode must fail: %v", err)
	}
}

func TestBuildFilesKeepExecAndSkipGit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	writeTree(t, dir, validTree())
	writeTree(t, dir, map[string]string{"tool.sh": "#!/bin/sh\n", ".git/HEAD": "ref: x\n"})
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, ".git", "link")); err != nil {
		t.Fatal(err)
	}
	p, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("a pack kept in git: %v", err)
	}
	files, err := p.BuildFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !files["tool.sh"].Exec || files["Containerfile"].Exec {
		t.Fatalf("exec bits %+v", files)
	}
	for name := range files {
		if strings.HasPrefix(name, ".git") {
			t.Fatalf("%s in the build context", name)
		}
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
