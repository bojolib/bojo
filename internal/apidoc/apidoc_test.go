package apidoc

import (
	"strings"
	"testing"
)

const samplePackageJSON = `{
  "version": "1.2.0",
  "decl": {
    "kind": "package",
    "name": "demo",
    "summary": "Demo Mojo package.",
    "modules": [
      {
        "kind": "module",
        "name": "__init__",
        "summary": "Root exports.",
        "functions": [
          {
            "kind": "function",
            "name": "greet",
            "overloads": [
              {
                "name": "greet",
                "signature": "def greet(name: String) -> String",
                "summary": "Return a greeting.",
                "args": [
                  {"name": "name", "type": "String", "convention": "imm", "description": "Person to greet."}
                ],
                "returns": {"type": "String", "doc": "Greeting message."}
              }
            ]
          },
          {
            "kind": "function",
            "name": "_secret_root",
            "overloads": [
              {"name": "_secret_root", "signature": "def _secret_root()"}
            ]
          }
        ]
      },
      {
        "kind": "module",
        "name": "zeta",
        "summary": "Zeta module.",
        "aliases": [
          {"name": "MAX_SIZE", "signature": "alias MAX_SIZE = 1024", "summary": "Maximum size."},
          {"name": "_INTERNAL", "signature": "alias _INTERNAL = 0"}
        ],
        "traits": [
          {
            "name": "Reader",
            "summary": "Reads bytes.",
            "parentTraits": ["Movable"],
            "functions": [
              {
                "name": "read",
                "overloads": [
                  {
                    "name": "read",
                    "signature": "def read(mut self, n: Int) raises -> String",
                    "args": [
                      {"name": "self", "type": "Self", "convention": "mut"},
                      {"name": "n", "type": "Int", "convention": "imm"}
                    ],
                    "returns": {"type": "String"},
                    "raises": true,
                    "raisesDoc": "On I/O error."
                  }
                ]
              }
            ]
          }
        ]
      },
      {
        "kind": "module",
        "name": "alpha",
        "summary": "Alpha module.",
        "structs": [
          {
            "name": "Client",
            "summary": "Network client.",
            "parentTraits": ["Movable", "Copyable"],
            "fields": [
              {"name": "port", "type": "Int", "summary": "TCP port."},
              {"name": "host", "type": "String", "summary": "Hostname."},
              {"name": "_fd", "type": "Int", "summary": "Private descriptor."}
            ],
            "functions": [
              {
                "name": "connect",
                "overloads": [
                  {
                    "name": "connect",
                    "signature": "def connect(mut self) raises",
                    "summary": "Open connection.",
                    "raises": true,
                    "deprecated": "Use dial() instead."
                  }
                ]
              },
              {
                "name": "_raw_open",
                "overloads": [{"name": "_raw_open", "signature": "def _raw_open()"}]
              }
            ]
          },
          {
            "name": "_Hidden",
            "summary": "Should not appear."
          }
        ]
      },
      {
        "kind": "module",
        "name": "_internal",
        "summary": "Private module.",
        "functions": [
          {"name": "leak", "overloads": [{"name": "leak", "signature": "def leak()"}]}
        ]
      }
    ]
  }
}`

func TestParse(t *testing.T) {
	doc, err := Parse([]byte(samplePackageJSON))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if doc.Version != "1.2.0" {
		t.Fatalf("got version %q, want 1.2.0", doc.Version)
	}
	if doc.Decl.Kind != "package" || doc.Decl.Name != "demo" {
		t.Fatalf("unexpected decl: %+v", doc.Decl)
	}
	if len(doc.Decl.Modules) != 4 {
		t.Fatalf("got %d modules, want 4", len(doc.Decl.Modules))
	}

	// Plain string return type is also accepted.
	strRet := `{"version":"1.2.0","decl":{"kind":"module","name":"m","functions":[{"name":"f","overloads":[{"name":"f","returns":"Int"}]}]}}`
	d2, err := Parse([]byte(strRet))
	if err != nil {
		t.Fatalf("Parse with string returns failed: %v", err)
	}
	if d2.Decl.Functions[0].Overloads[0].Returns == nil || d2.Decl.Functions[0].Overloads[0].Returns.Type != "Int" {
		t.Fatalf("unexpected string return parse: %+v", d2.Decl.Functions[0].Overloads[0].Returns)
	}

	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("expected error on empty JSON")
	}
	if _, err := Parse([]byte("{}")); err == nil {
		t.Fatal("expected error on missing decl")
	}
}

