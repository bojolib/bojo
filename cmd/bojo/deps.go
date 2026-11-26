package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/manifest"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/urlkey"
)

// cmdDeps is the dependency front end (D50):
//
//	bojo deps list
//	bojo deps add <url>@<ver> [--as <name>]
//	bojo deps add-python <name>[@<version>]
//	bojo deps check            (not implemented yet: newer versions, CVEs)
func cmdDeps(args []string) int {
	if len(args) == 0 || args[0] == "list" {
		return depsList()
	}
	switch args[0] {
	case "add":
		return cmdGet(args[1:])
	case "add-python":
		return depsAddPython(args[1:])
	case "check":
		fail(errors.New("deps check is not implemented yet (newer versions and CVEs)"))
		return exitUsage
	default:
		fail(fmt.Errorf("unknown deps command %q (list|add|add-python|check)", args[0]))
		return exitUsage
	}
}

func depsList() int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	cfg, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}

	fmt.Printf("mojo   %s\n", orNone(cfg.Version.Mojo))
	if cfg.Version.Python != "" {
		fmt.Printf("python %s\n", cfg.Version.Python)
	}
	fmt.Println()

	if len(cfg.DepsFrom) == 0 && len(cfg.Deps.Local) == 0 && len(cfg.Deps.Python) == 0 {
		fmt.Println("no dependencies")
		return exitOK
	}
	depKeys := make([]string, 0, len(cfg.DepsFrom))
	for k := range cfg.DepsFrom {
		depKeys = append(depKeys, k)
	}
	sort.Strings(depKeys)
	for _, rawKey := range depKeys {
		spec := cfg.DepsFrom[rawKey]
		repo, majorHint := urlkey.SplitDepKey(rawKey)
		ver := spec.Version
		if ver == "" && spec.Commit != "" {
			ver = shortSHA(spec.Commit)
		}
		if ver == "" {
			ver = "latest"
		}
		major := tag.MajorOf(spec.Version)
		if major == 0 {
			major = majorHint
		}
		name := repo
		if norm, err := urlkey.Normalize(repo); err == nil {
			if mp, err := urlkey.VersionedModulePath(norm, major); err == nil {
				name = mp
			}
		}
		fmt.Printf("pkg    %s  %s  %s\n", name, repo, ver)
	}
	localKeys := make([]string, 0, len(cfg.Deps.Local))
	for k := range cfg.Deps.Local {
		localKeys = append(localKeys, k)
	}
	sort.Strings(localKeys)
	for _, name := range localKeys {
		fmt.Printf("local  %s  %s\n", name, cfg.Deps.Local[name].Path)
	}
	pyKeys := make([]string, 0, len(cfg.Deps.Python))
	for k := range cfg.Deps.Python {
		pyKeys = append(pyKeys, k)
	}
	sort.Strings(pyKeys)
	for _, name := range pyKeys {
		fmt.Printf("python %s  %s\n", name, cfg.Deps.Python[name])
	}
	if len(cfg.Override) > 0 {
		keys := make([]string, 0, len(cfg.Override))
		for k := range cfg.Override {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ov := cfg.Override[k]
			val := ov.Version
			if val == "" {
				val = shortSHA(ov.Commit)
			}
			fmt.Printf("override %s  %s\n", k, val)
		}
	}
	if len(cfg.Replace) > 0 {
		keys := make([]string, 0, len(cfg.Replace))
		for k := range cfg.Replace {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rep := cfg.Replace[k]
			if rep.Path != "" {
				fmt.Printf("replace  %s  path:%s\n", k, rep.Path)
				continue
			}
			val := rep.Version
			if val == "" {
				val = shortSHA(rep.Commit)
			}
			fmt.Printf("replace  %s  %s@%s\n", k, rep.Repo, val)
		}
	}
	return exitOK
}

var pythonName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// depsAddPython records a Python package in [deps.python].
//
// A Python package name can never contain '@', so the single '@' is an
// unambiguous separator. A leading 'v' in the version is dropped because pip
// versions do not carry one.
func depsAddPython(args []string) int {
	if len(args) != 1 {
		fail(errors.New("usage: bojo deps add-python <name>[@<version>]"))
		return exitUsage
	}
	name, version := args[0], "*"
	if at := strings.Index(args[0], "@"); at >= 0 {
		name, version = args[0][:at], args[0][at+1:]
	}
	if !pythonName.MatchString(name) {
		fail(fmt.Errorf("%q is not a valid Python package name", name))
		return exitUsage
	}
	if version == "" {
		version = "*"
	}
	version = strings.TrimPrefix(version, "v")

	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	if _, err := manifest.Load(dir); err != nil {
		fail(err)
		return exitUsage
	}
	path := filepath.Join(dir, "bojo.toml")

	// [deps.python] without [version] python is a broken manifest, so make sure
	// the interpreter is declared first.
	if err := upsertTOMLKey(path, "version", "python", "3.12"); err != nil {
		fail(err)
		return exitUsage
	}
	if err := upsertTOMLKey(path, "deps.python", name, version); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("added python package %s %s\n", name, version)
	fmt.Println("next: bojo deps list")
	return exitOK
}

// upsertTOMLKey sets `key = "value"` inside [section], creating the section or
// the key when needed. It edits the file textually so comments survive.
func upsertTOMLKey(path, section, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	header := "[" + section + "]"
	keyLine := fmt.Sprintf("%s = %q", key, value)

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	if start < 0 {
		text := strings.TrimRight(string(raw), "\n")
		text += "\n\n" + header + "\n" + keyLine + "\n"
		return os.WriteFile(path, []byte(text), 0o644)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=") {
			lines[i] = keyLine
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}
	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	lines = append(lines[:insertAt], append([]string{keyLine}, lines[insertAt:]...)...)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}
