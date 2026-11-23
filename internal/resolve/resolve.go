// Package resolve turns bojo.toml plus remote repositories into a flat graph:
// every package, its import route, its linker flags and the hooks it wants.
//
// One repository is one package (D83), so resolution has no in-repo graph to
// walk and no tag namespace to disambiguate.
package resolve

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LukasLow/bojo/internal/cache"
	"github.com/LukasLow/bojo/internal/hooks"
	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/pkg"
	"github.com/LukasLow/bojo/internal/urlkey"
)

// Package is one resolved package.
type Package struct {
	Name string
	// Path is the import path, <host>.<owner>.<repo> (D82/D83).
	Path   string
	Alias  string
	Repo   string // normalised URL
	Commit string
	Tag    string
	Tree   string
	Dir    string

	Routes []string
	// Deps are the module paths this package depends on.
	Deps []string
	// Direct marks a package the project asked for itself.
	Direct  bool
	Kind    string
	Entry   string
	Linker  pkg.Linker
	Exclude []string
	Docs    string
	Hooks   map[string][][]string
}

// Route maps an import route to a directory.
type Route struct {
	Path string
	Dir  string
	Pkg  string
}

// LockEntry is one line of bojo.lock.
type LockEntry struct {
	Name   string
	Repo   string
	Tag    string
	Commit string
	Tree   string
	Alias  string
}

// Graph is the resolved model of a build.
type Graph struct {
	Packages map[string]*Package
	// Commands holds repositories that ship a [cmd], keyed by normalised URL.
	// A command is run, never imported, so it has no route (D83).
	Commands map[string]*Package
	Routes   []Route
	Hooks    []hooks.Requirement
	Lock     []LockEntry
}

// Resolver carries the cache and the trees pinned by bojo.lock.
type Resolver struct {
	Store *cache.Store
	// Locked maps "<repo>@<commit>" to the tree OID from the lock, so a version
	// bump does not get compared against the previous tree (review blocker).
	Locked  map[string]string
	Verbose bool
}

type job struct {
	direct bool
	repo   string
	ref    string
	commit string
	// dir is set for a local dependency: the source is a working directory, not
	// a checkout at a commit (D85).
	dir string
}

// projectDir is the directory bojo.toml lives in: the cache sits below
// <project>/.bojo/cache (D32).
func (r *Resolver) projectDir() string {
	return filepath.Dir(filepath.Dir(r.Store.Root()))
}

// Resolve walks the dependency graph starting from the project manifest.
func (r *Resolver) Resolve(cfg *manifest.File) (*Graph, error) {
	g := &Graph{Packages: map[string]*Package{}, Commands: map[string]*Package{}}

	var queue []job
	for label, d := range cfg.Deps.Local {
		if d.Path == "" {
			return nil, fmt.Errorf("local dependency %q needs a path", label)
		}
		dir := d.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(r.projectDir(), dir)
		}
		pt, err := pkg.LoadTree(dir)
		if err != nil {
			return nil, fmt.Errorf("local dependency %q: %w", label, err)
		}
		if len(pt.Pkg.Hooks) > 0 || (pt.Cmd != nil && len(pt.Cmd.Hooks) > 0) {
			return nil, fmt.Errorf("local dependency %q cannot declare hooks: an unpinned tree has nothing to approve", label)
		}
		queue = append(queue, job{direct: true, repo: pt.Repo.URL, dir: dir})
	}
	for repo, spec := range cfg.DepsFrom {
		ref := spec.Version
		if ref == "" {
			ref = "latest"
		}
		queue = append(queue, job{direct: true, repo: repo, ref: ref})
	}

	expanded := map[string]bool{} // repository already walked for nested deps

	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]

		norm, err := urlkey.Normalize(j.repo)
		if err != nil {
			return nil, err
		}

		var tagName, commit, tree string
		var dir string
		if j.dir != "" {
			dir = j.dir
		} else {
			if j.commit != "" {
				_, commit, err = r.Store.ResolveRef(norm, j.commit)
			} else {
				tagName, commit, err = r.Store.ResolveRef(norm, j.ref)
			}
			if err != nil {
				return nil, err
			}
			key := norm + "@" + commit
			dir, err = r.Store.Ensure(norm, tagName, commit, r.Locked[key])
			if err != nil {
				return nil, err
			}
			tree, err = r.Store.TreeOf(dir)
			if err != nil {
				return nil, err
			}
		}
		pt, err := pkg.LoadTree(dir)
		if err != nil {
			return nil, err
		}
		modPath, err := urlkey.ModulePath(norm)
		if err != nil {
			return nil, err
		}

		// A command is run, never imported: it has no module path and therefore
		// cannot collide with anything (D83).
		if pt.Pkg != nil {
			// A library never runs a compiler on someone else's machine (D57):
			// its only hook phase is "vendor", which runs once when the source
			// is materialised, in the store, never inside .vendor (D84).
			if err := checkPkgHooks(pt.Pkg.Hooks); err != nil {
				return nil, fmt.Errorf("%s: %w", norm, err)
			}
			deps, err := depPaths(pt)
			if err != nil {
				return nil, err
			}
			err = g.add(Package{
				Name: pt.Name, Path: modPath, Repo: norm, Commit: commit, Tag: tagName,
				Tree: tree, Dir: pt.Root, Routes: []string{modPath}, Deps: deps,
				Direct: j.direct, Linker: pt.Pkg.Linker, Exclude: pt.Pkg.Exclude,
				Docs: pt.Pkg.Docs, Hooks: pt.Pkg.Hooks,
			}, cfg.Alias[modPath])
			if err != nil {
				return nil, err
			}
		}

		if pt.Pkg == nil && pt.Cmd != nil {
			// A command-only repository is not importable, but `run`/`install`
			// must find its checkout and entry (D83).
			g.Commands[norm] = &Package{
				Name: pt.Name, Repo: norm, Commit: commit, Tag: tagName, Tree: tree,
				Dir: dir, Kind: "cmd", Entry: pt.Cmd.Entry, Direct: j.direct,
			}
		}

		// A repository's dependencies are enqueued once, never per package:
		// otherwise A->B->A grows the queue forever (review blocker).
		walkKey := norm + "@" + commit + "@" + dir
		if !expanded[walkKey] {
			expanded[walkKey] = true
			for _, d := range pkgRefs(pt) {
				queue = append(queue, job{repo: d.Repo, ref: d.Tag, commit: d.Commit})
			}
		}
	}

	sort.Slice(g.Routes, func(i, j int) bool { return g.Routes[i].Path < g.Routes[j].Path })
	sort.Slice(g.Lock, func(i, j int) bool { return g.Lock[i].Name < g.Lock[j].Name })
	return g, nil
}

