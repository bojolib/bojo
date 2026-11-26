package libverify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseImports(t *testing.T) {
	src := `"""Module docstring.

Example:
    from tls import Client
    import fake_pkg
"""

# from commented_out import bad
from std.testing import assert_equal
from .core import greet
from gh.bojolib.demo.sub import helper  # trailing comment
import std.sys, v2.gh.bojolib.socket as sock

'''Single-quoted multi-line docstring:
import ignored_single
from http import Server
'''

def run():
    msg = "import not_an_import"
    from gh.bojolib.io import Reader
    from tls import Client
    return msg
`

	got := ParseImports(src)
	want := []string{
		"std.testing",
		".core",
		"gh.bojolib.demo.sub",
		"std.sys",
		"v2.gh.bojolib.socket",
		"gh.bojolib.io",
		"tls",
	}
	if len(got) != len(want) {
		t.Fatalf("ParseImports got %d imports (%+v), want %d (%v)", len(got), got, len(want), want)
	}
	for i, w := range want {
		if got[i].Module != w {
			t.Errorf("import[%d] = %q (line %d), want %q", i, got[i].Module, got[i].Line, w)
		}
	}
	if !got[1].IsRelative {
		t.Errorf("expected .core to be marked IsRelative")
	}
	if got[0].IsRelative {
		t.Errorf("expected std.testing not to be marked IsRelative")
	}
}

func TestCheckFilePurity(t *testing.T) {
	src := `from std.python import Python
from std.testing import assert_equal
from .internal_mod import helper
from gh.bojolib.mylib.core import greet
from gh.bojolib.io import Reader
from v2.gh.bojolib.socket.tcp import TcpStream
from tls import Client
from gh.bojolib.other import Secret
from v2.gh.bojolib.undeclared import Foo
`
	allowed := map[string]bool{
		"gh.bojolib.io":        true,
		"v2.gh.bojolib.socket": true,
	}

	issues := CheckFilePurity("core.mojo", src, "gh.bojolib.mylib", allowed)
	if len(issues) != 3 {
		t.Fatalf("got %d purity issues, want 3: %+v", len(issues), issues)
	}

	if !strings.Contains(issues[0].Message, "forbidden bare/alias import") || !strings.Contains(issues[0].Message, `"tls"`) {
		t.Errorf("unexpected issue[0]: %+v", issues[0])
	}
	if !strings.Contains(issues[1].Message, "undeclared bojo import") || !strings.Contains(issues[1].Message, `"gh.bojolib.other"`) {
		t.Errorf("unexpected issue[1]: %+v", issues[1])
	}
	if !strings.Contains(issues[2].Message, "undeclared bojo import") || !strings.Contains(issues[2].Message, `"v2.gh.bojolib.undeclared"`) {
		t.Errorf("unexpected issue[2]: %+v", issues[2])
	}
	for _, iss := range issues {
		if iss.Severity != SeverityError {
			t.Errorf("expected SeverityError, got %+v", iss)
		}
	}
}

func TestCheckInitImportsOnly(t *testing.T) {
	clean := `"""Exports only."""

from gh.bojolib.demo.core import greet
`
	if iss := CheckInitImportsOnly("__init__.mojo", clean); len(iss) != 0 {
		t.Fatalf("expected 0 issues for clean __init__.mojo, got %+v", iss)
	}

	dirty := `"""Exports with code."""

from gh.bojolib.demo.core import greet

def naughty() -> Int:
    return 1
`
	iss := CheckInitImportsOnly("__init__.mojo", dirty)
	if len(iss) != 1 || iss[0].Severity != SeverityWarning {
		t.Fatalf("expected 1 warning for top-level def in __init__.mojo, got %+v", iss)
	}
}

func TestCheckStaticValidRepo(t *testing.T) {
	dir := t.TempDir()
	manifest := `schema = 1

[repo]
url = "https://github.com/bojolib/http"
license = "Apache-2.0"

[pkg]
description = "HTTP client and server."
exclude = ["_tests/**", "examples/**"]

[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"

[pkg.deps-from."https://github.com/bojolib/socket"]
version = "v2.1.0"

[pkg.hooks]
vendor = [["echo", "ok"]]
`
	writeTestFile(t, dir, "bojo.pkg.toml", manifest)
	writeTestFile(t, dir, "__init__.mojo", "from gh.bojolib.http.client import Client\n")
	writeTestFile(t, dir, "client.mojo", `from std.sys import argv
from .util import parse
from gh.bojolib.io import Reader
from v2.gh.bojolib.socket import Socket
`)
	writeTestFile(t, dir, "util.mojo", "def parse() -> Int:\n    return 0\n")
	// _tests/, cmd/, and excluded examples/ must be skipped by import purity.
	writeTestFile(t, dir, "_tests/test_client.mojo", "from my_test_alias import helper\n")
	writeTestFile(t, dir, "cmd/main.mojo", "from my_cmd_alias import run\n")
	writeTestFile(t, dir, "examples/demo.mojo", "from bare_example import x\n")

	issues := CheckStatic(dir, "", nil)
	if len(issues) != 0 {
		t.Fatalf("expected 0 issues for valid library repo, got: %+v", issues)
	}
}

