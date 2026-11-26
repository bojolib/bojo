package resolve

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojolib/bojo/internal/cache"
	"github.com/bojolib/bojo/internal/manifest"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

func mkpkg(path, commit string) Package {
	return Package{Name: "request", Path: path, Repo: "https://github.com/x/y", Commit: commit, Dir: "/tmp/x"}
}

// fakeStore is an in-memory / temp-dir implementation of Store so resolver
// tests exercise the full Resolver.Resolve pipeline without network access.
type fakeStore struct {
	root  string
	repos map[string]*fakeRepo // normalized URL -> fakeRepo
}

type fakeRepo struct {
	tags    map[string]string // tag -> commit
	commits map[string]string // commit -> checkout directory
	trees   map[string]string // dir -> tree OID
}

func newFakeStore(t *testing.T, projectDir string) *fakeStore {
	t.Helper()
	return &fakeStore{
		root:  filepath.Join(projectDir, ".bojo", "cache"),
		repos: map[string]*fakeRepo{},
	}
}

func (f *fakeStore) addCommit(t *testing.T, repoURL, tagName, commit, pkgTOML string) string {
	t.Helper()
	norm, err := urlkey.Normalize(repoURL)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := f.repos[norm]
	if !ok {
		r = &fakeRepo{
			tags:    map[string]string{},
			commits: map[string]string{},
			trees:   map[string]string{},
		}
		f.repos[norm] = r
	}
	dir := filepath.Join(t.TempDir(), commit[:8])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bojo.pkg.toml"), []byte(pkgTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pkgTOML, "[pkg]") {
		if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(pkgTOML, "[cmd]") {
		if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmd", "main.mojo"), []byte("def main():\n    pass\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tagName != "" {
		r.tags[tagName] = commit
	}
	r.commits[commit] = dir
	r.trees[dir] = "tree-" + commit[:8]
	return dir
}

func (f *fakeStore) Root() string { return f.root }

func (f *fakeStore) Tags(repo string) ([]string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return nil, err
	}
	r, ok := f.repos[norm]
	if !ok {
		return nil, fmt.Errorf("unknown fake repo %s", norm)
	}
	out := make([]string, 0, len(r.tags))
	for t := range r.tags {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeStore) ResolveRef(repo, ref string) (string, string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", "", err
	}
	r, ok := f.repos[norm]
	if !ok {
		return "", "", fmt.Errorf("unknown fake repo %s", norm)
	}
	// Exact or prefix commit lookup
	for sha := range r.commits {
		if sha == ref || (len(ref) >= 7 && strings.HasPrefix(sha, ref)) {
			return "", sha, nil
		}
	}
	tags, err := f.Tags(norm)
	if err != nil {
		return "", "", err
	}
	tagName, _, err := tag.Find(ref, tags)
	if err != nil {
		return "", "", err
	}
	return tagName, r.tags[tagName], nil
}

func (f *fakeStore) Ensure(repo, tagName, commit, wantTree string) (string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", err
	}
	r, ok := f.repos[norm]
	if !ok {
		return "", fmt.Errorf("unknown fake repo %s", norm)
	}
	dir, ok := r.commits[commit]
	if !ok {
		return "", fmt.Errorf("unknown commit %s in %s", commit, norm)
	}
	return dir, nil
}

func (f *fakeStore) TreeOf(dir string) (string, error) {
	for _, r := range f.repos {
		if tr, ok := r.trees[dir]; ok {
			return tr, nil
		}
	}
	return "", fmt.Errorf("unknown dir %s", dir)
}

func TestAddRejectsOnePathAtTwoCommits(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	if err := g.add(mkpkg("gh.x.y", "1111111111111111111111111111111111111111"), ""); err != nil {
		t.Fatal(err)
	}
	err := g.add(mkpkg("gh.x.y", "2222222222222222222222222222222222222222"), "")
	if err == nil {
		t.Fatal("two commits for one module path must fail")
	}
}

func TestAddIsIdempotentForTheSameCommit(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("gh.x.y", "1111111111111111111111111111111111111111")
	if err := g.add(p, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.add(p, ""); err != nil {
		t.Fatalf("merging the same package twice must be a no-op: %v", err)
	}
	if len(g.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(g.Packages))
	}
}