func TestRenderMarkdownDeterminismAndFiltering(t *testing.T) {
	doc1, err := Parse([]byte(samplePackageJSON))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	// Construct a second doc with reversed module/field/trait ordering to verify
	// deterministic sorting.
	doc2, err := Parse([]byte(samplePackageJSON))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	for i, j := 0, len(doc2.Decl.Modules)-1; i < j; i, j = i+1, j-1 {
		doc2.Decl.Modules[i], doc2.Decl.Modules[j] = doc2.Decl.Modules[j], doc2.Decl.Modules[i]
	}

	meta := Meta{ModulePath: "gh.bojolib.demo"}
	md1 := RenderMarkdown(doc1, meta)
	md2 := RenderMarkdown(doc2, meta)

	if md1 != md2 {
		t.Fatalf("RenderMarkdown is not deterministic:\n--- md1 ---\n%s\n--- md2 ---\n%s", md1, md2)
	}

	// Required header and notice.
	if !strings.HasPrefix(md1, "# gh.bojolib.demo\n\n> Generated by `bojo lib api` — do not edit by hand.\n") {
		t.Fatalf("unexpected header:\n%s", md1)
	}

	// Public symbols present.
	for _, want := range []string{
		"## Package Exports",
		"def greet(name: String) -> String",
		"## Module `alpha`",
		"## Module `zeta`",
		"struct Client(Copyable, Movable)",
		"- `host` (`String`): Hostname.",
		"- `port` (`Int`): TCP port.",
		"**Deprecated:** Use dial() instead.",
		"alias MAX_SIZE = 1024",
		"trait Reader(Movable)",
		"**Raises:** On I/O error.",
	} {
		if !strings.Contains(md1, want) {
			t.Errorf("expected markdown to contain %q\nfull output:\n%s", want, md1)
		}
	}

	// Private symbols excluded.
	for _, forbidden := range []string{
		"_secret_root",
		"_INTERNAL",
		"_fd",
		"_raw_open",
		"_Hidden",
		"_internal",
		"leak",
	} {
		if strings.Contains(md1, forbidden) {
			t.Errorf("markdown must not contain private symbol %q", forbidden)
		}
	}

	// Alphabetical module ordering: alpha before zeta.
	if strings.Index(md1, "## Module `alpha`") > strings.Index(md1, "## Module `zeta`") {
		t.Errorf("expected module alpha before zeta")
	}
	// Alphabetical field ordering: host before port.
	if strings.Index(md1, "- `host`") > strings.Index(md1, "- `port`") {
		t.Errorf("expected field host before port")
	}
}

func TestDiffIdentical(t *testing.T) {
	doc, err := Parse([]byte(samplePackageJSON))
	if err != nil {
		t.Fatal(err)
	}
	res := Diff(doc, doc)
	if !res.Empty() {
		t.Fatalf("expected empty diff for identical docs, got: %+v", res)
	}
}

