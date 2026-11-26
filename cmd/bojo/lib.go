package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/apidoc"
	"github.com/bojolib/bojo/internal/cache"
	"github.com/bojolib/bojo/internal/libverify"
	"github.com/bojolib/bojo/internal/manifest"
	"github.com/bojolib/bojo/internal/pages"
	"github.com/bojolib/bojo/internal/pkg"
	"github.com/bojolib/bojo/internal/resolve"
	"github.com/bojolib/bojo/internal/tag"
	"github.com/bojolib/bojo/internal/toolchain"
	"github.com/bojolib/bojo/internal/urlkey"
	"github.com/bojolib/bojo/internal/vendor"
)

// ---------------------------------------------------------------------------
// bojo lib verify | api | pages (D90–D92)

func cmdLib(args []string) int {
	if len(args) == 0 {
		fail(errors.New("usage: bojo lib <verify|api|pages> [flags]"))
		return exitUsage
	}
	switch args[0] {
	case "verify":
		return cmdLibVerify(args[1:])
	case "api":
		return cmdLibAPI(args[1:])
	case "pages":
		return cmdLibPages(args[1:])
	default:
		fail(fmt.Errorf("unknown lib command %q (verify|api|pages)", args[0]))
		return exitUsage
	}
}

func libRoot() (string, *pkg.Tree, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", nil, err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, pkg.FileName)); err == nil {
			pt, err := pkg.LoadTree(dir)
			return dir, pt, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil, fmt.Errorf("no %s found in this directory or any parent", pkg.FileName)
		}
		dir = parent
	}
}

func currentOrLatestTag(dir, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	// Prefer a tag on HEAD if present.
	out, err := exec.Command("git", "-C", dir, "tag", "--points-at", "HEAD").Output()
	if err == nil {
		var headTags []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if t := strings.TrimSpace(line); t != "" {
				headTags = append(headTags, t)
			}
		}
		if latest, err := tag.Latest(headTags); err == nil && latest.Raw != "" {
			return latest.Raw
		}
	}
	// Otherwise fall back to the highest semver tag in the repo.
	out, err = exec.Command("git", "-C", dir, "tag", "-l").Output()
	if err == nil {
		var allTags []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if t := strings.TrimSpace(line); t != "" {
				allTags = append(allTags, t)
			}
		}
		if latest, err := tag.Latest(allTags); err == nil && latest.Raw != "" {
			return latest.Raw
		}
	}
	return ""
}

// resolveLibDepIncludes materializes any `[pkg.deps-from]` dependencies into a
// temporary module root so `mojo doc`, `probe+tests`, and `pages` work when a
// library imports external bojo libraries.
func resolveLibDepIncludes(dir string, pt *pkg.Tree) ([]string, func(), error) {
	noop := func() {}
	if pt == nil || pt.Pkg == nil || len(pt.Pkg.DepsFrom) == 0 {
		return nil, noop, nil
	}
	tmp, err := os.MkdirTemp("", "bojo-lib-deps-*")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	cfg := &manifest.File{
		Schema:   1,
		Project:  manifest.Project{Name: "lib-deps", Version: "0.0.0", Entry: "main.mojo"},
		DepsFrom: map[string]manifest.DepSpec{},
	}
	for k, d := range pt.Pkg.DepsFrom {
		cfg.DepsFrom[k] = manifest.DepSpec{Version: d.Version, Commit: d.Commit}
	}
	store := cache.New(tmp)
	r := &resolve.Resolver{Store: store}
	g, err := r.Resolve(cfg)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	if _, err := vendor.Build(tmp, g); err != nil {
		cleanup()
		return nil, noop, err
	}
	return vendor.Roots(tmp), cleanup, nil
}

