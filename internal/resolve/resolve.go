// Package resolve turns bojo.toml plus remote repositories into a flat graph:
// every package, its import route, its linker flags and the hooks it wants.
//
// One repository is one package (D83), so resolution has no in-repo graph to
// walk and no tag namespace to disambiguate. Major versions v2+ receive a
// `v<N>.` module-path prefix so v1 and v2 of the same repository coexist
// cleanly without collisions.
package resolve

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/cache"
	"github.com/bojolib/bojo/internal/hooks"
	"github.com/bojolib/bojo/internal/manifest"
	"github.com/bojolib/bojo/internal/pkg"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

// Store is the cache surface Resolver needs. *cache.Store implements it
// directly; tests may supply a local fake without touching the network.
type Store interface {
	Root() string
	Tags(repo string) ([]string, error)
	ResolveRef(repo, ref string) (tagName, commit string, err error)
	Ensure(repo, tag, commit, wantTree string) (string, error)
	TreeOf(dir string) (string, error)
}

var _ Store = (*cache.Store)(nil)

// Package is one resolved package.
type Package struct {
	Name string
	// Path is the import path: <host>.<owner>.<repo> for v0/v1, or
	// v<N>.<host>.<owner>.<repo> for major >= 2.
	Path         string
	Alias        string
	Repo         string // normalised URL (for replaced repos, the actual source repo)
	ReplacedFrom string // original normalised repo URL when redirected via [replace]
	Commit       string
	Tag          string
	Tree         string
	Dir          string

	Routes []string
	// Deps are the module paths this package depends on.
	Deps []string
	// RequiredBy records the dependency chains that requested this package.
	RequiredBy [][]string
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
	Name         string
	Repo         string
	Tag          string
	Commit       string
	Tree         string
	Alias        string
	ReplacedFrom string
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
	Store Store
	// Locked maps "<repo>@<commit>" to the tree OID from the lock, so a version
	// bump does not get compared against the previous tree (review blocker).
	Locked  map[string]string
	Verbose bool
}

type job struct {
	direct    bool
	repo      string
	ref       string
	commit    string
	majorHint int
	alias     string
	// dir is set for a local dependency: the source is a working directory, not
	// a checkout at a commit (D85).
	dir string
	// chain is the dependency path leading to this job (root -> ... -> parent).
	chain []string
	// parentPath and depPlaceholder let us update parent.Deps when a "latest"
	// dependency resolves to v2+.
	parentPath     string
	depPlaceholder string
}

// projectDir is the directory bojo.toml lives in: the cache sits below
// <project>/.bojo/cache (D32).
func (r *Resolver) projectDir() string {
	return filepath.Dir(filepath.Dir(r.Store.Root()))
}