// checkPkgHooks rejects any phase a dependency may not declare: a library only
// gets "vendor", which runs once against its store checkout (D84).
func checkPkgHooks(h map[string][][]string) error {
	phases := make([]string, 0, len(h))
	for phase := range h {
		phases = append(phases, phase)
	}
	sort.Strings(phases)
	for _, phase := range phases {
		if phase != "vendor" {
			return fmt.Errorf("a dependency may only declare \"vendor\" hooks, got %q", phase)
		}
	}
	return nil
}

func cmdDeps(pt *pkg.Tree) []pkg.DepFrom {
	if pt.Cmd == nil {
		return nil
	}
	return pt.Cmd.DepsFrom
}

// pkgRefs is every repository dependency a tree declares, library and command.
func pkgRefs(pt *pkg.Tree) []pkg.DepFrom {
	var refs []pkg.DepFrom
	if pt.Pkg != nil {
		refs = append(refs, pt.Pkg.DepsFrom...)
	}
	return append(refs, cmdDeps(pt)...)
}

// depPaths turns the declared repository dependencies of a package into module
// paths.
func depPaths(pt *pkg.Tree) ([]string, error) {
	var refs []pkg.DepFrom
	if pt.Pkg != nil {
		refs = append(refs, pt.Pkg.DepsFrom...)
	}
	refs = append(refs, cmdDeps(pt)...)
	var out []string
	for _, d := range refs {
		norm, err := urlkey.Normalize(d.Repo)
		if err != nil {
			return nil, err
		}
		mp, err := urlkey.ModulePath(norm)
		if err != nil {
			return nil, err
		}
		out = append(out, mp)
	}
	sort.Strings(out)
	return out, nil
}

// add registers a package, applies the project's alias and checks route
// conflicts (R1/R2).
func (g *Graph) add(p Package, alias string) error {
	if existing, ok := g.Packages[p.Path]; ok {
		if existing.Repo == p.Repo && existing.Commit == p.Commit && existing.Dir == p.Dir {
			return nil // already merged
		}
		return fmt.Errorf("package %q is provided twice: %s@%s and %s@%s (R6)",
			p.Path, existing.Repo, short(existing.Commit), p.Repo, short(p.Commit))
	}
	routes := []string{p.Path}
	if alias != "" {
		routes = []string{alias}
		p.Alias = alias
		p.Routes = []string{alias}
	}
	g.Packages[p.Path] = &p

	for _, route := range routes {
		cleanRoute, err := validateRoute(route)
		if err != nil {
			return fmt.Errorf("package %q: %w", p.Name, err)
		}
		for _, existing := range g.Routes {
			if strings.EqualFold(existing.Path, cleanRoute) {
				return fmt.Errorf("conflict: route %q provided by %q and %q", cleanRoute, existing.Pkg, p.Name)
			}
		}
		g.Routes = append(g.Routes, Route{Path: cleanRoute, Dir: p.Dir, Pkg: p.Name})
	}

	for hookName, commands := range p.Hooks {
		for _, cmd := range commands {
			if len(cmd) == 0 {
				continue
			}
			g.Hooks = append(g.Hooks, hooks.Requirement{
				Repo: p.Repo, Commit: p.Commit, Pkg: p.Name, Hook: hookName, Command: cmd,
			})
		}
	}

	// A local dependency has no commit, so there is nothing to pin (D85). It is
	// resolved from a working directory every time.
	if p.Commit != "" {
		g.Lock = append(g.Lock, LockEntry{
			Name: p.Path, Repo: p.Repo, Tag: p.Tag, Commit: p.Commit,
			Tree: "git-tree:" + p.Tree, Alias: p.Alias,
		})
	}
	return nil
}

// validateRoute rejects anything that could escape the module root: absolute
// paths, parent segments, empty segments and backslashes (review blocker).
func validateRoute(route string) (string, error) {
	r := strings.TrimSpace(route)
	if r == "" {
		return "", fmt.Errorf("empty route")
	}
	if strings.ContainsAny(r, "\\:") || strings.HasPrefix(r, "/") {
		return "", fmt.Errorf("route %q must be a relative slash path", route)
	}
	cleaned := path.Clean(r)
	if cleaned == "." || cleaned != strings.Trim(r, "/") {
		return "", fmt.Errorf("route %q is not normalised", route)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("route %q contains a forbidden segment", route)
		}
	}
	return cleaned, nil
}

// ModulesDir is where the import routes are materialised.
func ModulesDir(projectDir string) string {
	return filepath.Join(projectDir, ".bojo", "cache", "mojo", "modules")
}

// LockedCommits returns repo -> commit for the staleness check (D28).
func (g *Graph) LockedCommits() map[string]string {
	out := map[string]string{}
	for _, e := range g.Lock {
		out[e.Repo] = e.Commit
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
