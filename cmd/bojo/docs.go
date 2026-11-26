package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bojolib/bojo/internal/manifest"
	"github.com/bojolib/bojo/internal/resolve"
	"github.com/bojolib/bojo/internal/toolchain"
)

// ---------------------------------------------------------------------------
// bojo docs <lib> [path]

// cmdDocs renders a markdown file from the vendored copy of a package.
func cmdDocs(args []string) int {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: bojo docs <package> [path]"))
		return exitUsage
	}
	dir, g, code := resolveProject()
	if code != exitOK {
		return code
	}
	p, ok := g.Packages[args[0]]
	if !ok {
		p, ok = packageByShortName(g, args[0])
	}
	if !ok {
		fail(fmt.Errorf("no package %q in the graph", args[0]))
		return exitUsage
	}

	root := docsDir(p)
	rel := "index.md"
	if len(args) > 1 {
		rel = args[1]
	}
	if strings.EqualFold(rel, "api") || strings.EqualFold(rel, "API.md") {
		apiPath := filepath.Join(p.Dir, "API.md")
		raw, err := os.ReadFile(apiPath)
		if err != nil {
			fail(fmt.Errorf("no API.md in %s (looked in %s)", args[0], apiPath))
			return exitUsage
		}
		_ = dir
		fmt.Print(renderMarkdown(string(raw)))
		return exitOK
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		fail(fmt.Errorf("path %q must stay inside the docs directory", rel))
		return exitUsage
	}
	full := filepath.Join(p.Dir, root, clean)
	raw, err := os.ReadFile(full)
	if err != nil {
		fail(fmt.Errorf("no %s in %s (looked in %s)", rel, args[0], filepath.Join(p.Dir, root)))
		return exitUsage
	}
	_ = dir
	fmt.Print(renderMarkdown(string(raw)))
	return exitOK
}

// ---------------------------------------------------------------------------
// bojo remove <package>

// cmdRemove deletes a dependency by repository. A repository is its package, so
// there is no package line to delete: the whole table goes (D83).
func cmdRemove(args []string) int {
	if len(args) != 1 {
		fail(fmt.Errorf("usage: bojo remove <url>"))
		return exitUsage
	}
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	path := filepath.Join(dir, "bojo.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		fail(err)
		return exitUsage
	}
	out, removed := removeRepoTable(string(raw), args[0])
	if !removed {
		fail(fmt.Errorf("no dependency on %q in %s", args[0], path))
		return exitUsage
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("removed %s\nnext: bojo update\n", args[0])
	return exitOK
}

// removeRepoTable drops `[deps-from."<url>"]` and its body. Textual, so
// comments and order survive.
func removeRepoTable(text, repo string) (string, bool) {
	header := fmt.Sprintf("[deps-from.%q]", repo)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != header {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				end = j
				break
			}
		}
		rest := lines[end:]
		if len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
			rest = rest[1:]
		}
		return strings.Join(append(append([]string{}, lines[:i]...), rest...), "\n"), true
	}
	return text, false
}

// renderMarkdown is deliberately small: headings, fenced code, lists, inline
// code, bold and links. Enough for a README, no dependency.
func renderMarkdown(src string) string {
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inCode = !inCode
			if inCode {
				b.WriteString("\n")
			} else {
				b.WriteString("\n")
			}
			continue
		case inCode:
			b.WriteString("    " + line + "\n")
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "### "):
			b.WriteString("\n\033[1m" + strings.TrimPrefix(trimmed, "### ") + "\033[0m\n")
		case strings.HasPrefix(trimmed, "## "):
			b.WriteString("\n\033[1m" + strings.TrimPrefix(trimmed, "## ") + "\033[0m\n")
		case strings.HasPrefix(trimmed, "# "):
			b.WriteString("\n\033[1m" + strings.TrimPrefix(trimmed, "# ") + "\033[0m\n")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* "):
			b.WriteString("  • " + inline(strings.TrimSpace(trimmed[2:])) + "\n")
		case strings.HasPrefix(trimmed, "> "):
			b.WriteString("  │ " + inline(strings.TrimPrefix(trimmed, "> ")) + "\n")
		case trimmed == "":
			b.WriteString("\n")
		default:
			b.WriteString(inline(line) + "\n")
		}
	}
	return b.String()
}

// inline strips emphasis markers and keeps code readable.
func inline(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// cmdFmt runs the formatter of the pinned toolchain over the given paths, or
// over every Mojo file in the project.
func cmdFmt(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	cfg, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	mojoBin, err := toolchain.Resolve(cfg.Version.Mojo)
	if err != nil {
		fail(err)
		return exitUsage
	}
	targets := args
	if len(targets) == 0 {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == ".bojo" || d.Name() == ".vendor" || d.Name() == ".out" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".mojo") {
				targets = append(targets, p)
			}
			return nil
		})
	}
	if len(targets) == 0 {
		fmt.Println("no Mojo files")
		return exitOK
	}
	cmd := exec.Command(mojoBin, append([]string{"format"}, targets...)...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fail(err)
		return exitBuild
	}
	return exitOK
}

// docsDir is where a package keeps its documentation.
func docsDir(p *resolve.Package) string {
	if p.Docs != "" {
		return strings.TrimSuffix(p.Docs, "/")
	}
	return "docs"
}