// Resolve walks the dependency graph starting from the project manifest.
func (r *Resolver) Resolve(cfg *manifest.File) (*Graph, error) {
	g := &Graph{Packages: map[string]*Package{}, Commands: map[string]*Package{}}
	rootLabel := cfg.Project.Name
	if rootLabel == "" {
		rootLabel = "root"
	}

	var queue []job

	localLabels := make([]string, 0, len(cfg.Deps.Local))
	for label := range cfg.Deps.Local {
		localLabels = append(localLabels, label)
	}
	sort.Strings(localLabels)
	for _, label := range localLabels {
		d := cfg.Deps.Local[label]
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
		if (pt.Pkg != nil && len(pt.Pkg.Hooks) > 0) || (pt.Cmd != nil && len(pt.Cmd.Hooks) > 0) {
			return nil, fmt.Errorf("local dependency %q cannot declare hooks: an unpinned tree has nothing to approve", label)
		}
		queue = append(queue, job{
			direct: true,
			repo:   pt.Repo.URL,
			dir:    dir,
			chain:  []string{rootLabel},
		})
	}

	depKeys := make([]string, 0, len(cfg.DepsFrom))
	for k := range cfg.DepsFrom {
		depKeys = append(depKeys, k)
	}
	sort.Strings(depKeys)
	for _, rawKey := range depKeys {
		spec := cfg.DepsFrom[rawKey]
		repoRaw, majorHint := urlkey.SplitDepKey(rawKey)
		ref := spec.Version
		if ref == "" && spec.Commit == "" {
			ref = "latest"
		}
		queue = append(queue, job{
			direct:    true,
			repo:      repoRaw,
			ref:       ref,
			commit:    spec.Commit,
			majorHint: majorHint,
			alias:     spec.Alias,
			chain:     []string{rootLabel},
		})
	}

	expanded := map[string]bool{} // (modPath, repo, commit, dir) already walked for nested deps

	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]

		norm, err := urlkey.Normalize(j.repo)
		if err != nil {
			return nil, err
		}

		initMajor := tag.MajorOf(j.ref)
		if initMajor == 0 {
			initMajor = j.majorHint
		}
		candidateModPath, err := urlkey.VersionedModulePath(norm, initMajor)
		if err != nil {
			return nil, err
		}

		fetchRepo := norm
		replacedFrom := ""
		modPath := candidateModPath
		var tagName, commit, tree, dir string
		var pt *pkg.Tree

		applyReplace := func(targetPath string, rep manifest.ReplaceSpec) error {
			replacedFrom = norm
			modPath = targetPath
			if rep.Path != "" {
				localDir := rep.Path
				if !filepath.IsAbs(localDir) {
					localDir = filepath.Join(r.projectDir(), localDir)
				}
				loaded, err := pkg.LoadTree(localDir)
				if err != nil {
					return fmt.Errorf("replace %q: %w", targetPath, err)
				}
				if (loaded.Pkg != nil && len(loaded.Pkg.Hooks) > 0) || (loaded.Cmd != nil && len(loaded.Cmd.Hooks) > 0) {
					return fmt.Errorf("replace %q: local replacement cannot declare hooks", targetPath)
				}
				dir = localDir
				pt = loaded
				fetchRepo = norm
				tagName, commit, tree = "", "", ""
				return nil
			}
			forkNorm, err := urlkey.Normalize(rep.Repo)
			if err != nil {
				return fmt.Errorf("replace %q: %w", targetPath, err)
			}
			fetchRepo = forkNorm
			tagName, commit, err = r.resolveRemoteRef(fetchRepo, rep.Version, rep.Commit, 0)
			if err != nil {
				return fmt.Errorf("replace %q: %w", targetPath, err)
			}
			dir, tree, err = r.checkout(fetchRepo, tagName, commit)
			if err != nil {
				return fmt.Errorf("replace %q: %w", targetPath, err)
			}
			pt = nil
			return nil
		}

		applyOverride := func(targetPath string, ov manifest.OverrideSpec) error {
			fetchRepo = norm
			modPath = targetPath
			tagName, commit, err = r.resolveRemoteRef(fetchRepo, ov.Version, ov.Commit, 0)
			if err != nil {
				return fmt.Errorf("override %q: %w", targetPath, err)
			}
			dir, tree, err = r.checkout(fetchRepo, tagName, commit)
			if err != nil {
				return fmt.Errorf("override %q: %w", targetPath, err)
			}
			pt = nil
			return nil
		}

		if rep, ok := cfg.Replace[candidateModPath]; ok {
			if err := applyReplace(candidateModPath, rep); err != nil {
				return nil, err
			}
		} else if ov, ok := cfg.Override[candidateModPath]; ok {
			if err := applyOverride(candidateModPath, ov); err != nil {
				return nil, err
			}
		} else if j.dir != "" {
			dir = j.dir
			major := initMajor
			if major == 0 {
				major = majorFromHEAD(dir)
			}
			modPath, err = urlkey.VersionedModulePath(norm, major)
			if err != nil {
				return nil, err
			}
			if modPath != candidateModPath {
				if rep, ok := cfg.Replace[modPath]; ok {
					if err := applyReplace(modPath, rep); err != nil {
						return nil, err
					}
				} else if ov, ok := cfg.Override[modPath]; ok {
					if err := applyOverride(modPath, ov); err != nil {
						return nil, err
					}
				}
			}
		} else {
			tagName, commit, err = r.resolveRemoteRef(fetchRepo, j.ref, j.commit, j.majorHint)
			if err != nil {
				return nil, err
			}
			major := tag.MajorOf(tagName)
			if major == 0 {
				major = j.majorHint
			}
			modPath, err = urlkey.VersionedModulePath(norm, major)
			if err != nil {
				return nil, err
			}
			if modPath != candidateModPath {
				if rep, ok := cfg.Replace[modPath]; ok {
					if err := applyReplace(modPath, rep); err != nil {
						return nil, err
					}
				} else if ov, ok := cfg.Override[modPath]; ok {
					if err := applyOverride(modPath, ov); err != nil {
						return nil, err
					}
				}
			}
			if dir == "" {
				dir, tree, err = r.checkout(fetchRepo, tagName, commit)
				if err != nil {
					return nil, err
				}
				if tagName == "" && major == 0 {
					if hm := majorFromHEAD(dir); hm >= 2 {
						modPath, err = urlkey.VersionedModulePath(norm, hm)
						if err != nil {
							return nil, err
						}
						if rep, ok := cfg.Replace[modPath]; ok {
							if err := applyReplace(modPath, rep); err != nil {
								return nil, err
							}
						} else if ov, ok := cfg.Override[modPath]; ok {
							if err := applyOverride(modPath, ov); err != nil {
								return nil, err
							}
						}
					}
				}
			}
		}

		if pt == nil {
			pt, err = pkg.LoadTree(dir)
			if err != nil {
				return nil, err
			}
		}

		if j.parentPath != "" && j.depPlaceholder != "" && modPath != j.depPlaceholder {
			if parent := g.Packages[j.parentPath]; parent != nil {
				replaceDep(parent, j.depPlaceholder, modPath)
			}
		}

		// A command is run, never imported: it has no module path and therefore
		// cannot collide with anything (D83).
		if pt.Pkg != nil {
			// A library never runs a compiler on someone else's machine (D57):
			// its only hook phase is "vendor", which runs once when the source
			// is materialised, in the store, never inside .vendor (D84).
			if err := checkPkgHooks(pt.Pkg.Hooks); err != nil {
				return nil, fmt.Errorf("%s: %w", fetchRepo, err)
			}
			deps, err := depPaths(pt)
			if err != nil {
				return nil, err
			}
			alias := cfg.Alias[modPath]
			if alias == "" {
				alias = j.alias
			}
			var chains [][]string
			if len(j.chain) > 0 {
				chains = [][]string{append([]string(nil), j.chain...)}
			}
			err = g.add(Package{
				Name: pt.Name, Path: modPath, Repo: fetchRepo, ReplacedFrom: replacedFrom,
				Commit: commit, Tag: tagName, Tree: tree, Dir: pt.Root,
				Routes: []string{modPath}, Deps: deps, RequiredBy: chains,
				Direct: j.direct, Linker: pt.Pkg.Linker, Exclude: pt.Pkg.Exclude,
				Docs: pt.Pkg.Docs, Hooks: pt.Pkg.Hooks,
			}, alias)
			if err != nil {
				return nil, err
			}
		}

		if pt.Cmd != nil {
			if err := checkPkgHooks(pt.Cmd.Hooks); err != nil {
				return nil, fmt.Errorf("%s: %w", fetchRepo, err)
			}
			g.Commands[norm] = &Package{
				Name: pt.Name, Path: modPath, Repo: fetchRepo, ReplacedFrom: replacedFrom,
				Commit: commit, Tag: tagName, Tree: tree, Dir: dir,
				Kind: "cmd", Entry: pt.Cmd.Entry, Direct: j.direct,
			}
		}

		// A repository's dependencies are enqueued once, never per package:
		// otherwise A->B->A grows the queue forever (review blocker).
		walkKey := modPath + "@" + fetchRepo + "@" + commit + "@" + dir
		if !expanded[walkKey] {
			expanded[walkKey] = true
			nextChain := append(append([]string(nil), j.chain...), modPath)
			refs, err := pkgRefs(pt)
			if err != nil {
				return nil, err
			}
			for _, d := range refs {
				queue = append(queue, job{
					repo:           d.repo,
					ref:            d.ref,
					commit:         d.commit,
					majorHint:      d.majorHint,
					chain:          nextChain,
					parentPath:     modPath,
					depPlaceholder: d.placeholder,
				})
			}
		}
	}

	sort.Slice(g.Routes, func(i, j int) bool { return g.Routes[i].Path < g.Routes[j].Path })
	sort.Slice(g.Lock, func(i, j int) bool { return g.Lock[i].Name < g.Lock[j].Name })
	return g, nil
}

