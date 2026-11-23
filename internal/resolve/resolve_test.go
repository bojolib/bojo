package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LukasLow/bojo/internal/cache"
	"github.com/LukasLow/bojo/internal/manifest"
)

func mkpkg(path, commit string) Package {
	return Package{Name: "request", Path: path, Repo: "https://github.com/x/y", Commit: commit, Dir: "/tmp/x"}
}

func TestAddRejectsOnePathAtTwoCommits(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	if err := g.add(mkpkg("gh.x.y.request", "1111111111111111111111111111111111111111"), ""); err != nil {
		t.Fatal(err)
	}
	err := g.add(mkpkg("gh.x.y.request", "2222222222222222222222222222222222222222"), "")
	if err == nil {
		t.Fatal("two commits for one module path must fail")
	}
}

func TestAddIsIdempotentForTheSameCommit(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("gh.x.y.request", "1111111111111111111111111111111111111111")
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
	p := mkpkg("gh.LukasLow.mojo_http", "1111111111111111111111111111111111111111")
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
	body := "[repo]\nurl = \"https://github.com/x/mojo_socket\"\n[pkg]\n"
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
	p := g.Packages["gh.x.mojo_socket"]
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

func TestAddRecordsTheModulePathInTheLock(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("gh.LukasLow.mojo_http", "1111111111111111111111111111111111111111")
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
