package rmcheck

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

const ws = "/workspace"

// fsFake is a container filesystem for lstat: link paths are symlinks, the
// rest of the listed paths directories, anything else missing.
type fsFake struct{ dirs, links []string }

func (f fsFake) lstat(p string) (fs.FileMode, error) {
	switch {
	case slices.Contains(f.links, p):
		return fs.ModeSymlink | 0o777, nil
	case slices.Contains(f.dirs, p):
		return fs.ModeDir | 0o755, nil
	case strings.Contains(p, "unreadable"):
		return 0, fs.ErrPermission
	}
	return 0, fs.ErrNotExist
}

var tree = fsFake{
	dirs:  []string{"/workspace/build", "/workspace/web", "/workspace/web/dist", "/workspace/assets", "/workspace/assets/renders"},
	links: []string{"/workspace/out", "/workspace/web/cache"},
}

func TestAutoApproved(t *testing.T) {
	for _, tc := range []struct {
		cmd, cwd string
		want     []string
	}{
		{`rm -rf build`, ws, []string{"/workspace/build"}},
		{`rm -rf ./build ./web/dist`, ws, []string{"/workspace/build", "/workspace/web/dist"}},
		{`rm -f /workspace/web/dist/app.js`, "/", []string{"/workspace/web/dist/app.js"}},
		{`rm -rf dist`, "/workspace/web", []string{"/workspace/web/dist"}},
		{`D=build; rm -rf "$D/out"`, ws, []string{"/workspace/build/out"}},
		{`D=build
rm -rf $D`, ws, []string{"/workspace/build"}},
		{`rm -rf "$PWD/build"`, ws, []string{"/workspace/build"}},
		{`cd web && rm -rf dist`, ws, []string{"/workspace/web/dist"}},
		{`cd web; rm -rf dist`, ws, []string{"/workspace/web/dist", "/workspace/dist"}}, // cd may fail: both
		{`cd assets/renders && rm -f *.png`, ws, []string{"/workspace/assets/renders/*.png"}},
		{`rm -rf build/*`, ws, []string{"/workspace/build/*"}},
		{`rm -rf *`, ws, []string{"/workspace/*"}}, // bash's * skips .git
		{`rm -rf -- -weird-name`, ws, []string{"/workspace/-weird-name"}},
		{`rm -r 'my dir'`, ws, []string{"/workspace/my dir"}},
		{`rm -rf build && go build ./... && rm -rf web/dist`, ws, []string{"/workspace/build", "/workspace/web/dist"}},
		{`test -d build && rm -rf build || true`, ws, []string{"/workspace/build"}},
		{`[ -d build ] && rm -rf build`, ws, []string{"/workspace/build"}},
		{`[[ -d build ]] && rm -rf build`, ws, []string{"/workspace/build"}},
		{`rmdir web/dist/empty`, ws, []string{"/workspace/web/dist/empty"}},
		{`ls build | wc -l; rm -rf build`, ws, []string{"/workspace/build"}},
		{`/bin/rm -rf build`, ws, []string{"/workspace/build"}},
		{`rm -rf out`, ws, []string{"/workspace/out"}}, // the link itself, not what it points to
		{`rm -rf build 2>/dev/null`, ws, []string{"/workspace/build"}},
	} {
		r := Check(tc.cmd, tc.cwd, ws, tree.lstat)
		if !r.Auto {
			t.Errorf("%q: not auto: %s", tc.cmd, r.Why)
			continue
		}
		slices.Sort(r.Targets)
		slices.Sort(tc.want)
		if !slices.Equal(r.Targets, tc.want) {
			t.Errorf("%q: targets %q, want %q", tc.cmd, r.Targets, tc.want)
		}
	}
}

