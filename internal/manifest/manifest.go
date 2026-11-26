// Package manifest reads and validates bojo.toml (the consumer side).
//
// Only the fields bojo actually uses are modelled. Unknown fields are an error:
// a manifest that has drifted from the specification must fail loudly instead of
// being silently ignored.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

type Project struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Entry   string `toml:"entry"`
}

// Version declares what the project is built against (D47). There are no
// ranges: a range is a guess about the future, one version is a fact.
type Version struct {
	Mojo   string `toml:"mojo"`
	Python string `toml:"python"`
}

// Linker declares native libraries the project links against, e.g. a C library
// built by a pre-build hook (D57).
type Linker struct {
	LibDirs []string `toml:"libdirs"`
	Libs    []string `toml:"libs"`
}

type Build struct {
	Output  string   `toml:"output"`
	Strip   bool     `toml:"strip"`
	Include []string `toml:"include"`
}

// Dep is a local dependency: a directory relative to the project root.
type Dep struct {
	Path string `toml:"path"`
}

// DepSpec is one dependency: a version or commit, and an optional local alias.
type DepSpec struct {
	Version string
	Commit  string
	Alias   string
}

// UnmarshalTOML accepts "v1.2.3" or { version = "v1.2.3", commit = "...", as = "name" }.
func (d *DepSpec) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		d.Version = t
	case map[string]any:
		for k, val := range t {
			s, ok := val.(string)
			if !ok {
				return fmt.Errorf("dependency field %q must be a string", k)
			}
			switch k {
			case "version":
				d.Version = s
			case "commit":
				d.Commit = s
			case "as":
				d.Alias = s
			default:
				return fmt.Errorf("unknown dependency field %q", k)
			}
		}
	default:
		return fmt.Errorf("a dependency must be a version string or a table")
	}
	return nil
}

// OverrideSpec pins an existing module path to a chosen version or commit
// across the entire dependency graph. Only allowed in the root bojo.toml.
type OverrideSpec struct {
	Version string `toml:"version"`
	Commit  string `toml:"commit"`
}

// ReplaceSpec redirects a module path to a fork repository or a local
// directory while preserving the original import path. Only allowed in the
// root bojo.toml.
type ReplaceSpec struct {
	Repo    string `toml:"repo"`
	Version string `toml:"version"`
	Commit  string `toml:"commit"`
	Path    string `toml:"path"`
}

// DepsFrom maps a repository URL to its version: repo -> spec. One repository
// is one package, so there is no package name in the key (D83).
type DepsFrom map[string]DepSpec

// DepFrom is one repository at one version, providing one or more packages.
type DepFrom struct {
	Repo   string            `toml:"repo"`
	Tag    string            `toml:"tag"`
	Commit string            `toml:"commit"`
	Libs   []string          `toml:"libs"`
	Alias  map[string]string `toml:"alias"`
}

// DepsTable is `[deps]`: local paths under `[deps.<name>]` plus the Python
// package list under `[deps.python]` (D50). Both live in one TOML table, so the
// table is decoded by hand.
type DepsTable struct {
	Local  map[string]Dep
	Python map[string]string
}

// UnmarshalTOML splits the shared table into local and Python entries.
func (d *DepsTable) UnmarshalTOML(v interface{}) error {
	raw, ok := v.(map[string]interface{})
	if !ok {
		return fmt.Errorf("[deps] must be a table")
	}
	d.Local = map[string]Dep{}
	d.Python = map[string]string{}
	for name, entry := range raw {
		table, ok := entry.(map[string]interface{})
		if !ok {
			return fmt.Errorf("[deps.%s] must be a table", name)
		}
		if name == "python" {
			for pkgName, value := range table {
				version, ok := value.(string)
				if !ok {
					return fmt.Errorf("[deps.python] %s must be a string", pkgName)
				}
				d.Python[pkgName] = version
			}
			continue
		}
		path, _ := table["path"].(string)
		d.Local[name] = Dep{Path: path}
	}
	return nil
}

type File struct {
	Schema   int                   `toml:"schema"`
	Project  Project               `toml:"project"`
	Version  Version               `toml:"version"`
	Build    Build                 `toml:"build"`
	Deps     DepsTable             `toml:"deps"`
	DepsFrom DepsFrom              `toml:"deps-from"`
	Hooks    map[string][][]string `toml:"hooks"`
	// Alias maps a module path to a short name the project may import: only the
	// project may alias, never a library (D68). Keying by path, not by name, is
	// what makes two repositories with the same name distinguishable.
	Alias map[string]string `toml:"alias"`
	// Override pins a module path to one version/commit across the graph to
	// resolve diamond conflicts explicitly (root bojo.toml only).
	Override map[string]OverrideSpec `toml:"override"`
	// Replace redirects a module path to a fork repository or local directory
	// while keeping the original import path (root bojo.toml only).
	Replace map[string]ReplaceSpec `toml:"replace"`
	Linker  Linker                 `toml:"linker"`
}

