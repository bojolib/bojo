// Package libverify implements static pull checks and the full 5-gate library
// verification suite (`bojo lib verify`).
package libverify

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bojolib/bojo/internal/pkg"
)

// Severity classifies a verification Issue.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Status represents the outcome of one named verification check.
type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusSkip Status = "skip"
)

// Issue is a single finding reported by a verification check.
type Issue struct {
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Message  string   `json:"message"`
}

// CheckResult is the outcome of one verification gate.
type CheckResult struct {
	Name    string  `json:"name"`
	Status  Status  `json:"status"`
	Summary string  `json:"summary,omitempty"`
	Issues  []Issue `json:"issues,omitempty"`
}

// DepRef describes one declared dependency repository and its pinned version or
// commit.
type DepRef struct {
	Repo    string `json:"repo"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// Options configures a full `Verify` run.
type Options struct {
	Dir            string   // library repository directory
	ModulePath     string   // optional override; derived from [repo] url (+ Version) when empty
	Version        string   // optional target version (e.g. "v2.0.0") for v2+ module path & semver diff
	MojoVersion    string   // optional Mojo toolchain version (empty = PATH)
	MojoBin        string   // optional pre-resolved mojo binary path
	IncludeDirs    []string // extra -I include roots (e.g. materialized dependencies)
	Deps           []DepRef // optional pre-loaded dependencies; auto-read from bojo.pkg.toml when empty
	StrictOutdated bool     // treat outdated dependencies as errors instead of warnings
	StrictBreaking bool     // treat unbumped breaking API changes as errors instead of warnings
	SkipProbe      bool     // skip consumer build probe + _tests
	SkipOutdated   bool     // skip remote tag outdated check
	SkipAPI        bool     // skip mojo doc / API.md / semver diff check
	SkipToolchain  bool     // skip toolchain check
	TagsFetcher    func(repo string) ([]string, error)
}

// Report is the structured, JSON-serializable result of Verify.
type Report struct {
	Dir        string        `json:"dir"`
	ModulePath string        `json:"module_path,omitempty"`
	Toolchain  string        `json:"toolchain,omitempty"`
	OK         bool          `json:"ok"`
	Checks     []CheckResult `json:"checks"`
}

// Verify runs the 5 library verification gates:
//  1. manifest+purity (CheckStatic)
//  2. probe+tests (isolated consumer build probe + _tests/*.mojo)
//  3. outdated (upstream semver tag check for [pkg.deps-from])
//  4. api+semver (API.md freshness + breaking change diff against prior git tag)
//  5. toolchain (Mojo toolchain resolution + fingerprint)
func Verify(opts Options) Report {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	report := Report{Dir: dir}

	raw, _ := parseRawManifest(filepath.Join(dir, pkg.FileName))
	repoURL := ""
	description := ""
	var exclude []string
	if raw != nil {
		repoURL = strings.TrimSpace(raw.Repo.URL)
		if raw.Pkg != nil {
			description = strings.TrimSpace(raw.Pkg.Description)
			exclude = raw.Pkg.Exclude
		}
	}

	modPath := strings.TrimSpace(opts.ModulePath)
	if modPath == "" && repoURL != "" {
		if derived, err := ModulePathForVersion(repoURL, opts.Version); err == nil {
			modPath = derived
		}
	}
	report.ModulePath = modPath

	deps := opts.Deps
	if len(deps) == 0 && raw != nil {
		deps = extractDepsFromRaw(raw)
	}

	// Gate 1: manifest+purity (static checks).
	staticIssues := CheckStaticWithDeps(dir, modPath, nil, opts.Deps)
	staticRes := CheckResult{
		Name:   "manifest+purity",
		Status: statusFromIssues(staticIssues),
		Issues: staticIssues,
	}
	switch staticRes.Status {
	case StatusPass:
		staticRes.Summary = "manifest valid and imports pure"
	case StatusWarn:
		staticRes.Summary = "manifest valid with warnings"
	default:
		staticRes.Summary = "manifest or import purity errors found"
	}
	report.Checks = append(report.Checks, staticRes)

	// Resolve toolchain (needed for gates 2 & 4, and reported as gate 5).
	var tcRes CheckResult
	mojoBin := opts.MojoBin
	if opts.SkipToolchain {
		tcRes = CheckResult{Name: "toolchain", Status: StatusSkip, Summary: "skipped"}
	} else {
		var fp string
		tcRes, mojoBin, fp = CheckToolchain(opts)
		report.Toolchain = fp
	}

	// Gate 2: probe+tests.
	if opts.SkipProbe {
		report.Checks = append(report.Checks, CheckResult{Name: "probe+tests", Status: StatusSkip, Summary: "skipped"})
	} else {
		report.Checks = append(report.Checks, RunProbe(opts, mojoBin, modPath, exclude))
	}

	// Gate 3: outdated.
	if opts.SkipOutdated {
		report.Checks = append(report.Checks, CheckResult{Name: "outdated", Status: StatusSkip, Summary: "skipped"})
	} else {
		report.Checks = append(report.Checks, CheckOutdated(opts, deps))
	}

	// Gate 4: api+semver.
	if opts.SkipAPI {
		report.Checks = append(report.Checks, CheckResult{Name: "api+semver", Status: StatusSkip, Summary: "skipped"})
	} else {
		report.Checks = append(report.Checks, CheckAPI(opts, mojoBin, modPath, repoURL, description, exclude))
	}

	// Gate 5: toolchain.
	report.Checks = append(report.Checks, tcRes)

	report.OK = true
	for _, c := range report.Checks {
		if c.Status == StatusFail {
			report.OK = false
			break
		}
	}
	return report
}

// FormatText renders the Report cleanly with one concise fact per line.
func (r Report) FormatText() string {
	var b strings.Builder
	for _, c := range r.Checks {
		tag := strings.ToUpper(string(c.Status))
		if c.Summary != "" {
			fmt.Fprintf(&b, "[%s] %-16s %s\n", tag, c.Name, c.Summary)
		} else {
			fmt.Fprintf(&b, "[%s] %s\n", tag, c.Name)
		}
		for _, iss := range c.Issues {
			loc := ""
			if iss.File != "" {
				if iss.Line > 0 {
					loc = fmt.Sprintf("%s:%d: ", iss.File, iss.Line)
				} else {
					loc = iss.File + ": "
				}
			}
			fmt.Fprintf(&b, "  %s: %s%s\n", iss.Severity, loc, iss.Message)
		}
	}
	return b.String()
}

// FormatJSON serializes the Report as pretty-printed JSON for `--json`.
func (r Report) FormatJSON() ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func statusFromIssues(issues []Issue) Status {
	hasWarn := false
	for _, iss := range issues {
		switch iss.Severity {
		case SeverityError:
			return StatusFail
		case SeverityWarning:
			hasWarn = true
		}
	}
	if hasWarn {
		return StatusWarn
	}
	return StatusPass
}