func cmdLibVerify(args []string) int {
	var (
		mojoVer        string
		targetVer      string
		strictOutdated bool
		strictBreaking bool
		skipOutdated   bool
		skipProbe      bool
		skipAPI        bool
		skipToolchain  bool
		asJSON         bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mojo":
			if i+1 >= len(args) {
				fail(errors.New("--mojo requires a version"))
				return exitUsage
			}
			mojoVer = args[i+1]
			i++
		case "--version":
			if i+1 >= len(args) {
				fail(errors.New("--version requires a semver tag"))
				return exitUsage
			}
			targetVer = args[i+1]
			i++
		case "--strict-outdated":
			strictOutdated = true
		case "--strict-breaking":
			strictBreaking = true
		case "--skip-outdated":
			skipOutdated = true
		case "--skip-probe":
			skipProbe = true
		case "--skip-api":
			skipAPI = true
		case "--skip-toolchain":
			skipToolchain = true
		case "--json":
			asJSON = true
		default:
			fail(fmt.Errorf("unknown flag %q", args[i]))
			return exitUsage
		}
	}

	dir, pt, err := libRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}

	includes, cleanup, err := resolveLibDepIncludes(dir, pt)
	if err != nil {
		fail(fmt.Errorf("resolving library dependencies: %w", err))
		return resolveExit(err)
	}
	defer cleanup()

	ver := currentOrLatestTag(dir, targetVer)
	opts := libverify.Options{
		Dir:            dir,
		Version:        ver,
		MojoVersion:    mojoVer,
		IncludeDirs:    includes,
		StrictOutdated: strictOutdated,
		StrictBreaking: strictBreaking,
		SkipOutdated:   skipOutdated,
		SkipProbe:      skipProbe,
		SkipAPI:        skipAPI,
		SkipToolchain:  skipToolchain,
	}
	report := libverify.Verify(opts)

	if asJSON {
		raw, err := report.FormatJSON()
		if err != nil {
			fail(err)
			return exitUsage
		}
		fmt.Print(string(raw))
	} else {
		fmt.Print(report.FormatText())
	}
	if !report.OK {
		return exitBuild
	}
	return exitOK
}

func extractLibAPIDoc(dir string, pt *pkg.Tree, mojoVer, targetVer string) (*apidoc.Doc, string, string, error) {
	if pt.Pkg == nil {
		return nil, "", "", fmt.Errorf("%s has no [pkg] section", pkg.FileName)
	}
	mojoBin, err := toolchain.Resolve(mojoVer)
	if err != nil {
		return nil, "", "", err
	}
	ver := currentOrLatestTag(dir, targetVer)
	norm, err := urlkey.Normalize(pt.Repo.URL)
	if err != nil {
		return nil, "", "", err
	}
	modPath, err := urlkey.VersionedModulePath(norm, tag.MajorOf(ver))
	if err != nil {
		return nil, "", "", err
	}

	depIncludes, depCleanup, err := resolveLibDepIncludes(dir, pt)
	if err != nil {
		return nil, "", "", err
	}
	defer depCleanup()

	tmp, err := os.MkdirTemp("", "bojo-lib-apidoc-*")
	if err != nil {
		return nil, "", "", err
	}
	defer os.RemoveAll(tmp)

	modulesRoot := filepath.Join(tmp, "modules")
	pkgDir := filepath.Join(modulesRoot, filepath.FromSlash(strings.ReplaceAll(modPath, ".", "/")))
	if err := libverify.MaterializePackage(dir, pkgDir, pt.Pkg.Exclude); err != nil {
		return nil, "", "", err
	}

	includes := append([]string{modulesRoot}, depIncludes...)
	doc, err := apidoc.Extract(mojoBin, pkgDir, includes)
	if err != nil {
		return nil, "", "", err
	}
	return doc, modPath, ver, nil
}

