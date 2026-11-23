package pkg

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const manifest = `schema = 1

[repo]
url     = "https://github.com/Owner/mojo-tls"
license = "Apache-2.0"

[pkg]
description = "TLS for Mojo"
`

func TestNameIsDerivedFromTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", manifest)
	write(t, dir, "__init__.mojo", "")

	tree, err := LoadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A dash is not a Mojo identifier, so it becomes an underscore (D83).
	if tree.Name != "mojo_tls" {
		t.Fatalf("name = %q, want mojo_tls", tree.Name)
	}
	if tree.Pkg == nil || tree.Cmd != nil {
		t.Fatalf("tree = %+v", tree)
	}
}

func TestLibraryNeedsInitAtTheRoot(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", manifest)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("a [pkg] without __init__.mojo at the repository root must be rejected")
	}
}

func TestCommandDefaultsToCmdMain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `schema = 1
[repo]
url = "https://github.com/Owner/tool"
[cmd]
`)
	write(t, dir, "cmd/main.mojo", "def main():\n    pass\n")

	tree, err := LoadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Cmd == nil || tree.Pkg != nil {
		t.Fatalf("tree = %+v", tree)
	}
	if tree.Cmd.Entry != "cmd/main.mojo" {
		t.Fatalf("entry = %q", tree.Cmd.Entry)
	}
}

func TestCommandEntryMustExist(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `schema = 1
[repo]
url = "https://github.com/Owner/tool"
[cmd]
entry = "cmd/other.mojo"
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("a missing [cmd] entry must be rejected")
	}
}

func TestCommandEntryMustStayInside(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `schema = 1
[repo]
url = "https://github.com/Owner/tool"
[cmd]
entry = "../outside.mojo"
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("an entry outside the repository must be rejected")
	}
}

func TestEmptyPackageIsRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `schema = 1
[repo]
url = "https://github.com/Owner/empty"
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("a manifest without [pkg] and [cmd] must be rejected")
	}
}

func TestRepoURLIsRequired(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", "[pkg]\n")
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("[repo] url is required")
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `schema = 1
[repo]
url      = "https://github.com/Owner/Repo"
surprise = 1
[pkg]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("unknown keys must be rejected")
	}
}

func TestPackageMetadataIsOptional(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "__init__.mojo", "")
	write(t, dir, "bojo.pkg.toml", `schema = 1

[repo]
url = "https://github.com/x/mojo_http"

[pkg]
description = "HTTP GET through libcurl"
keywords    = ["http", "network"]
license     = "Apache-2.0"
homepage    = "https://example.com"
readme      = "README.md"
`)
	tree, err := LoadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := tree.Pkg
	if p.Description != "HTTP GET through libcurl" {
		t.Errorf("description = %q", p.Description)
	}
	if len(p.Keywords) != 2 || p.Keywords[0] != "http" {
		t.Errorf("keywords = %v", p.Keywords)
	}
	if p.License != "Apache-2.0" || p.Homepage != "https://example.com" || p.Readme != "README.md" {
		t.Errorf("metadata not parsed: %+v", p)
	}
}
