package libverify

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bojolib/bojo/internal/pkg"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

type rawManifest struct {
	Schema int `toml:"schema"`
	Repo   struct {
		URL     string `toml:"url"`
		License string `toml:"license"`
	} `toml:"repo"`
	Pkg *struct {
		Description string                `toml:"description"`
		Exclude     []string              `toml:"exclude"`
		Hooks       map[string][][]string `toml:"hooks"`
		DepsFrom    any                   `toml:"deps-from"`
	} `toml:"pkg"`
	Cmd any `toml:"cmd"`
}

// CheckStatic runs fast, static-only verification checks with zero
// subprocesses, making it safe to execute automatically on `bojo get` /
// `bojo vendor`.
//
// It checks:
//  1. Manifest structure (`schema == 1`, valid `[repo] url`, `[pkg]` with
//     `__init__.mojo`, hooks restricted to `vendor` only).
//  2. Import purity across all non-excluded `*.mojo` files (skipping `_tests/`
//     and `cmd/`), forbidding bare/alias/Pixi imports and undeclared `bojo`
//     imports.
func CheckStatic(dir, modPath string, pt *pkg.Tree) []Issue {
	return CheckStaticWithDeps(dir, modPath, pt, nil)
}

// CheckStaticWithDeps runs CheckStatic and merges any caller-supplied DepRef
// entries into the allowed dependency set.
func CheckStaticWithDeps(dir, modPath string, pt *pkg.Tree, extraDeps []DepRef) []Issue {
	var issues []Issue

	manifestPath := filepath.Join(dir, pkg.FileName)
	raw, err := parseRawManifest(manifestPath)
	if err != nil {
		issues = append(issues, Issue{
			Check:    "manifest",
			Severity: SeverityError,
			File:     pkg.FileName,
			Message:  err.Error(),
		})
		return issues
	}

	// 1. Schema version must be 1.
	if raw.Schema != 1 {
		issues = append(issues, Issue{
			Check:    "manifest",
			Severity: SeverityError,
			File:     pkg.FileName,
			Message:  fmt.Sprintf("schema must be 1 (got %d)", raw.Schema),
		})
	}

	// 2. [repo] url must be present, valid, and normalizable.
	repoURL := strings.TrimSpace(raw.Repo.URL)
	if pt != nil && strings.TrimSpace(pt.Repo.URL) != "" {
		repoURL = strings.TrimSpace(pt.Repo.URL)
	}
	if repoURL == "" {
		issues = append(issues, Issue{
			Check:    "manifest",
			Severity: SeverityError,
			File:     pkg.FileName,
			Message:  "[repo] url is required",
		})
	} else {
		norm, err := urlkey.Normalize(repoURL)
		if err != nil {
			issues = append(issues, Issue{
				Check:    "manifest",
				Severity: SeverityError,
				File:     pkg.FileName,
				Message:  fmt.Sprintf("invalid [repo] url %q: %v", repoURL, err),
			})
		} else if derived, err := urlkey.ModulePath(norm); err != nil {
			issues = append(issues, Issue{
				Check:    "manifest",
				Severity: SeverityError,
				File:     pkg.FileName,
				Message:  fmt.Sprintf("cannot derive module path from [repo] url %q: %v", repoURL, err),
			})
		} else if modPath == "" {
			modPath = derived
		}
	}

	// 3. [pkg] block and root __init__.mojo must be present for libraries.
	hasPkg := raw.Pkg != nil || (pt != nil && pt.Pkg != nil)
	if !hasPkg {
		issues = append(issues, Issue{
			Check:    "manifest",
			Severity: SeverityError,
			File:     pkg.FileName,
			Message:  "[pkg] section is required for a library",
		})
		return issues
	}

	initPath := filepath.Join(dir, "__init__.mojo")
	if info, err := os.Stat(initPath); err != nil || info.IsDir() {
		issues = append(issues, Issue{
			Check:    "manifest",
			Severity: SeverityError,
			File:     "__init__.mojo",
			Message:  "library requires __init__.mojo at repository root",
		})
	}

	// 4. Hooks: libraries may only declare the `vendor` hook (never pre-build/post-build).
	var hooks map[string][][]string
	if pt != nil && pt.Pkg != nil && len(pt.Pkg.Hooks) > 0 {
		hooks = pt.Pkg.Hooks
	} else if raw.Pkg != nil {
		hooks = raw.Pkg.Hooks
	}
	if len(hooks) > 0 {
		hookNames := make([]string, 0, len(hooks))
		for k := range hooks {
			hookNames = append(hookNames, k)
		}
		sort.Strings(hookNames)
		for _, k := range hookNames {
			if k != "vendor" {
				issues = append(issues, Issue{
					Check:    "manifest",
					Severity: SeverityError,
					File:     pkg.FileName,
					Message:  fmt.Sprintf("forbidden [pkg.hooks] key %q (libraries may only declare \"vendor\" hooks)", k),
				})
			}
		}
	}

	// 5. Collect declared dependencies and compute allowed import module paths.
	deps := extractDepsFromRaw(raw)
	deps = append(deps, extraDeps...)
	allowedDeps := make(map[string]bool, len(deps))
	for _, d := range deps {
		if strings.TrimSpace(d.Repo) == "" {
			issues = append(issues, Issue{
				Check:    "manifest",
				Severity: SeverityError,
				File:     pkg.FileName,
				Message:  "[pkg.deps-from] entry is missing repository URL",
			})
			continue
		}
		depMod, err := ModulePathForVersion(d.Repo, d.Version)
		if err != nil {
			issues = append(issues, Issue{
				Check:    "manifest",
				Severity: SeverityError,
				File:     pkg.FileName,
				Message:  fmt.Sprintf("invalid [pkg.deps-from] repo %q: %v", d.Repo, err),
			})
			continue
		}
		allowedDeps[depMod] = true
	}

	// 6. Walk non-excluded *.mojo files (skipping _tests/ and cmd/) and check import purity.
	var exclude []string
	if pt != nil && pt.Pkg != nil {
		exclude = pt.Pkg.Exclude
	} else if raw.Pkg != nil {
		exclude = raw.Pkg.Exclude
	}

	var mojoFiles []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".bojo" || name == ".vendor" || name == ".out" ||
				name == "target" || name == "_tests" || name == "cmd" {
				return filepath.SkipDir
			}
			if matchesExclude(slashRel, exclude) || matchesExclude(slashRel+"/", exclude) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".mojo") {
			return nil
		}
		if matchesExclude(slashRel, exclude) {
			return nil
		}
		mojoFiles = append(mojoFiles, slashRel)
		return nil
	})
	if walkErr != nil {
		issues = append(issues, Issue{
			Check:    "purity",
			Severity: SeverityError,
			Message:  fmt.Sprintf("scanning library files: %v", walkErr),
		})
		return issues
	}

	sort.Strings(mojoFiles)
	for _, relFile := range mojoFiles {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(relFile)))
		if err != nil {
			issues = append(issues, Issue{
				Check:    "purity",
				Severity: SeverityError,
				File:     relFile,
				Message:  fmt.Sprintf("reading file: %v", err),
			})
			continue
		}
		src := string(data)
		issues = append(issues, CheckFilePurity(relFile, src, modPath, allowedDeps)...)
		if relFile == "__init__.mojo" {
			issues = append(issues, CheckInitImportsOnly(relFile, src)...)
		}
	}

	return issues
}

