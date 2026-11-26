package libverify

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// RunProbe materializes the library's non-excluded files into an isolated
// temporary module root under its `modPath`, compiles a minimal consumer
// program against it (`mojo build`), and executes `_tests/*.mojo` via
// `mojo run`.
func RunProbe(opts Options, mojoBin, modPath string, exclude []string) CheckResult {
	res := CheckResult{Name: "probe+tests"}
	if mojoBin == "" {
		res.Status = StatusSkip
		res.Summary = "skipped (no Mojo binary resolved)"
		return res
	}
	if modPath == "" {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  "cannot run probe without a resolved module path",
		})
		return res
	}

	tmp, err := os.MkdirTemp("", "bojo-libverify-probe-*")
	if err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  fmt.Sprintf("creating temp workspace: %v", err),
		})
		return res
	}
	defer os.RemoveAll(tmp)

	modulesRoot := filepath.Join(tmp, "modules")
	pkgDst := filepath.Join(modulesRoot, filepath.FromSlash(strings.ReplaceAll(modPath, ".", "/")))
	if err := materializePackage(opts.Dir, pkgDst, exclude); err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  fmt.Sprintf("materializing package: %v", err),
		})
		return res
	}

	// 1. Minimal consumer probe compilation.
	probeDir := filepath.Join(tmp, "probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  fmt.Sprintf("creating probe dir: %v", err),
		})
		return res
	}
	probeMain := filepath.Join(probeDir, "main.mojo")
	probeSrc := fmt.Sprintf("import %s\n\n\ndef main():\n    pass\n", modPath)
	if err := os.WriteFile(probeMain, []byte(probeSrc), 0o644); err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  fmt.Sprintf("writing probe main.mojo: %v", err),
		})
		return res
	}

	probeBin := filepath.Join(probeDir, "probe_bin")
	buildArgs := []string{"build", probeMain, "-I", modulesRoot}
	for _, inc := range opts.IncludeDirs {
		if strings.TrimSpace(inc) != "" {
			buildArgs = append(buildArgs, "-I", inc)
		}
	}
	buildArgs = append(buildArgs, "-o", probeBin)

	if out, err := runCmd(probeDir, mojoBin, buildArgs...); err != nil {
		res.Issues = append(res.Issues, Issue{
			Check:    "probe",
			Severity: SeverityError,
			Message:  fmt.Sprintf("consumer build probe failed: %s", compactOutput(out, err)),
		})
	}

	// 2. Run _tests/*.mojo against the isolated materialized module root.
	testFiles, err := collectTestFiles(opts.Dir)
	if err != nil {
		res.Issues = append(res.Issues, Issue{
			Check:    "tests",
			Severity: SeverityError,
			Message:  fmt.Sprintf("listing _tests: %v", err),
		})
	} else if len(testFiles) == 0 {
		res.Issues = append(res.Issues, Issue{
			Check:    "tests",
			Severity: SeverityWarning,
			Message:  "no test files found under _tests/",
		})
	} else {
		passed := 0
		for _, relTest := range testFiles {
			absTest := filepath.Join(opts.Dir, filepath.FromSlash(relTest))
			runArgs := []string{"run", "-I", modulesRoot}
			for _, inc := range opts.IncludeDirs {
				if strings.TrimSpace(inc) != "" {
					runArgs = append(runArgs, "-I", inc)
				}
			}
			runArgs = append(runArgs, absTest)
			if out, err := runCmd(opts.Dir, mojoBin, runArgs...); err != nil {
				res.Issues = append(res.Issues, Issue{
					Check:    "tests",
					Severity: SeverityError,
					File:     relTest,
					Message:  fmt.Sprintf("test failed: %s", compactOutput(out, err)),
				})
			} else {
				passed++
			}
		}
		res.Summary = fmt.Sprintf("probe ok, %d/%d test(s) passed", passed, len(testFiles))
	}

	res.Status = statusFromIssues(res.Issues)
	if res.Summary == "" {
		switch res.Status {
		case StatusPass:
			res.Summary = "consumer build probe ok"
		case StatusWarn:
			res.Summary = "consumer build probe ok (no tests)"
		default:
			res.Summary = "probe or tests failed"
		}
	}
	return res
}

// MaterializePackage copies all non-excluded library files from srcDir into
// dstDir, always excluding VCS/build metadata, `_tests/`, and `cmd/`.
func MaterializePackage(srcDir, dstDir string, exclude []string) error {
	return materializePackage(srcDir, dstDir, exclude)
}

// materializePackage copies all non-excluded library files from srcDir into
// dstDir, always excluding VCS/build metadata, `_tests/`, and `cmd/`.
func materializePackage(srcDir, dstDir string, exclude []string) error {
	allExcludes := append([]string{"_tests/**", "cmd/**"}, exclude...)
	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != srcDir && (d.IsDir() || d.Type()&fs.ModeSymlink != 0) {
			clean := filepath.Clean(p)
			if clean == dstDir || strings.HasPrefix(dstDir, clean+string(filepath.Separator)) {
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dstDir, 0o755)
		}
		slashRel := filepath.ToSlash(rel)
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".bojo" || name == ".vendor" || name == ".out" ||
				name == "target" || name == "_tests" || name == "cmd" {
				return filepath.SkipDir
			}
			if matchesExclude(slashRel, allExcludes) || matchesExclude(slashRel+"/", allExcludes) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstDir, rel), 0o755)
		}
		if matchesExclude(slashRel, allExcludes) {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

func collectTestFiles(repoDir string) ([]string, error) {
	testsRoot := filepath.Join(repoDir, "_tests")
	info, err := os.Stat(testsRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	var files []string
	err = filepath.WalkDir(testsRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".mojo") || strings.HasPrefix(name, "_") {
			return nil
		}
		rel, err := filepath.Rel(repoDir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func compactOutput(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var clean []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t != "" {
			clean = append(clean, t)
		}
	}
	if len(clean) == 0 {
		return err.Error()
	}
	if len(clean) > 6 {
		clean = clean[len(clean)-6:]
	}
	return strings.Join(clean, " | ")
}