func (r *Resolver) resolveRemoteRef(norm, ref, commit string, majorHint int) (tagName, sha string, err error) {
	if commit != "" {
		return r.Store.ResolveRef(norm, commit)
	}
	if majorHint > 0 && (ref == "" || ref == "latest") {
		tags, err := r.Store.Tags(norm)
		if err != nil {
			return "", "", err
		}
		t, ok := tag.LatestInMajor(tags, majorHint)
		if !ok {
			return "", "", fmt.Errorf("%s has no v%d.x.y tags", norm, majorHint)
		}
		return r.Store.ResolveRef(norm, t)
	}
	if ref == "" {
		ref = "latest"
	}
	return r.Store.ResolveRef(norm, ref)
}

func (r *Resolver) checkout(norm, tagName, commit string) (dir, tree string, err error) {
	key := norm + "@" + commit
	dir, err = r.Store.Ensure(norm, tagName, commit, r.Locked[key])
	if err != nil {
		return "", "", err
	}
	tree, err = r.Store.TreeOf(dir)
	if err != nil {
		return "", "", err
	}
	return dir, tree, nil
}

// majorFromHEAD inspects git tags pointing at HEAD in dir and returns the
// highest major version >= 2, or 0 if dir is not a git repo or has no v2+ tag.
func majorFromHEAD(dir string) int {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return 0
	}
	out, err := exec.Command("git", "-C", dir, "tag", "--points-at", "HEAD").Output()
	if err != nil {
		return 0
	}
	best := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if m := tag.MajorOf(strings.TrimSpace(line)); m >= 2 && m > best {
			best = m
		}
	}
	return best
}