func TestDiffAdditiveVsBreaking(t *testing.T) {
	oldDoc := &Doc{
		Version: "1.2.0",
		Decl: Decl{
			Kind: "module",
			Name: "core",
			Aliases: []Alias{
				{Name: "PORT", Signature: "alias PORT = 80"},
			},
			Structs: []Struct{
				{
					Name:         "Conn",
					ParentTraits: []string{"Movable"},
					Fields: []Field{
						{Name: "host", Type: "String"},
						{Name: "_priv", Type: "Int"},
					},
					Functions: []Function{
						{
							Name: "send",
							Overloads: []Overload{
								{
									Name:      "send",
									Signature: "def send(self, data: String) raises -> Int",
									Args: []Arg{
										{Name: "self", Type: "Self", Convention: "imm"},
										{Name: "data", Type: "String", Convention: "imm"},
									},
									Returns: &ReturnDoc{Type: "Int"},
									Raises:  true,
								},
							},
						},
					},
				},
			},
			Functions: []Function{
				{
					Name: "greet",
					Overloads: []Overload{
						{
							Name:      "greet",
							Signature: "def greet(name: String) -> String",
							Args:      []Arg{{Name: "name", Type: "String"}},
							Returns:   &ReturnDoc{Type: "String"},
						},
					},
				},
			},
		},
	}

	// 1. Purely additive update + private change.
	additiveDoc := &Doc{
		Version: "1.2.0",
		Decl: Decl{
			Kind: "module",
			Name: "core",
			Aliases: []Alias{
				{Name: "PORT", Signature: "alias PORT = 80"},
				{Name: "TLS_PORT", Signature: "alias TLS_PORT = 443"},
			},
			Structs: []Struct{
				{
					Name:         "Conn",
					ParentTraits: []string{"Movable", "Copyable"},
					Fields: []Field{
						{Name: "host", Type: "String"},
						{Name: "timeout", Type: "Int"},
						{Name: "_priv", Type: "String"}, // private type change must be ignored
					},
					Functions: []Function{
						{
							Name: "send",
							Overloads: []Overload{
								{
									Name:      "send",
									Signature: "def send(self, data: String) -> Int",
									Args: []Arg{
										{Name: "self", Type: "Self", Convention: "imm"},
										{Name: "data", Type: "String", Convention: "imm"},
									},
									Returns: &ReturnDoc{Type: "Int"},
									Raises:  false, // no longer raises -> additive
								},
							},
						},
					},
				},
			},
			Functions: []Function{
				{
					Name: "greet",
					Overloads: []Overload{
						{
							Name:      "greet",
							Signature: "def greet(name: String, loud: Bool = False) -> String",
							Args: []Arg{
								{Name: "name", Type: "String"},
								{Name: "loud", Type: "Bool", Default: "False"},
							},
							Returns: &ReturnDoc{Type: "String"},
						},
					},
				},
				{
					Name: "farewell",
					Overloads: []Overload{
						{Name: "farewell", Signature: "def farewell() -> String", Returns: &ReturnDoc{Type: "String"}},
					},
				},
			},
		},
	}

	addRes := Diff(oldDoc, additiveDoc)
	if addRes.HasBreaking() {
		t.Fatalf("expected zero breaking changes, got: %+v", addRes.Breaking)
	}
	if len(addRes.Additive) < 5 {
		t.Fatalf("expected >= 5 additive changes, got %d: %+v", len(addRes.Additive), addRes.Additive)
	}

	// 2. Breaking changes: changed alias, removed field, added required arg, changed return type.
	breakingDoc := &Doc{
		Version: "1.2.0",
		Decl: Decl{
			Kind: "module",
			Name: "core",
			Aliases: []Alias{
				{Name: "PORT", Signature: "alias PORT = 8080"},
			},
			Structs: []Struct{
				{
					Name:         "Conn",
					ParentTraits: []string{"Movable"},
					Fields:       []Field{}, // removed host
					Functions:    []Function{},
				},
			},
			Functions: []Function{
				{
					Name: "greet",
					Overloads: []Overload{
						{
							Name:      "greet",
							Signature: "def greet(name: String, extra: Int) -> Int",
							Args: []Arg{
								{Name: "name", Type: "String"},
								{Name: "extra", Type: "Int"},
							},
							Returns: &ReturnDoc{Type: "Int"},
						},
					},
				},
			},
		},
	}

	brkRes := Diff(oldDoc, breakingDoc)
	if !brkRes.HasBreaking() {
		t.Fatal("expected breaking changes")
	}
	if len(brkRes.Breaking) < 5 {
		t.Fatalf("expected >= 5 breaking changes, got %d: %+v", len(brkRes.Breaking), brkRes.Breaking)
	}
}