// Load reads bojo.toml from dir and validates it.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, "bojo.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no bojo.toml in %s (run `bojo init`)", dir)
		}
		return nil, err
	}

	var f File
	md, err := toml.Decode(string(raw), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		if strings.HasPrefix(k.String(), "deps.") || strings.HasPrefix(k.String(), "deps-from.") {
			continue // handled by the custom unmarshalers
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, unknown)
	}

	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Validate checks every field and table of a consumer bojo.toml.
func (f *File) Validate() error {
	if f.Schema != 0 && f.Schema != 1 {
		return fmt.Errorf("unsupported schema %d (this bojo reads schema 1)", f.Schema)
	}
	if f.Project.Name == "" {
		return fmt.Errorf("[project] name is required")
	}
	if f.Project.Entry == "" {
		return fmt.Errorf("[project] entry is required")
	}
	switch f.Build.Output {
	case "", "dir", "onefile":
	default:
		return fmt.Errorf("[build] output must be \"dir\" or \"onefile\", got %q", f.Build.Output)
	}
	if len(f.Deps.Python) > 0 && f.Version.Python == "" {
		return fmt.Errorf("deps.python needs [version] python")
	}
	for name, d := range f.Deps.Local {
		if d.Path == "" {
			return fmt.Errorf("deps.%s needs a path", name)
		}
	}

	for rawKey, spec := range f.DepsFrom {
		if strings.TrimSpace(rawKey) == "" {
			return fmt.Errorf("a deps-from entry needs a repository")
		}
		repoRaw, majorHint := urlkey.SplitDepKey(rawKey)
		if _, err := urlkey.Normalize(repoRaw); err != nil {
			return fmt.Errorf("deps-from.%q: %w", rawKey, err)
		}
		if spec.Version != "" && spec.Commit != "" {
			return fmt.Errorf("deps-from.%q: set version or commit, not both", rawKey)
		}
		if majorHint > 0 && spec.Version != "" {
			if v, err := tag.ParseVersion(spec.Version); err == nil && v.Major != majorHint {
				return fmt.Errorf("deps-from.%q: key major v%d disagrees with version %q", rawKey, majorHint, spec.Version)
			}
		}
		if spec.Alias != "" && strings.ContainsAny(spec.Alias, " \t/.\\:") {
			return fmt.Errorf("alias %q for %s is not a plain name", spec.Alias, rawKey)
		}
	}

	for modulePath, alias := range f.Alias {
		if err := validateModulePathKey("alias", modulePath); err != nil {
			return err
		}
		if alias == "" || strings.ContainsAny(alias, " \t/.\\:") {
			return fmt.Errorf("alias %q is not a plain name", alias)
		}
	}

	for modPath, spec := range f.Override {
		if err := validateModulePathKey("override", modPath); err != nil {
			return err
		}
		if spec.Version == "" && spec.Commit == "" {
			return fmt.Errorf("override.%q needs version or commit", modPath)
		}
		if spec.Version != "" && spec.Commit != "" {
			return fmt.Errorf("override.%q cannot set both version and commit", modPath)
		}
		if spec.Version != "" {
			modMajor, _ := urlkey.ParseModulePath(modPath)
			if v, err := tag.ParseVersion(spec.Version); err == nil {
				if modMajor >= 2 && v.Major != modMajor {
					return fmt.Errorf("override.%q: version %q does not match major v%d", modPath, spec.Version, modMajor)
				}
				if modMajor == 0 && v.Major >= 2 {
					return fmt.Errorf("override.%q: version %q requires major path prefix v%d.%s", modPath, spec.Version, v.Major, modPath)
				}
			}
		}
	}

	for modPath, spec := range f.Replace {
		if err := validateModulePathKey("replace", modPath); err != nil {
			return err
		}
		if _, dup := f.Override[modPath]; dup {
			return fmt.Errorf("%q cannot appear in both [override] and [replace]", modPath)
		}
		hasPath := strings.TrimSpace(spec.Path) != ""
		hasRepo := strings.TrimSpace(spec.Repo) != ""
		if !hasPath && !hasRepo {
			return fmt.Errorf("replace.%q needs repo or path", modPath)
		}
		if hasPath && hasRepo {
			return fmt.Errorf("replace.%q cannot set both repo and path", modPath)
		}
		if hasPath {
			if spec.Version != "" || spec.Commit != "" {
				return fmt.Errorf("replace.%q with local path cannot set version or commit", modPath)
			}
			continue
		}
		if _, err := urlkey.Normalize(spec.Repo); err != nil {
			return fmt.Errorf("replace.%q: %w", modPath, err)
		}
		if spec.Version == "" && spec.Commit == "" {
			return fmt.Errorf("replace.%q with repo needs version or commit", modPath)
		}
		if spec.Version != "" && spec.Commit != "" {
			return fmt.Errorf("replace.%q cannot set both version and commit", modPath)
		}
	}

	return nil
}

func validateModulePathKey(section, modPath string) error {
	s := strings.TrimSpace(modPath)
	if s == "" || s != modPath {
		return fmt.Errorf("a [%s] entry needs a module path", section)
	}
	if strings.ContainsAny(s, " \t/\\:") || !strings.Contains(s, ".") {
		return fmt.Errorf("[%s] key %q is not a valid module path (example: gh.bojolib.io)", section, modPath)
	}
	return nil
}