func TestAddAppliesAliasToTheRoute(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("gh.bojolib.mojo_http", "1111111111111111111111111111111111111111")
	if err := g.add(p, "http"); err != nil {
		t.Fatal(err)
	}
	if len(g.Routes) != 1 || g.Routes[0].Path != "http" {
		t.Fatalf("routes = %+v, want one route named http", g.Routes)
	}
	if g.Packages[p.Path].Alias != "http" {
		t.Fatalf("alias not recorded: %+v", g.Packages[p.Path])
	}
}

func TestAddRejectsTwoPackagesOnOneRoute(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	a := mkpkg("gh.a.mojo_tls", "1111111111111111111111111111111111111111")
	b := mkpkg("gh.b.mojo_tls2", "1111111111111111111111111111111111111111")
	if err := g.add(a, "tls"); err != nil {
		t.Fatal(err)
	}
	if err := g.add(b, "tls"); err == nil {
		t.Fatal("two packages may not claim the same route")
	}
}

func TestDependencyMayOnlyDeclareVendorHooks(t *testing.T) {
	if err := checkPkgHooks(map[string][][]string{"vendor": {{"cc", "-shared"}}}); err != nil {
		t.Fatalf("vendor hooks must be allowed: %v", err)
	}
	if err := checkPkgHooks(map[string][][]string{"pre-build": {{"make"}}}); err == nil {
		t.Fatal("a dependency must not declare a build hook")
	}
}

func TestLocalDependencyIsResolvedFromItsDirectory(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, "socket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	body := "schema = 1\n[repo]\nurl = \"https://github.com/bojolib/socket\"\n[pkg]\n"
	if err := os.WriteFile(filepath.Join(dir, "bojo.pkg.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &manifest.File{Deps: manifest.DepsTable{
		Local: map[string]manifest.Dep{"socket": {Path: "socket"}},
	}}
	r := &Resolver{Store: cache.New(project)}
	g, err := r.Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(g.Packages))
	}
	p := g.Packages["gh.bojolib.socket"]
	if p == nil {
		t.Fatalf("local package not under its declared path: %+v", g.Packages)
	}
	if p.Commit != "" {
		t.Fatalf("a local dependency must not carry a commit, got %q", p.Commit)
	}
	if len(g.Lock) != 0 {
		t.Fatalf("a local dependency must not be pinned: %+v", g.Lock)
	}
}