func TestLeftToTheLead(t *testing.T) {
	for cmd, why := range map[string]string{
		`rm -rf "$(ls -d dist)"`:               "command substitution",
		"rm -rf `ls -d dist`":                  "command substitution",
		`rm -rf "$(pwd)/build"`:                "command substitution",
		`rm -rf "$BUILD_DIR/out"`:              "not known",
		`rm -rf ${D:-build}`:                   "not known",
		`D=$(mktemp -d); rm -rf "$D"`:          "command substitution",
		`rm -rf $HOME/.cache`:                  "not known",
		`rm -rf ~/.cache`:                      "not known",
		`rm -rf .`:                             "the workspace itself",
		`rm -rf /workspace`:                    "the workspace itself",
		`rm -rf /workspace/`:                   "the workspace itself",
		`rm -rf ..`:                            "outside the workspace",
		`rm -rf build/../..`:                   "outside the workspace",
		`rm -rf /tmp/x`:                        "outside the workspace",
		`rm -rf /config/projects`:              "outside the workspace",
		`rm -rf .git`:                          "in .git",
		`rm -rf .git/hooks`:                    "in .git",
		`rm -rf sub/.git`:                      "in .git",
		`rm -rf .*`:                            "hidden",
		`rm -rf .g*`:                           "hidden",
		`rm -rf [.]git`:                        "hidden",
		`rm -rf */dist`:                        "wildcard before its last part",
		`rm -rf {build,dist}`:                  "not known",
		`rm -rf out/`:                          "symlink",
		`rm -rf web/cache/x`:                   "symlink",
		`rm -rf unreadable/x`:                  "could not be checked",
		`rm -rf $D`:                            "not known",
		`cd; rm -rf build`:                     "not known",
		`cd - && rm -rf build`:                 "not known",
		`cd "$X" && rm -rf build`:              "not known",
		`find . -name '*.tmp' -exec rm {} +`:   "another command",
		`find . -name '*.tmp' | xargs rm -f`:   "another command",
		`sh -c 'rm -rf build'`:                 "another command",
		`bash -c "rm -rf $X"`:                  "another command",
		`sudo rm -rf build`:                    "another command",
		`env rm -rf build`:                     "another command",
		`command rm -rf build`:                 "runs command",
		`eval "rm -rf build"`:                  "runs eval",
		`for d in a b; do rm -rf "$d"; done`:   "loop",
		`if true; then rm -rf build; fi`:       "loop or a conditional",
		`f() { rm -rf "$1"; }; f build`:        "function",
		`(cd web && rm -rf dist)`:              "subshell",
		`{ rm -rf build; }`:                    "subshell or a block",
		`rm -rf build &`:                       "background",
		`export D=build; rm -rf $D`:            "declares",
		`X="a b"; rm -rf $X`:                   "not known",
		`X='*'; rm -rf $X`:                     "not known",
		`R=rm; $R -rf build`:                   "not a plain word",
		`rm -rf "$1"`:                          "not known",
		`rm -rf ''`:                            "empty",
		`rm -rf`:                               "without a target",
		`go build ./...`:                       "no rm",
		`rm -rf build; rm -rf "$(cat list)"`:   "command substitution",
		`rm -rf build $'\x2e\x2e'`:             "not known",
		`D=build; D+=/x; rm -rf $D`:            "not known",
		`cat <(rm -rf build)`:                  "process substitution",
		`rm -rf build; unset D; rm -rf $D`:     "runs unset",
		`echo hi > >(rm -rf build)`:            "process substitution",
		`rm -rf "build" $((1+1))`:              "arithmetic",
		`ls | rm -rf build; rm -rf $(echo x)`:  "command substitution",
		`if`:                                   "parse",
		strings.Repeat("rm -rf build; ", 2000): "longer than",
		`alias rm=echo; rm -rf /`:              "runs alias",
		`D=build | true; rm -rf $D`:            "not known", // the pipe's assignment is in a subshell
	} {
		r := Check(cmd, ws, ws, tree.lstat)
		if r.Auto {
			t.Errorf("%q: auto-approved %q", cmd, r.Targets)
			continue
		}
		if !strings.Contains(r.Why, why) {
			t.Errorf("%q: why %q, want it to say %q", cmd, r.Why, why)
		}
	}
}

func TestRefusesBadContext(t *testing.T) {
	if r := Check("rm -rf build", "relative/dir", ws, nil); r.Auto {
		t.Error("relative cwd")
	}
	if r := Check("rm -rf build", "/", "/", nil); r.Auto {
		t.Error("workspace /")
	}
	if r := Check("rm -rf x", "/", "", nil); r.Auto {
		t.Error("no workspace")
	}
}

// TestNoLstat: without lstat (parser tests) symlinks are not checked.
func TestNoLstat(t *testing.T) {
	if r := Check("rm -rf out/", ws, ws, nil); !r.Auto {
		t.Fatalf("no lstat: %s", r.Why)
	}
}

// FuzzCheck: whatever the command, an approval only ever names targets
// strictly inside the workspace, outside .git.
func FuzzCheck(f *testing.F) {
	for _, s := range []string{`rm -rf build`, `cd web && rm -rf dist`, `D=x; rm -rf "$D/y"`, `rm -rf ../x`, `rm -rf *`,
		`rm -rf "$(ls)"`, `cd /; rm -rf workspace/x`, `cd ..; rm -rf workspace`, `rm -rf /workspace/./.git/../x`} {
		f.Add(s, "/workspace")
	}
	f.Fuzz(func(t *testing.T, cmd, cwd string) {
		r := Check(cmd, cwd, ws, tree.lstat)
		if !r.Auto {
			return
		}
		if len(r.Targets) == 0 {
			t.Fatalf("%q: approved with no target", cmd)
		}
		for _, p := range r.Targets {
			if !strings.HasPrefix(p, ws+"/") || strings.Contains("/"+p+"/", "/.git/") || strings.Contains(p, "/../") ||
				strings.HasSuffix(p, "/..") {
				t.Fatalf("%q from %q: approved target %q", cmd, cwd, p)
			}
		}
	})
}