func TestCheckStaticErrors(t *testing.T) {
	dir := t.TempDir()
	manifest := `schema = 2

[repo]
url = "https://github.com/bojolib/badlib"

[pkg]
description = "Broken lib."

[pkg.hooks]
pre-build = [["sh", "-c", "evil"]]
`
	writeTestFile(t, dir, "bojo.pkg.toml", manifest)
	// Deliberately omit __init__.mojo and include forbidden imports in core.mojo.
	writeTestFile(t, dir, "core.mojo", `from tls import Client
from gh.bojolib.undeclared import Foo
`)

	issues := CheckStatic(dir, "", nil)
	joined := ""
	for _, iss := range issues {
		joined += iss.Check + ":" + iss.Message + "\n"
	}

	for _, want := range []string{
		"schema must be 1",
		"__init__.mojo",
		"pre-build",
		"forbidden bare/alias import",
		"undeclared bojo import",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected CheckStatic issues to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestCheckOutdated(t *testing.T) {
	deps := []DepRef{
		{Repo: "https://github.com/bojolib/io", Version: "v0.1.0"},
		{Repo: "https://github.com/bojolib/socket", Version: "v1.2.0"},
	}
	fetcher := func(repo string) ([]string, error) {
		switch repo {
		case "https://github.com/bojolib/io":
			return []string{"v0.1.0", "v0.2.0"}, nil
		case "https://github.com/bojolib/socket":
			return []string{"v1.0.0", "v1.2.0"}, nil
		default:
			return nil, nil
		}
	}

	warnRes := CheckOutdated(Options{TagsFetcher: fetcher, StrictOutdated: false}, deps)
	if warnRes.Status != StatusWarn {
		t.Fatalf("expected StatusWarn, got %s (%+v)", warnRes.Status, warnRes)
	}

	failRes := CheckOutdated(Options{TagsFetcher: fetcher, StrictOutdated: true}, deps)
	if failRes.Status != StatusFail {
		t.Fatalf("expected StatusFail with StrictOutdated, got %s (%+v)", failRes.Status, failRes)
	}
}

func TestVerifyAndReportFormatting(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "bojo.pkg.toml", `schema = 1

[repo]
url = "https://github.com/bojolib/sample"

[pkg]
description = "Sample library."

[pkg.deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
`)
	writeTestFile(t, dir, "__init__.mojo", "from gh.bojolib.sample.core import hello\n")
	writeTestFile(t, dir, "core.mojo", "from gh.bojolib.io import Reader\n")

	rep := Verify(Options{
		Dir:           dir,
		SkipProbe:     true,
		SkipAPI:       true,
		SkipToolchain: true,
		TagsFetcher: func(repo string) ([]string, error) {
			return []string{"v0.1.0", "v0.1.1"}, nil
		},
	})

	if !rep.OK {
		t.Fatalf("expected OK=true when only warnings exist, got: %s", rep.FormatText())
	}
	if rep.ModulePath != "gh.bojolib.sample" {
		t.Fatalf("got ModulePath %q, want gh.bojolib.sample", rep.ModulePath)
	}
	if len(rep.Checks) != 5 {
		t.Fatalf("expected 5 checks in report, got %d", len(rep.Checks))
	}

	text := rep.FormatText()
	for _, want := range []string{
		"[PASS] manifest+purity",
		"[SKIP] probe+tests",
		"[WARN] outdated",
		"newer version v0.1.1 is available",
		"[SKIP] api+semver",
		"[SKIP] toolchain",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("FormatText missing %q:\n%s", want, text)
		}
	}

	rawJSON, err := rep.FormatJSON()
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(rawJSON, &decoded); err != nil {
		t.Fatalf("FormatJSON produced invalid JSON: %v\n%s", err, string(rawJSON))
	}
	if decoded.ModulePath != rep.ModulePath || decoded.OK != rep.OK || len(decoded.Checks) != 5 {
		t.Fatalf("decoded JSON mismatch: %+v", decoded)
	}
}

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
