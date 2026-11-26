package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func loadFromString(t *testing.T, body string) (*File, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bojo.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

func TestLoadValidManifests(t *testing.T) {
	body := `schema = 1

[project]
name    = "demo"
version = "0.1.0"
entry   = "main.mojo"

[version]
mojo = "1.2.0"

[deps-from."https://github.com/bojolib/socket"]
version = "v1.4.0"

[deps-from."https://github.com/bojolib/socket@v2"]
version = "v2.0.0"

[deps-from."https://github.com/bojolib/io"]
commit = "1111111111111111111111111111111111111111"

[alias]
"v2.gh.bojolib.socket" = "socket"

[override."gh.bojolib.io"]
version = "v0.2.0"

[override."v2.gh.bojolib.task"]
commit = "2222222222222222222222222222222222222222"

[replace."gh.bojolib.http"]
repo    = "https://github.com/myfork/http"
version = "v0.3.1-patched"

[replace."gh.bojolib.tls"]
path = "../local-tls"
`
	f, err := loadFromString(t, body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Schema != 1 {
		t.Errorf("schema = %d, want 1", f.Schema)
	}
	if len(f.DepsFrom) != 3 {
		t.Errorf("deps-from len = %d, want 3", len(f.DepsFrom))
	}
	if f.DepsFrom["https://github.com/bojolib/socket@v2"].Version != "v2.0.0" {
		t.Errorf("socket@v2 = %+v", f.DepsFrom["https://github.com/bojolib/socket@v2"])
	}
	if f.Alias["v2.gh.bojolib.socket"] != "socket" {
		t.Errorf("alias = %+v", f.Alias)
	}
	if f.Override["gh.bojolib.io"].Version != "v0.2.0" {
		t.Errorf("override io = %+v", f.Override["gh.bojolib.io"])
	}
	if f.Override["v2.gh.bojolib.task"].Commit != "2222222222222222222222222222222222222222" {
		t.Errorf("override task = %+v", f.Override["v2.gh.bojolib.task"])
	}
	if f.Replace["gh.bojolib.http"].Repo != "https://github.com/myfork/http" {
		t.Errorf("replace http = %+v", f.Replace["gh.bojolib.http"])
	}
	if f.Replace["gh.bojolib.tls"].Path != "../local-tls" {
		t.Errorf("replace tls = %+v", f.Replace["gh.bojolib.tls"])
	}
}

func TestValidationErrors(t *testing.T) {
	base := `schema = 1
[project]
name  = "demo"
entry = "main.mojo"
`
	cases := []struct {
		name string
		toml string
	}{
		{
			name: "unsupported schema",
			toml: `schema = 2
[project]
name = "demo"
entry = "main.mojo"
`,
		},
		{
			name: "missing project name",
			toml: `schema = 1
[project]
entry = "main.mojo"
`,
		},
		{
			name: "missing project entry",
			toml: `schema = 1
[project]
name = "demo"
`,
		},
		{
			name: "invalid deps-from url",
			toml: base + `
[deps-from."not-a-url"]
version = "v0.1.0"
`,
		},
		{
			name: "deps-from both version and commit",
			toml: base + `
[deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
commit  = "1111111111111111111111111111111111111111"
`,
		},
		{
			name: "deps-from major suffix mismatch",
			toml: base + `
[deps-from."https://github.com/bojolib/socket@v2"]
version = "v1.4.0"
`,
		},
		{
			name: "deps-from unknown field",
			toml: base + `
[deps-from."https://github.com/bojolib/io"]
version = "v0.1.0"
extra   = "nope"
`,
		},
		{
			name: "alias invalid key",
			toml: base + `
[alias]
"plain" = "io"
`,
		},
		{
			name: "alias invalid name",
			toml: base + `
[alias]
"gh.bojolib.io" = "bad/name"
`,
		},
		{
			name: "override missing version and commit",
			toml: base + `
[override."gh.bojolib.io"]
`,
		},
		{
			name: "override both version and commit",
			toml: base + `
[override."gh.bojolib.io"]
version = "v0.1.0"
commit  = "1111111111111111111111111111111111111111"
`,
		},
		{
			name: "override v2 path with v1 version",
			toml: base + `
[override."v2.gh.bojolib.socket"]
version = "v1.2.0"
`,
		},
		{
			name: "override v1 path with v2 version",
			toml: base + `
[override."gh.bojolib.socket"]
version = "v2.0.0"
`,
		},
		{
			name: "override invalid module path key",
			toml: base + `
[override."socket"]
version = "v1.0.0"
`,
		},
		{
			name: "override unknown field",
			toml: base + `
[override."gh.bojolib.io"]
version = "v0.1.0"
repo    = "https://github.com/other/io"
`,
		},
		{
			name: "replace missing repo and path",
			toml: base + `
[replace."gh.bojolib.io"]
`,
		},
		{
			name: "replace both repo and path",
			toml: base + `
[replace."gh.bojolib.io"]
repo    = "https://github.com/fork/io"
version = "v0.1.0"
path    = "../io"
`,
		},
		{
			name: "replace local path with version",
			toml: base + `
[replace."gh.bojolib.io"]
path    = "../io"
version = "v0.1.0"
`,
		},
		{
			name: "replace repo without version or commit",
			toml: base + `
[replace."gh.bojolib.io"]
repo = "https://github.com/fork/io"
`,
		},
		{
			name: "replace repo with both version and commit",
			toml: base + `
[replace."gh.bojolib.io"]
repo    = "https://github.com/fork/io"
version = "v0.1.0"
commit  = "1111111111111111111111111111111111111111"
`,
		},
		{
			name: "replace invalid repo url",
			toml: base + `
[replace."gh.bojolib.io"]
repo    = "not-a-url"
version = "v0.1.0"
`,
		},
		{
			name: "same module in both override and replace",
			toml: base + `
[override."gh.bojolib.io"]
version = "v0.2.0"

[replace."gh.bojolib.io"]
path = "../io"
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadFromString(t, tc.toml); err == nil {
				t.Fatalf("%s: expected validation error, got nil", tc.name)
			}
		})
	}
}