func replaceDep(parent *Package, oldPath, newPath string) {
	found := false
	for i, d := range parent.Deps {
		if d == oldPath {
			parent.Deps[i] = newPath
			found = true
		}
	}
	if !found {
		parent.Deps = append(parent.Deps, newPath)
	}
	sort.Strings(parent.Deps)
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

type depRef struct {
	repo        string
	ref         string
	commit      string
	majorHint   int
	placeholder string
}

// pkgRefs returns every repository dependency a tree declares (library and
// command), sorted deterministically by manifest key.
func pkgRefs(pt *pkg.Tree) ([]depRef, error) {
	var out []depRef
	collect := func(m map[string]pkg.DepSpec) error {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, rawKey := range keys {
			spec := m[rawKey]
			repoRaw, majorHint := urlkey.SplitDepKey(rawKey)
			norm, err := urlkey.Normalize(repoRaw)
			if err != nil {
				return err
			}
			major := tag.MajorOf(spec.Version)
			if major == 0 {
				major = majorHint
			}
			mp, err := urlkey.VersionedModulePath(norm, major)
			if err != nil {
				return err
			}
			out = append(out, depRef{
				repo:        repoRaw,
				ref:         spec.Version,
				commit:      spec.Commit,
				majorHint:   majorHint,
				placeholder: mp,
			})
		}
		return nil
	}
	if pt.Pkg != nil {
		if err := collect(pt.Pkg.DepsFrom); err != nil {
			return nil, err
		}
	}
	if pt.Cmd != nil {
		if err := collect(pt.Cmd.DepsFrom); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// depPaths turns the declared repository dependencies of a package into
// versioned module paths.
func depPaths(pt *pkg.Tree) ([]string, error) {
	refs, err := pkgRefs(pt)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(refs))
	for _, d := range refs {
		if !seen[d.placeholder] {
			seen[d.placeholder] = true
			out = append(out, d.placeholder)
		}
	}
	sort.Strings(out)
	return out, nil
}

// add registers a package, applies the project's alias and checks route
// conflicts (R1/R2/D69).
func (g *Graph) add(p Package, alias string) error {
	if existing, ok := g.Packages[p.Path]; ok {
		if existing.Repo == p.Repo && existing.Commit == p.Commit && existing.Dir == p.Dir {
			if p.Direct {
				existing.Direct = true
			}
			for _, c := range p.RequiredBy {
				existing.RequiredBy = appendChainUnique(existing.RequiredBy, c)
			}
			return nil // already merged
		}
		return conflictError(existing, &p)
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
			Tree: "git-tree:" + p.Tree, Alias: p.Alias, ReplacedFrom: p.ReplacedFrom,
		})
	}
	return nil
}

