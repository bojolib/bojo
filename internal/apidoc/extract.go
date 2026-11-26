// Package apidoc extracts, renders, and diffs public Mojo API documentation
// produced by `mojo doc` (JSON).
package apidoc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Doc is the top-level JSON document emitted by `mojo doc -o <file>`.
type Doc struct {
	Version string `json:"version"`
	Decl    Decl   `json:"decl"`
}

// Decl represents a Mojo module or package declaration node.
type Decl struct {
	Kind        string     `json:"kind"` // "module" or "package"
	Name        string     `json:"name"`
	Summary     string     `json:"summary,omitempty"`
	Description string     `json:"description,omitempty"`
	Modules     []Decl     `json:"modules,omitempty"`
	Packages    []Decl     `json:"packages,omitempty"`
	Aliases     []Alias    `json:"aliases,omitempty"`
	Functions   []Function `json:"functions,omitempty"`
	Structs     []Struct   `json:"structs,omitempty"`
	Traits      []Trait    `json:"traits,omitempty"`
}

// Alias represents a Mojo `alias` / `comptime` declaration.
type Alias struct {
	Name        string `json:"name"`
	Signature   string `json:"signature,omitempty"`
	Value       string `json:"value,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Deprecated  string `json:"deprecated,omitempty"`
}

// Function groups one or more overloads sharing a function or method name.
type Function struct {
	Kind      string     `json:"kind,omitempty"`
	Name      string     `json:"name"`
	Overloads []Overload `json:"overloads,omitempty"`
}

// Overload is a single callable signature of a Function.
type Overload struct {
	Name        string     `json:"name"`
	Signature   string     `json:"signature,omitempty"`
	Summary     string     `json:"summary,omitempty"`
	Description string     `json:"description,omitempty"`
	Args        []Arg      `json:"args,omitempty"`
	Parameters  []Param    `json:"parameters,omitempty"`
	Returns     *ReturnDoc `json:"returns,omitempty"`
	ReturnsDoc  string     `json:"returnsDoc,omitempty"`
	Raises      bool       `json:"raises,omitempty"`
	RaisesDoc   string     `json:"raisesDoc,omitempty"`
	Deprecated  string     `json:"deprecated,omitempty"`
	Async       bool       `json:"async,omitempty"`
}

// Arg describes one runtime argument of an Overload.
type Arg struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Convention  string `json:"convention,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// Param describes one compile-time parameter of a Struct or Overload.
type Param struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// ReturnDoc describes the return type (and optional doc text) of an Overload.
type ReturnDoc struct {
	Type string `json:"type,omitempty"`
	Doc  string `json:"doc,omitempty"`
}

// UnmarshalJSON accepts either an object {"type": "...", "doc": "..."} or a
// plain string return type.
func (r *ReturnDoc) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		r.Type = s
		return nil
	}
	type rawReturn ReturnDoc
	var rr rawReturn
	if err := json.Unmarshal(data, &rr); err != nil {
		return err
	}
	*r = ReturnDoc(rr)
	return nil
}

// Field describes one member field of a Struct.
type Field struct {
	Name        string `json:"name"`
	Type        string `json:"type,omitempty"`
	Signature   string `json:"signature,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Deprecated  string `json:"deprecated,omitempty"`
}

// Struct describes a Mojo struct declaration.
type Struct struct {
	Name         string     `json:"name"`
	Signature    string     `json:"signature,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	Description  string     `json:"description,omitempty"`
	Fields       []Field    `json:"fields,omitempty"`
	Functions    []Function `json:"functions,omitempty"`
	Aliases      []Alias    `json:"aliases,omitempty"`
	Parameters   []Param    `json:"parameters,omitempty"`
	ParentTraits []string   `json:"parentTraits,omitempty"`
	Deprecated   string     `json:"deprecated,omitempty"`
}

// Trait describes a Mojo trait declaration.
type Trait struct {
	Name         string     `json:"name"`
	Signature    string     `json:"signature,omitempty"`
	Summary      string     `json:"summary,omitempty"`
	Description  string     `json:"description,omitempty"`
	Functions    []Function `json:"functions,omitempty"`
	Aliases      []Alias    `json:"aliases,omitempty"`
	ParentTraits []string   `json:"parentTraits,omitempty"`
	Deprecated   string     `json:"deprecated,omitempty"`
}

// Extract invokes `<mojoBin> doc -o <tmp.json> [-I <dir>...] <targetDir>` and
// parses the resulting JSON. Writing to a temporary file via `-o` guarantees
// Crashpad or compiler stderr messages can never corrupt the JSON payload.
func Extract(mojoBin, targetDir string, includeDirs []string) (*Doc, error) {
	if strings.TrimSpace(mojoBin) == "" {
		return nil, fmt.Errorf("mojo binary path is required")
	}
	if strings.TrimSpace(targetDir) == "" {
		return nil, fmt.Errorf("target directory is required")
	}

	tmpFile, err := os.CreateTemp("", "bojo-apidoc-*.json")
	if err != nil {
		return nil, fmt.Errorf("creating temp file for mojo doc: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}
	defer os.Remove(tmpPath)

	args := []string{"doc", "-o", tmpPath}
	for _, dir := range includeDirs {
		if strings.TrimSpace(dir) != "" {
			args = append(args, "-I", dir)
		}
	}
	args = append(args, targetDir)

	cmd := exec.Command(mojoBin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("mojo doc %s: %w: %s", targetDir, err, msg)
		}
		return nil, fmt.Errorf("mojo doc %s: %w", targetDir, err)
	}

	raw, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("reading mojo doc output: %w", err)
	}
	return Parse(raw)
}

