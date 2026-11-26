package libverify

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/apidoc"
	"github.com/bojolib/bojo/internal/tag"
)

// CheckAPI extracts the library's public API via `apidoc.Extract`, verifies
// `API.md` exists and matches `apidoc.RenderMarkdown` byte-for-byte, and
// compares the current API surface against the previous `vX.Y.Z` git tag (if
// any) via `apidoc.Diff` to detect unbumped breaking changes.
func CheckAPI(opts Options, mojoBin, modPath, repoURL, description string, exclude []string) CheckResult {
	res := CheckResult{Name: "api+semver"}
	if mojoBin == "" {
		res.Status = StatusSkip
		res.Summary = "skipped (no Mojo binary resolved)"
		return res
	}
	if modPath == "" {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "api",
			Severity: SeverityError,
			Message:  "cannot extract API without a resolved module path",
		})
		return res
	}

	tmp, err := os.MkdirTemp("", "bojo-libverify-api-*")
	if err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "api",
			Severity: SeverityError,
			Message:  fmt.Sprintf("creating temp dir: %v", err),
		})
		return res
	}
	defer os.RemoveAll(tmp)

	modulesRoot := filepath.Join(tmp, "modules")
	pkgDir := filepath.Join(modulesRoot, filepath.FromSlash(strings.ReplaceAll(modPath, ".", "/")))
	if err := materializePackage(opts.Dir, pkgDir, exclude); err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "api",
			Severity: SeverityError,
			Message:  fmt.Sprintf("materializing package for mojo doc: %v", err),
		})
		return res
	}

	includes := append([]string{modulesRoot}, opts.IncludeDirs...)
	curDoc, err := apidoc.Extract(mojoBin, pkgDir, includes)
	if err != nil {
		res.Status = StatusFail
		res.Issues = append(res.Issues, Issue{
			Check:    "api",
			Severity: SeverityError,
			Message:  fmt.Sprintf("extracting API doc: %v", err),
		})
		return res
	}

	// 1. Check API.md freshness byte-for-byte.
	wantMD := apidoc.RenderMarkdown(curDoc, apidoc.Meta{
		ModulePath:  modPath,
		RepoURL:     repoURL,
		Description: description,
	})
	apiPath := filepath.Join(opts.Dir, "API.md")
	haveRaw, err := os.ReadFile(apiPath)
	if err != nil {
		if os.IsNotExist(err) {
			res.Issues = append(res.Issues, Issue{
				Check:    "api",
				Severity: SeverityError,
				File:     "API.md",
				Message:  "API.md is missing (run `bojo lib api` to generate it)",
			})
		} else {
			res.Issues = append(res.Issues, Issue{
				Check:    "api",
				Severity: SeverityError,
				File:     "API.md",
				Message:  fmt.Sprintf("reading API.md: %v", err),
			})
		}
	} else if string(haveRaw) != wantMD {
		res.Issues = append(res.Issues, Issue{
			Check:    "api",
			Severity: SeverityError,
			File:     "API.md",
			Message:  "API.md is out of date (run `bojo lib api` to regenerate it)",
		})
	}

	// 2. Compare against previous vX.Y.Z git tag if one exists.
	prevTag, prevVer, ok := findPreviousTag(opts.Dir, opts.Version)
	if ok {
		prevModPath := modPath
		if repoURL != "" {
			if p, err := ModulePathForVersion(repoURL, prevTag); err == nil {
				prevModPath = p
			}
		}
		prevModulesRoot := filepath.Join(tmp, "prev-modules")
		prevPkgDir := filepath.Join(prevModulesRoot, filepath.FromSlash(strings.ReplaceAll(prevModPath, ".", "/")))
		if err := extractGitTagArchive(opts.Dir, prevTag, prevPkgDir, exclude); err != nil {
			res.Issues = append(res.Issues, Issue{
				Check:    "semver",
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("could not extract previous tag %s for API diff: %v", prevTag, err),
			})
		} else {
			prevIncludes := append([]string{prevModulesRoot}, opts.IncludeDirs...)
			prevDoc, err := apidoc.Extract(mojoBin, prevPkgDir, prevIncludes)
			if err != nil {
				res.Issues = append(res.Issues, Issue{
					Check:    "semver",
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("could not extract API doc for previous tag %s: %v", prevTag, err),
				})
			} else {
				diff := apidoc.Diff(prevDoc, curDoc)
				if diff.HasBreaking() {
					majorBumped := isMajorBump(prevVer, opts.Version)
					if majorBumped {
						res.Issues = append(res.Issues, Issue{
							Check:    "semver",
							Severity: SeverityInfo,
							Message:  fmt.Sprintf("%d breaking API change(s) since %s (allowed by major bump to %s)", len(diff.Breaking), prevTag, opts.Version),
						})
					} else {
						sev := SeverityWarning
						if opts.StrictBreaking {
							sev = SeverityError
						}
						for _, ch := range diff.Breaking {
							res.Issues = append(res.Issues, Issue{
								Check:    "semver",
								Severity: sev,
								Message:  fmt.Sprintf("breaking change since %s: %s: %s", prevTag, ch.Symbol, ch.Message),
							})
						}
					}
				}
			}
		}
	}

	res.Status = statusFromIssues(res.Issues)
	switch res.Status {
	case StatusPass:
		if ok {
			res.Summary = fmt.Sprintf("API.md up to date; compatible with %s", prevTag)
		} else {
			res.Summary = "API.md up to date (no prior release tag)"
		}
	case StatusWarn:
		res.Summary = fmt.Sprintf("API.md up to date; breaking changes detected since %s", prevTag)
	default:
		res.Summary = "API.md or semver compatibility check failed"
	}
	return res
}

