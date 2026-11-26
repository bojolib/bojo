package libverify

import (
	"fmt"
	"strings"
)

// ImportStmt represents a single parsed Mojo import statement.
type ImportStmt struct {
	Line       int    // 1-based line number
	Module     string // imported module path, e.g. "std.testing", ".core", "gh.bojolib.io"
	IsRelative bool   // true when Module starts with "."
	Raw        string // trimmed code line (without comments)
}

// ParseImports scans Mojo source code and returns every `import ...` and
// `from ... import ...` statement, ignoring `#` comments and single/double/
// triple-quoted string literals (including multi-line docstrings).
func ParseImports(src string) []ImportStmt {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	var out []ImportStmt
	inTriple := "" // "", `"""`, or `'''`

	for idx, rawLine := range lines {
		lineNo := idx + 1
		codeLine, nextTriple := stripCommentsAndStrings(rawLine, inTriple)
		inTriple = nextTriple

		trimmed := strings.TrimSpace(codeLine)
		if trimmed == "" {
			continue
		}

		// Case 1: `from <module> import ...` (including `from .core import ...` or `from.core import ...`).
		if rest, ok := trimFromPrefix(trimmed); ok {
			mod := extractFromModule(rest)
			if mod != "" {
				out = append(out, ImportStmt{
					Line:       lineNo,
					Module:     mod,
					IsRelative: strings.HasPrefix(mod, "."),
					Raw:        trimmed,
				})
			}
			continue
		}

		// Case 2: `import <mod1> [as <alias>], <mod2> ...`
		if rest, ok := trimKeywordPrefix(trimmed, "import"); ok {
			for _, part := range strings.Split(rest, ",") {
				fields := strings.Fields(strings.TrimSpace(part))
				if len(fields) == 0 {
					continue
				}
				mod := strings.TrimRight(fields[0], ";")
				if mod == "" {
					continue
				}
				out = append(out, ImportStmt{
					Line:       lineNo,
					Module:     mod,
					IsRelative: strings.HasPrefix(mod, "."),
					Raw:        trimmed,
				})
			}
		}
	}

	return out
}

// CheckFilePurity verifies that every import in a library `.mojo` file is
// allowed: Mojo `std`/`std.*`/`builtin`, relative imports (`.foo`), the
// library's own `modPath` (and submodules), or a module path declared in
// `[pkg.deps-from]`.
func CheckFilePurity(relFile, src, modPath string, allowedDeps map[string]bool) []Issue {
	var issues []Issue
	for _, imp := range ParseImports(src) {
		if isAllowedModule(imp.Module, modPath, allowedDeps) {
			continue
		}
		var msg string
		if isBojoModulePath(imp.Module) {
			msg = fmt.Sprintf("undeclared bojo import %q (declare it under [pkg.deps-from] in bojo.pkg.toml)", imp.Module)
		} else {
			msg = fmt.Sprintf("forbidden bare/alias import %q (use std.*, relative imports, own module path %s, or declared [pkg.deps-from] paths)", imp.Module, modPath)
		}
		issues = append(issues, Issue{
			Check:    "purity",
			Severity: SeverityError,
			File:     relFile,
			Line:     imp.Line,
			Message:  msg,
		})
	}
	return issues
}

// CheckInitImportsOnly warns when `__init__.mojo` declares top-level
// functions, structs, traits, aliases, or variables directly instead of
// keeping the package root imports-only.
func CheckInitImportsOnly(relFile, src string) []Issue {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")

	var issues []Issue
	inTriple := ""
	declKeywords := []string{"def", "fn", "struct", "trait", "comptime", "alias", "var", "let"}

	for idx, rawLine := range lines {
		lineNo := idx + 1
		codeLine, nextTriple := stripCommentsAndStrings(rawLine, inTriple)
		inTriple = nextTriple

		// Only inspect unindented top-level statements.
		if codeLine == "" || codeLine[0] == ' ' || codeLine[0] == '\t' {
			continue
		}
		trimmed := strings.TrimSpace(codeLine)
		if trimmed == "" {
			continue
		}
		for _, kw := range declKeywords {
			if _, ok := trimKeywordPrefix(trimmed, kw); ok {
				issues = append(issues, Issue{
					Check:    "purity",
					Severity: SeverityWarning,
					File:     relFile,
					Line:     lineNo,
					Message:  fmt.Sprintf("__init__.mojo should contain imports only (found top-level %q declaration)", kw),
				})
				break
			}
		}
	}
	return issues
}

func isAllowedModule(mod, ownModPath string, allowedDeps map[string]bool) bool {
	if mod == "" {
		return true
	}
	// Relative imports inside the package.
	if strings.HasPrefix(mod, ".") {
		return true
	}
	// Mojo 1.2.0 stdlib and compiler builtins.
	if mod == "std" || strings.HasPrefix(mod, "std.") ||
		mod == "builtin" || strings.HasPrefix(mod, "builtin.") {
		return true
	}
	// Own module path (and submodules).
	if ownModPath != "" && (mod == ownModPath || strings.HasPrefix(mod, ownModPath+".")) {
		return true
	}
	// Declared dependencies (and their submodules).
	for depPath := range allowedDeps {
		if depPath != "" && (mod == depPath || strings.HasPrefix(mod, depPath+".")) {
			return true
		}
	}
	return false
}

func isBojoModulePath(mod string) bool {
	s := mod
	// Strip optional v<N>. major version prefix.
	if len(s) >= 3 && s[0] == 'v' {
		dot := strings.IndexByte(s, '.')
		if dot > 1 && isAllDigits(s[1:dot]) {
			s = s[dot+1:]
		}
	}
	for _, prefix := range []string{"gh.", "gl.", "cb.", "sr."} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func stripCommentsAndStrings(line, inTriple string) (string, string) {
	var b strings.Builder
	n := len(line)
	i := 0
	for i < n {
		if inTriple != "" {
			if line[i] == '\\' && i+1 < n {
				i += 2
				continue
			}
			if i+3 <= n && line[i:i+3] == inTriple {
				inTriple = ""
				i += 3
				continue
			}
			i++
			continue
		}

		// Line comment starts outside strings.
		if line[i] == '#' {
			break
		}

		// Triple-quoted string starts.
		if i+3 <= n && (line[i:i+3] == `"""` || line[i:i+3] == `'''`) {
			inTriple = line[i : i+3]
			i += 3
			continue
		}

		// Single- or double-quoted string literal on one line.
		if line[i] == '"' || line[i] == '\'' {
			quote := line[i]
			i++
			for i < n {
				if line[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if line[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}

		b.WriteByte(line[i])
		i++
	}
	return b.String(), inTriple
}

func trimKeywordPrefix(s, kw string) (string, bool) {
	if !strings.HasPrefix(s, kw) {
		return "", false
	}
	if len(s) == len(kw) {
		return "", false
	}
	next := s[len(kw)]
	if next != ' ' && next != '\t' {
		return "", false
	}
	return strings.TrimSpace(s[len(kw)+1:]), true
}

func trimFromPrefix(s string) (string, bool) {
	if !strings.HasPrefix(s, "from") || len(s) <= 4 {
		return "", false
	}
	next := s[4]
	if next == ' ' || next == '\t' {
		return strings.TrimSpace(s[5:]), true
	}
	if next == '.' {
		return strings.TrimSpace(s[4:]), true
	}
	return "", false
}

func extractFromModule(rest string) string {
	// Expect `<module> import ...`.
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return ""
	}
	if fields[1] != "import" {
		return ""
	}
	return fields[0]
}
