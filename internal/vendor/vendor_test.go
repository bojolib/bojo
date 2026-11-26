package vendor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojolib/bojo/internal/resolve"
)

func pkgDir(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildIsFlatAndOneDirectoryPerModulePath(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()

	// Two repositories with the same last segment, different owners (D83).
	a := pkgDir(t, src, "markus", "def connect() -> Int:\n    return 111\n")
	b := pkgDir(t, src, "anna", "def connect() -> Int:\n    return 222\n")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.Markus.mojosocket": {Name: "mojosocket", Path: "gh.Markus.mojosocket", Dir: a, Commit: strings.Repeat("a", 40)},
		"gh.Anna.mojosocket":   {Name: "mojosocket", Path: "gh.Anna.mojosocket", Dir: b, Commit: strings.Repeat("b", 40)},
	}}

	m, err := Build(project, g)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(m.Entries))
	}
	for _, want := range []string{
		"gh/Markus/mojosocket/__init__.mojo",
		"gh/Anna/mojosocket/__init__.mojo",
	} {
		if _, err := os.Stat(filepath.Join(Modules(project), filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestBuildGeneratesAliasShim(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "addition", "def add(a: Int, b: Int) -> Int:\n    return a + b\n")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.bojolib.mojo_addition": {
			Name: "mojo_addition", Path: "gh.bojolib.mojo_addition",
			Dir: dir, Commit: strings.Repeat("c", 40), Alias: "mathx",
		},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(Modules(project), "mathx", "__init__.mojo"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "from gh.bojolib.mojo_addition import *") {
		t.Fatalf("shim does not re-export the full path:\n%s", got)
	}
}

func TestBuildRejectsOnePathAtTwoCommits(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "z", "def label() -> String:\n    return \"z\"\n")

	// The graph keys by path, so this can only be constructed directly.
	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.X.z": {Name: "z", Path: "gh.X.z", Dir: dir, Commit: strings.Repeat("1", 40)},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}

	// A second package with the same path but another commit is rejected by the
	// resolver already (g.add); Build keeps its own guard for hand-built graphs.
	g.Packages["other"] = &resolve.Package{Name: "z2", Path: "gh.X.z", Dir: dir, Commit: strings.Repeat("2", 40)}
	if _, err := Build(project, g); err == nil {
		t.Fatal("two commits for one module path must fail")
	}
}

func TestBuildUsesGitArchiveAndHonoursExportIgnore(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "http", "def get() -> Int:\n    return 1\n")
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("tests export-ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "a.mojo"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that is not committed must not reach the vendor tree, because the
	// vendored tree is the commit, not the working directory (D73).
	if err := os.WriteFile(filepath.Join(dir, "scratch.mojo"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=bojo", "GIT_AUTHOR_EMAIL=bojo@example.com",
			"GIT_COMMITTER_NAME=bojo", "GIT_COMMITTER_EMAIL=bojo@example.com",
			"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".gitattributes", "__init__.mojo", "tests/a.mojo")
	git("commit", "-q", "-m", "fixture")
	commit := git("rev-parse", "HEAD")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.X.mojo_http": {Name: "mojo_http", Path: "gh.X.mojo_http", Dir: dir, Commit: commit},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(Modules(project), "gh/X/mojo_http/tests/a.mojo")); !os.IsNotExist(err) {
		t.Fatalf("export-ignore must drop tests/, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(Modules(project), "gh/X/mojo_http/scratch.mojo")); !os.IsNotExist(err) {
		t.Fatalf("an uncommitted file must not be vendored, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(Modules(project), "gh/X/mojo_http/__init__.mojo")); err != nil {
		t.Fatalf("the committed body must be vendored: %v", err)
	}
}

func TestBuildHonoursExclude(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "http", "def get() -> Int:\n    return 1\n")
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "a.mojo"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.X.mojo_http": {Name: "mojo_http", Path: "gh.X.mojo_http", Dir: dir, Commit: strings.Repeat("3", 40), Exclude: []string{"tests/**"}},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(Modules(project), "gh/X/mojo_http/tests/a.mojo")); !os.IsNotExist(err) {
		t.Fatalf("tests/a.mojo must be excluded, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(Modules(project), "gh/X/mojo_http/__init__.mojo")); err != nil {
		t.Fatalf("package body must be copied: %v", err)
	}
}

func TestBuildCoexistsV1AndV2ModulePaths(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	v1Dir := pkgDir(t, src, "v1", "def ver() -> Int:\n    return 1\n")
	v2Dir := pkgDir(t, src, "v2", "def ver() -> Int:\n    return 2\n")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"gh.bojolib.socket": {
			Name: "socket", Path: "gh.bojolib.socket", Dir: v1Dir,
			Commit: strings.Repeat("1", 40),
		},
		"v2.gh.bojolib.socket": {
			Name: "socket", Path: "v2.gh.bojolib.socket", Dir: v2Dir,
			Commit: strings.Repeat("2", 40), Alias: "socket",
		},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"gh/bojolib/socket/__init__.mojo",
		"v2/gh/bojolib/socket/__init__.mojo",
		"socket/__init__.mojo",
	} {
		if _, err := os.Stat(filepath.Join(Modules(project), filepath.FromSlash(rel))); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	shim, err := os.ReadFile(filepath.Join(Modules(project), "socket/__init__.mojo"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(shim), "from v2.gh.bojolib.socket import *") {
		t.Fatalf("v2 alias shim mismatch:\n%s", shim)
	}
}