func TestLocalCmdOnlyDependencyDoesNotPanic(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, "mytool")
	if err := os.MkdirAll(filepath.Join(dir, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "main.mojo"), []byte("def main():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "schema = 1\n[repo]\nurl = \"https://github.com/bojolib/mytool\"\n[cmd]\n"
	if err := os.WriteFile(filepath.Join(dir, "bojo.pkg.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &manifest.File{Deps: manifest.DepsTable{
		Local: map[string]manifest.Dep{"mytool": {Path: "mytool"}},
	}}
	r := &Resolver{Store: cache.New(project)}
	g, err := r.Resolve(cfg)
	if err != nil {
		t.Fatalf("cmd-only local dep should resolve cleanly: %v", err)
	}
	if len(g.Packages) != 0 {
		t.Fatalf("packages = %d, want 0", len(g.Packages))
	}
	if g.Commands["https://github.com/bojolib/mytool"] == nil {
		t.Fatalf("command not recorded in g.Commands: %+v", g.Commands)
	}
}

func TestLocalGitDepAtV2TagGetsV2ModulePath(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, "socket")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "schema = 1\n[repo]\nurl = \"https://github.com/bojolib/socket\"\n[pkg]\n"
	if err := os.WriteFile(filepath.Join(dir, "bojo.pkg.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=bojo", "GIT_AUTHOR_EMAIL=bojo@example.com",
			"GIT_COMMITTER_NAME=bojo", "GIT_COMMITTER_EMAIL=bojo@example.com",
			"GIT_AUTHOR_DATE=2020-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "v2")
	git("tag", "v2.0.0")

	cfg := &manifest.File{Deps: manifest.DepsTable{
		Local: map[string]manifest.Dep{"socket": {Path: "socket"}},
	}}
	r := &Resolver{Store: cache.New(project)}
	g, err := r.Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if g.Packages["v2.gh.bojolib.socket"] == nil {
		t.Fatalf("expected v2.gh.bojolib.socket, got %+v", g.Packages)
	}
}

func TestAddRecordsTheModulePathInTheLock(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("gh.bojolib.mojo_http", "1111111111111111111111111111111111111111")
	if err := g.add(p, ""); err != nil {
		t.Fatal(err)
	}
	if len(g.Lock) != 1 || g.Lock[0].Name != p.Path {
		t.Fatalf("lock entry = %+v, want the module path", g.Lock)
	}
	if g.Lock[0].Commit != p.Commit {
		t.Fatalf("lock must carry the full commit, got %q", g.Lock[0].Commit)
	}
}

func TestV1AndV2Coexistence(t *testing.T) {
	cases := []struct {
		name string
		cfg  func() *manifest.File
	}{
		{
			name: "direct v1 and v2 via @v2 key",
			cfg: func() *manifest.File {
				return &manifest.File{
					Schema:  1,
					Project: manifest.Project{Name: "app", Entry: "main.mojo"},
					DepsFrom: manifest.DepsFrom{
						"https://github.com/bojolib/socket":    {Version: "v1.2.0"},
						"https://github.com/bojolib/socket@v2": {Version: "v2.0.0"},
					},
					Alias: map[string]string{
						"v2.gh.bojolib.socket": "socket",
					},
				}
			},
		},
		{
			name: "transitive v1 from http and v2 from tls",
			cfg: func() *manifest.File {
				return &manifest.File{
					Schema:  1,
					Project: manifest.Project{Name: "app", Entry: "main.mojo"},
					DepsFrom: manifest.DepsFrom{
						"https://github.com/bojolib/http": {Version: "v0.1.0"},
						"https://github.com/bojolib/tls":  {Version: "v0.1.0"},
					},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			fs := newFakeStore(t, project)

			shaV1 := strings.Repeat("1", 40)
			shaV2 := strings.Repeat("2", 40)
			shaHTTP := strings.Repeat("a", 40)
			shaTLS := strings.Repeat("b", 40)

			fs.addCommit(t, "https://github.com/bojolib/socket", "v1.2.0", shaV1, `schema = 1
[repo]
url = "https://github.com/bojolib/socket"
[pkg]
`)
			fs.addCommit(t, "https://github.com/bojolib/socket", "v2.0.0", shaV2, `schema = 1
[repo]
url = "https://github.com/bojolib/socket"
[pkg]
`)
			fs.addCommit(t, "https://github.com/bojolib/http", "v0.1.0", shaHTTP, `schema = 1
[repo]
url = "https://github.com/bojolib/http"
[pkg]
[pkg.deps-from."https://github.com/bojolib/socket"]
version = "v1.2.0"
`)
			fs.addCommit(t, "https://github.com/bojolib/tls", "v0.1.0", shaTLS, `schema = 1
[repo]
url = "https://github.com/bojolib/tls"
[pkg]
[pkg.deps-from."https://github.com/bojolib/socket"]
version = "v2.0.0"
`)

			r := &Resolver{Store: fs}
			g, err := r.Resolve(tc.cfg())
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}

			v1Pkg := g.Packages["gh.bojolib.socket"]
			v2Pkg := g.Packages["v2.gh.bojolib.socket"]
			if v1Pkg == nil || v2Pkg == nil {
				t.Fatalf("expected both gh.bojolib.socket and v2.gh.bojolib.socket, got %+v", g.Packages)
			}
			if v1Pkg.Commit != shaV1 || v1Pkg.Tag != "v1.2.0" {
				t.Errorf("v1 pkg = %+v", v1Pkg)
			}
			if v2Pkg.Commit != shaV2 || v2Pkg.Tag != "v2.0.0" {
				t.Errorf("v2 pkg = %+v", v2Pkg)
			}

			locked := g.LockedCommits()
			if !locked["https://github.com/bojolib/socket@"+shaV1] {
				t.Errorf("LockedCommits missing v1 commit: %+v", locked)
			}
			if !locked["https://github.com/bojolib/socket@"+shaV2] {
				t.Errorf("LockedCommits missing v2 commit: %+v", locked)
			}
		})
	}
}

func TestDiamondConflictAndOverrideResolution(t *testing.T) {
	shaIO1 := strings.Repeat("1", 40)
	shaIO2 := strings.Repeat("2", 40)
	shaHTTP := strings.Repeat("a", 40)
	shaTLS := strings.Repeat("b", 40)

	setup := func(t *testing.T) (*fakeStore, string) {
		t.Helper()
		project := t.TempDir()
		fs := newFakeStore(t, project)
		fs.addCommit(t, "https://github.com/bojolib/io", "v0.1.0", shaIO1, `schema = 1
[repo]
url = "https://github.com/bojolib/io"
[pkg]
`)
		fs.addCommit(t, "https://github.com/bojolib/io", "v0.2.0", shaIO2, `schema = 1
[repo]
url = "https://github.com/bojolib/io"
[pkg]
`)
		fs.addCommit(t, "https://github.com/bojolib/http", "v0.1.0", shaHTTP, `schema = 1
[repo]
url = "https://github.com/bojolib/http"
[pkg]
[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
`)
		fs.addCommit(t, "https://github.com/bojolib/tls", "v0.1.0", shaTLS, `schema = 1
[repo]
url = "https://github.com/bojolib/tls"
[pkg]
[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.2.0"
`)
		return fs, project
	}

	t.Run("D69 conflict names both chains and override hint", func(t *testing.T) {
		fs, _ := setup(t)
		cfg := &manifest.File{
			Schema:  1,
			Project: manifest.Project{Name: "myapp", Entry: "main.mojo"},
			DepsFrom: manifest.DepsFrom{
				"https://github.com/bojolib/http": {Version: "v0.1.0"},
				"https://github.com/bojolib/tls":  {Version: "v0.1.0"},
			},
		}
		_, err := (&Resolver{Store: fs}).Resolve(cfg)
		if err == nil {
			t.Fatal("expected D69 conflict error, got nil")
		}
		msg := err.Error()
		for _, want := range []string{
			"gh.bojolib.io",
			"myapp -> gh.bojolib.http",
			"myapp -> gh.bojolib.tls",
			`[override."gh.bojolib.io"]`,
			`version = "v0.2.0"`,
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("conflict error missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("override by version resolves diamond conflict", func(t *testing.T) {
		fs, _ := setup(t)
		cfg := &manifest.File{
			Schema:  1,
			Project: manifest.Project{Name: "myapp", Entry: "main.mojo"},
			DepsFrom: manifest.DepsFrom{
				"https://github.com/bojolib/http": {Version: "v0.1.0"},
				"https://github.com/bojolib/tls":  {Version: "v0.1.0"},
			},
			Override: map[string]manifest.OverrideSpec{
				"gh.bojolib.io": {Version: "v0.2.0"},
			},
		}
		g, err := (&Resolver{Store: fs}).Resolve(cfg)
		if err != nil {
			t.Fatalf("override should resolve diamond conflict: %v", err)
		}
		ioPkg := g.Packages["gh.bojolib.io"]
		if ioPkg == nil {
			t.Fatalf("missing gh.bojolib.io in graph: %+v", g.Packages)
		}
		if ioPkg.Commit != shaIO2 || ioPkg.Tag != "v0.2.0" {
			t.Errorf("io = %+v, want v0.2.0 (%s)", ioPkg, shaIO2)
		}
		if len(ioPkg.RequiredBy) != 2 {
			t.Errorf("RequiredBy = %+v, want 2 chains", ioPkg.RequiredBy)
		}
	})

	t.Run("override by commit resolves diamond conflict", func(t *testing.T) {
		fs, _ := setup(t)
		cfg := &manifest.File{
			Schema:  1,
			Project: manifest.Project{Name: "myapp", Entry: "main.mojo"},
			DepsFrom: manifest.DepsFrom{
				"https://github.com/bojolib/http": {Version: "v0.1.0"},
				"https://github.com/bojolib/tls":  {Version: "v0.1.0"},
			},
			Override: map[string]manifest.OverrideSpec{
				"gh.bojolib.io": {Commit: shaIO1},
			},
		}
		g, err := (&Resolver{Store: fs}).Resolve(cfg)
		if err != nil {
			t.Fatalf("commit override should resolve diamond conflict: %v", err)
		}
		ioPkg := g.Packages["gh.bojolib.io"]
		if ioPkg == nil || ioPkg.Commit != shaIO1 {
			t.Fatalf("io = %+v, want commit %s", ioPkg, shaIO1)
		}
	})
}

func TestReplaceRedirectsToForkOrLocalPath(t *testing.T) {
	shaIO := strings.Repeat("1", 40)
	shaFork := strings.Repeat("f", 40)
	shaHTTP := strings.Repeat("a", 40)

	t.Run("replace with fork repo keeps original module path and sets ReplacedFrom", func(t *testing.T) {
		project := t.TempDir()
		fs := newFakeStore(t, project)
		fs.addCommit(t, "https://github.com/bojolib/io", "v0.1.0", shaIO, `schema = 1
[repo]
url = "https://github.com/bojolib/io"
[pkg]
`)
		fs.addCommit(t, "https://github.com/myfork/io", "v0.1.1-patch", shaFork, `schema = 1
[repo]
url = "https://github.com/myfork/io"
[pkg]
`)
		fs.addCommit(t, "https://github.com/bojolib/http", "v0.1.0", shaHTTP, `schema = 1
[repo]
url = "https://github.com/bojolib/http"
[pkg]
[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
`)

		cfg := &manifest.File{
			Schema:  1,
			Project: manifest.Project{Name: "myapp", Entry: "main.mojo"},
			DepsFrom: manifest.DepsFrom{
				"https://github.com/bojolib/http": {Version: "v0.1.0"},
			},
			Replace: map[string]manifest.ReplaceSpec{
				"gh.bojolib.io": {
					Repo:    "https://github.com/myfork/io",
					Version: "v0.1.1-patch",
				},
			},
		}

		g, err := (&Resolver{Store: fs}).Resolve(cfg)
		if err != nil {
			t.Fatalf("replace with fork failed: %v", err)
		}
		p := g.Packages["gh.bojolib.io"]
		if p == nil {
			t.Fatalf("original module path gh.bojolib.io not preserved: %+v", g.Packages)
		}
		if p.Repo != "https://github.com/myfork/io" {
			t.Errorf("Repo = %q, want fork URL", p.Repo)
		}
		if p.ReplacedFrom != "https://github.com/bojolib/io" {
			t.Errorf("ReplacedFrom = %q, want original URL", p.ReplacedFrom)
		}
		if p.Commit != shaFork || p.Tag != "v0.1.1-patch" {
			t.Errorf("Commit/Tag = %s/%s, want %s/v0.1.1-patch", p.Commit, p.Tag, shaFork)
		}
		foundLock := false
		for _, le := range g.Lock {
			if le.Name == "gh.bojolib.io" {
				foundLock = true
				if le.Repo != "https://github.com/myfork/io" || le.ReplacedFrom != "https://github.com/bojolib/io" || le.Commit != shaFork {
					t.Errorf("lock entry = %+v", le)
				}
			}
		}
		if !foundLock {
			t.Errorf("missing lock entry for replaced gh.bojolib.io: %+v", g.Lock)
		}
	})

	t.Run("replace with local path keeps original module path and is not pinned", func(t *testing.T) {
		project := t.TempDir()
		fs := newFakeStore(t, project)
		fs.addCommit(t, "https://github.com/bojolib/http", "v0.1.0", shaHTTP, `schema = 1
[repo]
url = "https://github.com/bojolib/http"
[pkg]
[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
`)

		localIO := filepath.Join(project, "local-io")
		if err := os.MkdirAll(localIO, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(localIO, "__init__.mojo"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(localIO, "bojo.pkg.toml"), []byte(`schema = 1
[repo]
url = "https://github.com/bojolib/io"
[pkg]
`), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg := &manifest.File{
			Schema:  1,
			Project: manifest.Project{Name: "myapp", Entry: "main.mojo"},
			DepsFrom: manifest.DepsFrom{
				"https://github.com/bojolib/http": {Version: "v0.1.0"},
			},
			Replace: map[string]manifest.ReplaceSpec{
				"gh.bojolib.io": {Path: "local-io"},
			},
		}

		g, err := (&Resolver{Store: fs}).Resolve(cfg)
		if err != nil {
			t.Fatalf("replace with local path failed: %v", err)
		}
		p := g.Packages["gh.bojolib.io"]
		if p == nil {
			t.Fatalf("original module path gh.bojolib.io not preserved: %+v", g.Packages)
		}
		if p.Dir != localIO {
			t.Errorf("Dir = %q, want %q", p.Dir, localIO)
		}
		if p.Commit != "" {
			t.Errorf("local replace must not carry commit, got %q", p.Commit)
		}
		if p.ReplacedFrom != "https://github.com/bojolib/io" {
			t.Errorf("ReplacedFrom = %q, want original URL", p.ReplacedFrom)
		}
		for _, le := range g.Lock {
			if le.Name == "gh.bojolib.io" {
				t.Errorf("local replace must not appear in lock: %+v", le)
			}
		}
	})
}
