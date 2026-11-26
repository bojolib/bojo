package pages

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojolib/bojo/internal/apidoc"
)

func TestGenerateStaticSite(t *testing.T) {
	tmp := t.TempDir()
	readmePath := filepath.Join(tmp, "README.md")
	docsDir := filepath.Join(tmp, "docs")
	outDir := filepath.Join(tmp, "site")

	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	readme := `# Demo Socket

A fast library with **bold**, *italic*, and ` + "`inline_code`" + `.

<script>alert("xss")</script>

[Safe Link](https://bojolib.github.io/socket/) and [Bad Link](javascript:alert(1)).

` + "```mojo\nfrom gh.bojolib.socket import Socket\n```" + `
`
	if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}

	guide1 := "# Getting Started\n\nInstall with `bojo get`.\n\n- Step one\n- Step two\n"
	guide2 := "# Advanced Usage\n\n| Option | Default |\n| --- | --- |\n| timeout | 30s |\n"
	if err := os.WriteFile(filepath.Join(docsDir, "01-getting-started.md"), []byte(guide1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "02-advanced.md"), []byte(guide2), 0o644); err != nil {
		t.Fatal(err)
	}

	doc := &apidoc.Doc{
		Version: "1.2.0",
		Decl: apidoc.Decl{
			Kind:    "package",
			Name:    "socket",
			Summary: "TCP/UDP sockets for Mojo.",
			Modules: []apidoc.Decl{
				{
					Kind:    "module",
					Name:    "core",
					Summary: "Core socket primitives.",
					Structs: []apidoc.Struct{
						{
							Name:         "Socket",
							Summary:      "A network socket <unsafe>.",
							ParentTraits: []string{"Movable"},
							Fields: []apidoc.Field{
								{Name: "fd", Type: "Int", Summary: "File descriptor."},
							},
							Functions: []apidoc.Function{
								{
									Name: "close",
									Overloads: []apidoc.Overload{
										{
											Name:       "close",
											Signature:  "def close(mut self) raises",
											Summary:    "Close socket.",
											Raises:     true,
											Deprecated: "Use shutdown() instead.",
										},
									},
								},
							},
						},
					},
					Functions: []apidoc.Function{
						{
							Name: "dial",
							Overloads: []apidoc.Overload{
								{
									Name:      "dial",
									Signature: "def dial(host: String, port: Int) raises -> Socket",
									Summary:   "Connect to host:port.",
									Args: []apidoc.Arg{
										{Name: "host", Type: "String", Description: "Target host."},
										{Name: "port", Type: "Int", Description: "Target port."},
									},
									Returns: &apidoc.ReturnDoc{Type: "Socket", Doc: "Connected socket."},
									Raises:  true,
								},
							},
						},
					},
				},
			},
		},
	}

	err := Generate(Options{
		OutDir:      outDir,
		PackageName: "socket",
		ModulePath:  "gh.bojolib.socket",
		RepoURL:     "https://github.com/bojolib/socket",
		Version:     "v0.2.0",
		Description: "TCP/UDP sockets for Mojo.",
		License:     "Apache-2.0",
		Keywords:    []string{"mojo", "network"},
		ReadmePath:  readmePath,
		DocsDir:     docsDir,
		Doc:         doc,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify all expected output files exist.
	expectedFiles := []string{
		"index.html",
		"api/index.html",
		"docs/01-getting-started.html",
		"docs/02-advanced.html",
		"search.json",
		"assets/style.css",
		"assets/app.js",
	}
	for _, rel := range expectedFiles {
		p := filepath.Join(outDir, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("expected file %s missing: %v", rel, err)
		}
		if info.Size() == 0 {
			t.Fatalf("expected file %s to be non-empty", rel)
		}
	}

	// Verify index.html content and XSS escaping.
	indexRaw, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	indexHTML := string(indexRaw)
	if !strings.Contains(indexHTML, "bojo get https://github.com/bojolib/socket@v0.2.0") {
		t.Errorf("index.html missing install command")
	}
	if strings.Contains(indexHTML, `<script>alert("xss")</script>`) {
		t.Fatalf("XSS vulnerability: raw <script> tag was not escaped in index.html")
	}
	if !strings.Contains(indexHTML, `&lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt;`) {
		t.Errorf("expected escaped script tag in index.html")
	}
	if strings.Contains(indexHTML, `href="javascript:`) {
		t.Fatalf("XSS vulnerability: javascript: href was not stripped")
	}
	if !strings.Contains(indexHTML, `<a href="https://bojolib.github.io/socket/">Safe Link</a>`) {
		t.Errorf("expected safe link in index.html")
	}

	// Verify api/index.html content and escaping.
	apiRaw, err := os.ReadFile(filepath.Join(outDir, "api", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	apiHTML := string(apiRaw)
	for _, want := range []string{
		`id="mod-core"`,
		`id="struct-core-socket"`,
		`id="func-core-dial"`,
		`def dial(host: String, port: Int) raises -&gt; Socket`,
		`Deprecated`,
		`Use shutdown() instead.`,
		`A network socket &lt;unsafe&gt;.`,
	} {
		if !strings.Contains(apiHTML, want) {
			t.Errorf("api/index.html missing %q", want)
		}
	}

	// Verify docs guide page and prev/next navigation.
	g1Raw, err := os.ReadFile(filepath.Join(outDir, "docs", "01-getting-started.html"))
	if err != nil {
		t.Fatal(err)
	}
	g1HTML := string(g1Raw)
	if !strings.Contains(g1HTML, "Getting Started") || !strings.Contains(g1HTML, `../docs/02-advanced.html`) {
		t.Errorf("guide 1 missing expected title or next link")
	}

	g2Raw, err := os.ReadFile(filepath.Join(outDir, "docs", "02-advanced.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(g2Raw), "<table><thead><tr><th>Option</th><th>Default</th></tr></thead>") {
		t.Errorf("guide 2 missing rendered markdown table")
	}

	// Verify search.json entries.
	searchRaw, err := os.ReadFile(filepath.Join(outDir, "search.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []SearchEntry
	if err := json.Unmarshal(searchRaw, &entries); err != nil {
		t.Fatalf("invalid search.json: %v", err)
	}

	found := map[string]bool{}
	for _, e := range entries {
		found[e.Kind+":"+e.Title] = true
	}
	for _, want := range []string{
		"overview:socket",
		"module:core",
		"struct:Socket",
		"method:Socket.close",
		"function:dial",
		"guide:Getting Started",
		"guide:Advanced Usage",
	} {
		if !found[want] {
			t.Errorf("search.json missing entry %q (got %+v)", want, entries)
		}
	}
}