func findPreviousTag(repoDir, targetVersion string) (string, tag.Version, bool) {
	out, err := runCmd(repoDir, "git", "tag", "--list")
	if err != nil {
		return "", tag.Version{}, false
	}

	var targetVer tag.Version
	hasTarget := false
	if vStr := strings.TrimSpace(targetVersion); vStr != "" {
		if !strings.HasPrefix(vStr, "v") {
			vStr = "v" + vStr
		}
		if v, err := tag.ParseVersion(vStr); err == nil {
			targetVer = v
			hasTarget = true
		}
	}

	type tagged struct {
		raw string
		ver tag.Version
	}
	var cands []tagged
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		raw := strings.TrimSpace(line)
		if raw == "" {
			continue
		}
		v, err := tag.ParseVersion(raw)
		if err != nil {
			continue
		}
		if hasTarget && v.Compare(targetVer) >= 0 {
			continue
		}
		cands = append(cands, tagged{raw: raw, ver: v})
	}
	if len(cands) == 0 {
		return "", tag.Version{}, false
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].ver.Compare(cands[j].ver) > 0 })
	return cands[0].raw, cands[0].ver, true
}

func isMajorBump(prev tag.Version, targetVersion string) bool {
	vStr := strings.TrimSpace(targetVersion)
	if vStr == "" {
		return false
	}
	if !strings.HasPrefix(vStr, "v") {
		vStr = "v" + vStr
	}
	cur, err := tag.ParseVersion(vStr)
	if err != nil {
		return false
	}
	return cur.Major > prev.Major
}

func extractGitTagArchive(repoDir, gitTag, dstDir string, exclude []string) error {
	allExcludes := append([]string{"_tests/**", "cmd/**"}, exclude...)
	cmd := exec.Command("git", "-C", repoDir, "archive", "--format=tar", gitTag)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tr := tar.NewReader(stdout)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return err
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if name == "." || name == "" || strings.HasPrefix(name, "..") {
			continue
		}
		if matchesExclude(name, allExcludes) {
			continue
		}
		target := filepath.Join(dstDir, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				_ = cmd.Wait()
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				_ = cmd.Wait()
				return err
			}
			raw, err := io.ReadAll(tr)
			if err != nil {
				_ = cmd.Wait()
				return err
			}
			if err := os.WriteFile(target, raw, 0o644); err != nil {
				_ = cmd.Wait()
				return err
			}
		}
	}
	return cmd.Wait()
}