func cmdLibAPI(args []string) int {
	var (
		mojoVer   string
		targetVer string
		toStdout  bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--write":
			toStdout = false
		case "--stdout":
			toStdout = true
		case "--mojo":
			if i+1 >= len(args) {
				fail(errors.New("--mojo requires a version"))
				return exitUsage
			}
			mojoVer = args[i+1]
			i++
		case "--version":
			if i+1 >= len(args) {
				fail(errors.New("--version requires a semver tag"))
				return exitUsage
			}
			targetVer = args[i+1]
			i++
		default:
			fail(fmt.Errorf("unknown flag %q", args[i]))
			return exitUsage
		}
	}

	dir, pt, err := libRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	doc, modPath, _, err := extractLibAPIDoc(dir, pt, mojoVer, targetVer)
	if err != nil {
		fail(err)
		return exitBuild
	}

	md := apidoc.RenderMarkdown(doc, apidoc.Meta{
		ModulePath:  modPath,
		RepoURL:     pt.Repo.URL,
		Description: pt.Pkg.Description,
	})
	if toStdout {
		fmt.Print(md)
		return exitOK
	}
	outPath := filepath.Join(dir, "API.md")
	if err := os.WriteFile(outPath, []byte(md), 0o644); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("wrote %s\n", outPath)
	return exitOK
}

func cmdLibPages(args []string) int {
	var (
		outDir    = "_site"
		mojoVer   string
		targetVer string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--out":
			if i+1 >= len(args) {
				fail(errors.New("--out requires a directory path"))
				return exitUsage
			}
			outDir = args[i+1]
			i++
		case "--mojo":
			if i+1 >= len(args) {
				fail(errors.New("--mojo requires a version"))
				return exitUsage
			}
			mojoVer = args[i+1]
			i++
		case "--version":
			if i+1 >= len(args) {
				fail(errors.New("--version requires a semver tag"))
				return exitUsage
			}
			targetVer = args[i+1]
			i++
		default:
			fail(fmt.Errorf("unknown flag %q", args[i]))
			return exitUsage
		}
	}

	dir, pt, err := libRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	doc, modPath, ver, err := extractLibAPIDoc(dir, pt, mojoVer, targetVer)
	if err != nil {
		fail(err)
		return exitBuild
	}

	pkgName := ""
	if norm, err := urlkey.Normalize(pt.Repo.URL); err == nil {
		if name, err := urlkey.RepoName(norm); err == nil {
			pkgName = name
		}
	}

	readmePath := filepath.Join(dir, "README.md")
	if pt.Pkg.Readme != "" {
		readmePath = filepath.Join(dir, pt.Pkg.Readme)
	}
	docsPath := filepath.Join(dir, "docs")
	if pt.Pkg.Docs != "" {
		docsPath = filepath.Join(dir, pt.Pkg.Docs)
	}
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(dir, outDir)
	}

	opts := pages.Options{
		OutDir:      outDir,
		PackageName: pkgName,
		ModulePath:  modPath,
		RepoURL:     pt.Repo.URL,
		Version:     ver,
		Description: pt.Pkg.Description,
		License:     pt.Repo.License,
		Keywords:    pt.Pkg.Keywords,
		ReadmePath:  readmePath,
		DocsDir:     docsPath,
		Doc:         doc,
	}
	if err := pages.Generate(opts); err != nil {
		fail(err)
		return exitBuild
	}
	fmt.Printf("wrote pages to %s\n", outDir)
	return exitOK
}

// verifyPulledGraph runs fast, static-only checks (D90) on every resolved
// package in the graph without executing any foreign code or subprocesses.
func verifyPulledGraph(g *resolve.Graph) error {
	if g == nil || len(g.Packages) == 0 {
		return nil
	}
	keys := make([]string, 0, len(g.Packages))
	for k := range g.Packages {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []string
	for _, k := range keys {
		p := g.Packages[k]
		if p == nil || p.Dir == "" {
			continue
		}
		issues := libverify.CheckStatic(p.Dir, p.Path, nil)
		for _, iss := range issues {
			if iss.Severity != libverify.SeverityError {
				continue
			}
			loc := iss.File
			if iss.Line > 0 {
				loc = fmt.Sprintf("%s:%d", iss.File, iss.Line)
			}
			if loc != "" {
				errs = append(errs, fmt.Sprintf("%s (%s): %s", p.Path, loc, iss.Message))
			} else {
				errs = append(errs, fmt.Sprintf("%s: %s", p.Path, iss.Message))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("pulled dependency failed static verification:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
