// Package pkg parses bojo.pkg.toml: one repository, at most one library and at
// most one command (DESIGN §5.2, D83).
//
// The repository name is the package name: it is derived from the repository
// URL, never declared. There is no import list and no in-repo dependency graph
// to walk — a repository is its package.
package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

// FileName is the only accepted name for a package file (D5).
const FileName = "bojo.pkg.toml"

// DirName is the fixed directory of a command package (D83).
const DirName = "cmd"

// EntryName is the fixed entry file of a command package.
const EntryName = "main.mojo"

// Repo is the repository-level block.
type Repo struct {
	URL     string `toml:"url"`
	License string `toml:"license"`
}

// Linker describes what a package needs at link time and at run time.
//
// Libs/LibDirs: names and directories handed to the linker.
// FFI: source files that belong to the native part of the package. They ship as
// source and are compiled by the consumer, never by the package itself.
// Runtime: data files that must travel with the finished binary (timezone
// tables, JSON, models). They are copied into the bundle, not into the module
// tree.
type Linker struct {
	Libs    []string `toml:"libs"`
	LibDirs []string `toml:"libdirs"`
	FFI     []string `toml:"ffi"`
	Runtime []string `toml:"runtime"`
}

// DepSpec is one repository dependency declared in bojo.pkg.toml:
//
//	[pkg.deps-from."https://github.com/bojolib/io"]
//	version = "v0.1.0"
//
// Or pinned to a commit:
//
//	[pkg.deps-from."https://github.com/bojolib/io"]
//	commit = "9f3a2b1c..."
//
// To depend on two major versions of the same repository, suffix the key with
// "@v<N>" (e.g. "https://github.com/bojolib/socket@v2").
type DepSpec struct {
	Version string `toml:"version"`
	Commit  string `toml:"commit"`
}

// Pkg is the body of `[pkg]` (a library) or `[cmd]` (a command).
type Pkg struct {
	Entry       string                `toml:"entry"`
	Exclude     []string              `toml:"exclude"`
	Description string                `toml:"description"`
	Keywords    []string              `toml:"keywords"`
	License     string                `toml:"license"`
	Homepage    string                `toml:"homepage"`
	Docs        string                `toml:"docs"`
	Readme      string                `toml:"readme"`
	DepsFrom    map[string]DepSpec    `toml:"deps-from"`
	Linker      Linker                `toml:"linker"`
	Hooks       map[string][][]string `toml:"hooks"`
}

// File is the parsed shape of one bojo.pkg.toml.
type File struct {
	Schema int  `toml:"schema"`
	Repo   Repo `toml:"repo"`
	Pkg    *Pkg `toml:"pkg"`
	Cmd    *Pkg `toml:"cmd"`
}

// Tree is the model of one repository.
type Tree struct {
	Root string
	Repo Repo
	// Name is the derived package name: the repository name as a Mojo
	// identifier (D83).
	Name string
	Pkg  *Pkg
	Cmd  *Pkg
}

// LoadTree reads the package file in dir and validates the layout.
func LoadTree(dir string) (*Tree, error) {
	path := filepath.Join(dir, FileName)
	f, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	if f.Repo.URL == "" {
		return nil, fmt.Errorf("%s: [repo] url is required", path)
	}
	name, err := urlkey.RepoName(f.Repo.URL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Pkg == nil && f.Cmd == nil {
		return nil, fmt.Errorf("%s: declare [pkg] (a library), [cmd] (a command) or both", path)
	}

	t := &Tree{Root: dir, Repo: f.Repo, Name: name, Pkg: f.Pkg, Cmd: f.Cmd}

	// A library lives at the repository root: its __init__.mojo is the module
	// entry. Where it keeps the rest is the author's decision (D83).
	if f.Pkg != nil {
		if _, err := os.Stat(filepath.Join(dir, "__init__.mojo")); err != nil {
			return nil, fmt.Errorf("%s: [pkg] needs __init__.mojo at the repository root", path)
		}
		if err := validateDeps(path, "pkg", f.Pkg.DepsFrom); err != nil {
			return nil, err
		}
	}
	if f.Cmd != nil {
		if f.Cmd.Entry == "" {
			f.Cmd.Entry = DirName + "/" + EntryName
		}
		entry := filepath.Clean(filepath.FromSlash(f.Cmd.Entry))
		if filepath.IsAbs(entry) || strings.HasPrefix(entry, "..") {
			return nil, fmt.Errorf("%s: [cmd] entry %q must stay inside the repository", path, f.Cmd.Entry)
		}
		if _, err := os.Stat(filepath.Join(dir, entry)); err != nil {
			return nil, fmt.Errorf("%s: [cmd] entry %q does not exist", path, f.Cmd.Entry)
		}
		if err := validateDeps(path, "cmd", f.Cmd.DepsFrom); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func validateDeps(path, section string, deps map[string]DepSpec) error {
	for rawKey, spec := range deps {
		repoRaw, majorHint := urlkey.SplitDepKey(rawKey)
		if _, err := urlkey.Normalize(repoRaw); err != nil {
			return fmt.Errorf("%s: [%s.deps-from.%q]: %w", path, section, rawKey, err)
		}
		if spec.Version != "" && spec.Commit != "" {
			return fmt.Errorf("%s: [%s.deps-from.%q]: set version or commit, not both", path, section, rawKey)
		}
		if majorHint > 0 && spec.Version != "" {
			if v, err := tag.ParseVersion(spec.Version); err == nil && v.Major != majorHint {
				return fmt.Errorf("%s: [%s.deps-from.%q]: key major v%d disagrees with version %q",
					path, section, rawKey, majorHint, spec.Version)
			}
		}
	}
	return nil
}

func parseFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s in %s (a package must declare what it is)", FileName, filepath.Dir(path))
		}
		return nil, err
	}
	var f File
	md, err := toml.Decode(string(raw), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, u)
	}
	if f.Schema != 1 {
		return nil, fmt.Errorf("%s: schema = 1 is required (got %d)", path, f.Schema)
	}
	return &f, nil
}