// LoadDeps reads declared `[pkg.deps-from]` entries from `<dir>/bojo.pkg.toml`,
// supporting both the map table form `[pkg.deps-from."<url>"]` and the legacy
// array-of-tables form `[[pkg.deps-from]]`.
func LoadDeps(dir string) ([]DepRef, error) {
	raw, err := parseRawManifest(filepath.Join(dir, pkg.FileName))
	if err != nil {
		return nil, err
	}
	return extractDepsFromRaw(raw), nil
}

// ModulePathForVersion computes the Mojo import path for a repository URL at
// the given version: `gh.<owner>.<repo>` for v0.x/v1.x (or empty version), and
// `v<N>.gh.<owner>.<repo>` for major version v2.0.0+.
func ModulePathForVersion(repoURL, version string) (string, error) {
	norm, err := urlkey.Normalize(repoURL)
	if err != nil {
		return "", err
	}
	base, err := urlkey.ModulePath(norm)
	if err != nil {
		return "", err
	}
	vStr := strings.TrimSpace(version)
	if vStr == "" {
		return base, nil
	}
	if !strings.HasPrefix(vStr, "v") {
		vStr = "v" + vStr
	}
	v, err := tag.ParseVersion(vStr)
	if err != nil {
		return base, nil
	}
	if v.Major >= 2 {
		return fmt.Sprintf("v%d.%s", v.Major, base), nil
	}
	return base, nil
}

func parseRawManifest(path string) (*rawManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", pkg.FileName, err)
	}
	var m rawManifest
	if _, err := toml.Decode(string(data), &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", pkg.FileName, err)
	}
	return &m, nil
}

func extractDepsFromRaw(m *rawManifest) []DepRef {
	if m == nil || m.Pkg == nil || m.Pkg.DepsFrom == nil {
		return nil
	}
	var deps []DepRef
	switch v := m.Pkg.DepsFrom.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, repo := range keys {
			entry := v[repo]
			dep := DepRef{Repo: repo}
			switch spec := entry.(type) {
			case map[string]any:
				dep.Version = firstString(spec, "version", "tag")
				dep.Commit = firstString(spec, "commit")
			case string:
				dep.Version = spec
			}
			deps = append(deps, dep)
		}
	case []map[string]any:
		for _, item := range v {
			deps = append(deps, DepRef{
				Repo:    firstString(item, "repo"),
				Version: firstString(item, "version", "tag"),
				Commit:  firstString(item, "commit"),
			})
		}
	case []any:
		for _, rawItem := range v {
			if item, ok := rawItem.(map[string]any); ok {
				deps = append(deps, DepRef{
					Repo:    firstString(item, "repo"),
					Version: firstString(item, "version", "tag"),
					Commit:  firstString(item, "commit"),
				})
			}
		}
	}
	return deps
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func matchesExclude(rel string, patterns []string) bool {
	rel = strings.TrimPrefix(rel, "./")
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		if strings.HasSuffix(pat, "/**") {
			prefix := strings.TrimSuffix(pat, "/**")
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				return true
			}
		}
		if strings.HasPrefix(pat, "**/") {
			suffix := strings.TrimPrefix(pat, "**/")
			if rel == suffix || strings.HasSuffix(rel, "/"+suffix) {
				return true
			}
		}
	}
	return false
}