// Parse unmarshals raw `mojo doc` JSON bytes into a Doc.
func Parse(data []byte) (*Doc, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, fmt.Errorf("empty mojo doc JSON")
	}
	var doc Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing mojo doc JSON: %w", err)
	}
	if doc.Decl.Name == "" && doc.Decl.Kind == "" {
		return nil, fmt.Errorf("mojo doc JSON is missing decl.name and decl.kind")
	}
	return &doc, nil
}

// IsPublic reports whether a symbol name is part of the public API surface.
// Names starting with "_" are considered private.
func IsPublic(name string) bool {
	return name != "" && !strings.HasPrefix(name, "_")
}

// isPublicModule reports whether a child module or package should be exposed.
// Only "__init__" (the package root module) is exempted from the leading "_" rule.
func isPublicModule(name string) bool {
	return name == "__init__" || IsPublic(name)
}

// ModuleView is a flattened, public-only view of one module/package node with
// its qualified submodule path (empty string for the root package).
type ModuleView struct {
	Path        string // "" for root / __init__, otherwise e.g. "core" or "net.tcp"
	Name        string
	Summary     string
	Description string
	Aliases     []Alias
	Traits      []Trait
	Structs     []Struct
	Functions   []Function
}

// FlattenPublic walks a Doc and returns all public modules in deterministic
// order (root first, then submodules sorted lexicographically by Path).
// Private symbols (starting with "_") are filtered out.
func FlattenPublic(doc *Doc) []ModuleView {
	if doc == nil {
		return nil
	}
	var views []ModuleView
	collectModules(&doc.Decl, "", true, &views)

	// Merge any "__init__" child module into root "" if both exist, or keep root first.
	byPath := make(map[string]int, len(views))
	var merged []ModuleView
	for _, v := range views {
		if idx, ok := byPath[v.Path]; ok {
			m := &merged[idx]
			if m.Summary == "" {
				m.Summary = v.Summary
			}
			if m.Description == "" {
				m.Description = v.Description
			}
			m.Aliases = append(m.Aliases, v.Aliases...)
			m.Traits = append(m.Traits, v.Traits...)
			m.Structs = append(m.Structs, v.Structs...)
			m.Functions = append(m.Functions, v.Functions...)
		} else {
			byPath[v.Path] = len(merged)
			merged = append(merged, v)
		}
	}

	// Sort symbols inside each module and drop completely empty non-root modules
	// only if they carry neither symbols nor documentation.
	for i := range merged {
		sortModuleView(&merged[i])
	}
	sortModuleViews(merged)
	return merged
}

func collectModules(d *Decl, prefix string, isRoot bool, out *[]ModuleView) {
	if d == nil {
		return
	}
	if !isRoot && !isPublicModule(d.Name) {
		return
	}

	modPath := prefix
	if !isRoot {
		if d.Name != "__init__" {
			if modPath == "" {
				modPath = d.Name
			} else {
				modPath = modPath + "." + d.Name
			}
		}
	}

	view := ModuleView{
		Path:        modPath,
		Name:        d.Name,
		Summary:     strings.TrimSpace(d.Summary),
		Description: strings.TrimSpace(d.Description),
		Aliases:     filterAliases(d.Aliases),
		Traits:      filterTraits(d.Traits),
		Structs:     filterStructs(d.Structs),
		Functions:   filterFunctions(d.Functions),
	}

	hasSymbols := len(view.Aliases) > 0 || len(view.Traits) > 0 || len(view.Structs) > 0 || len(view.Functions) > 0
	if isRoot || hasSymbols || view.Summary != "" || view.Description != "" {
		*out = append(*out, view)
	}

	for i := range d.Modules {
		collectModules(&d.Modules[i], modPath, false, out)
	}
	for i := range d.Packages {
		collectModules(&d.Packages[i], modPath, false, out)
	}
}

func filterAliases(in []Alias) []Alias {
	var out []Alias
	for _, a := range in {
		if IsPublic(a.Name) {
			out = append(out, a)
		}
	}
	return out
}

func filterFunctions(in []Function) []Function {
	var out []Function
	for _, f := range in {
		if IsPublic(f.Name) {
			cp := f
			cp.Overloads = append([]Overload(nil), f.Overloads...)
			out = append(out, cp)
		}
	}
	return out
}

func filterFields(in []Field) []Field {
	var out []Field
	for _, f := range in {
		if IsPublic(f.Name) {
			out = append(out, f)
		}
	}
	return out
}

func filterStructs(in []Struct) []Struct {
	var out []Struct
	for _, s := range in {
		if !IsPublic(s.Name) {
			continue
		}
		cp := s
		cp.Fields = filterFields(s.Fields)
		cp.Aliases = filterAliases(s.Aliases)
		cp.Functions = filterFunctions(s.Functions)
		cp.Parameters = append([]Param(nil), s.Parameters...)
		cp.ParentTraits = append([]string(nil), s.ParentTraits...)
		out = append(out, cp)
	}
	return out
}

func filterTraits(in []Trait) []Trait {
	var out []Trait
	for _, t := range in {
		if !IsPublic(t.Name) {
			continue
		}
		cp := t
		cp.Aliases = filterAliases(t.Aliases)
		cp.Functions = filterFunctions(t.Functions)
		cp.ParentTraits = append([]string(nil), t.ParentTraits...)
		out = append(out, cp)
	}
	return out
}