func appendChainUnique(chains [][]string, candidate []string) [][]string {
	key := strings.Join(candidate, "\x00")
	for _, c := range chains {
		if strings.Join(c, "\x00") == key {
			return chains
		}
	}
	return append(chains, candidate)
}

func conflictError(a, b *Package) error {
	var msg strings.Builder
	fmt.Fprintf(&msg, "conflict: %s is required at two different commits (D69):\n", b.Path)
	fmt.Fprintf(&msg, "  %s\n", describeConflictSide(a))
	fmt.Fprintf(&msg, "  %s\n", describeConflictSide(b))
	msg.WriteString("\nbojo does not silently upgrade dependencies. To pin one version, add to your root bojo.toml:\n\n")
	fmt.Fprintf(&msg, "  [override.%q]\n", b.Path)
	hintTag, hintCommit := pickHint(a, b)
	if hintTag != "" {
		fmt.Fprintf(&msg, "  version = %q", hintTag)
	} else {
		fmt.Fprintf(&msg, "  commit = %q", hintCommit)
	}
	return fmt.Errorf("%s", msg.String())
}

func describeConflictSide(p *Package) string {
	label := p.Repo
	switch {
	case p.Tag != "" && p.Commit != "":
		label = fmt.Sprintf("%s@%s (%s)", p.Repo, p.Tag, short(p.Commit))
	case p.Commit != "":
		label = fmt.Sprintf("%s@%s", p.Repo, short(p.Commit))
	case p.Dir != "":
		label = fmt.Sprintf("%s (local %s)", p.Repo, p.Dir)
	}
	if len(p.RequiredBy) > 0 && len(p.RequiredBy[0]) > 0 {
		return label + " via " + strings.Join(p.RequiredBy[0], " -> ")
	}
	return label
}

func pickHint(a, b *Package) (hintTag, hintCommit string) {
	va, errA := tag.ParseVersion(a.Tag)
	vb, errB := tag.ParseVersion(b.Tag)
	switch {
	case errA == nil && errB == nil:
		if vb.Compare(va) >= 0 {
			return b.Tag, b.Commit
		}
		return a.Tag, a.Commit
	case b.Tag != "":
		return b.Tag, b.Commit
	case a.Tag != "":
		return a.Tag, a.Commit
	case b.Commit != "":
		return "", b.Commit
	default:
		return "", a.Commit
	}
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

// LockedCommits returns the set of "<repo>@<commit>" pairs in the lock for the
// hook staleness check (D28). Keying by repo+commit lets v1 and v2 of the same
// repository coexist cleanly.
func (g *Graph) LockedCommits() map[string]bool {
	out := map[string]bool{}
	for _, e := range g.Lock {
		out[e.Repo+"@"+e.Commit] = true
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
