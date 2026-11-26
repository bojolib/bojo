// Package pages generates a self-contained, zero-CDN static HTML documentation
// site for GitHub Pages (https://bojolib.github.io/<repo>/).
package pages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bojolib/bojo/internal/apidoc"
)

// Options configures static site generation.
type Options struct {
	OutDir      string      // required output directory
	PackageName string      // e.g. "socket"
	ModulePath  string      // e.g. "gh.bojolib.socket"
	RepoURL     string      // e.g. "https://github.com/bojolib/socket"
	Version     string      // e.g. "v0.1.0" (defaults to "latest" in install snippet if empty)
	Description string      // package one-liner
	License     string      // SPDX license identifier
	Keywords    []string    // optional keywords
	ReadmePath  string      // optional path to README.md
	DocsDir     string      // optional path to docs/ containing *.md guides
	Doc         *apidoc.Doc // optional extracted Mojo API documentation
}

// SearchEntry is one item in the client-side search.json index.
type SearchEntry struct {
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Module  string `json:"module,omitempty"`
	Summary string `json:"summary,omitempty"`
	URL     string `json:"url"`
}

type guidePage struct {
	Slug  string
	Title string
	HTML  template.HTML
}

type pageData struct {
	PageTitle   string
	PackageName string
	ModulePath  string
	SafeRepoURL string
	Version     string
	Description string
	License     string
	Keywords    []string
	RootPrefix  string
	ActivePage  string
	ActiveGuide string
	Guides      []guidePage

	// Overview
	InstallCmd  string
	SymbolCount int
	ModuleCount int
	ReadmeHTML  template.HTML

	// API
	APISummary string
	APIModules []apiModuleVM

	// Guide
	GuideTitle string
	GuideHTML  template.HTML
	PrevGuide  *guidePage
	NextGuide  *guidePage
}

type apiModuleVM struct {
	Anchor          string
	Path            string
	DisplayName     string
	Summary         string
	Description     string
	DescriptionHTML template.HTML
	Aliases         []apiAliasVM
	Traits          []apiTraitVM
	Structs         []apiStructVM
	Functions       []apiFuncVM
}

type apiAliasVM struct {
	Anchor     string
	Name       string
	Signature  string
	Deprecated string
	DocHTML    template.HTML
}

type apiTraitVM struct {
	Anchor     string
	Name       string
	Signature  string
	Deprecated string
	DocHTML    template.HTML
	Methods    []apiFuncVM
}

type apiStructVM struct {
	Anchor     string
	Name       string
	Signature  string
	Deprecated string
	DocHTML    template.HTML
	Fields     []apiFieldVM
	Methods    []apiFuncVM
}

type apiFieldVM struct {
	Name        string
	Type        string
	Description string
}

type apiFuncVM struct {
	Anchor    string
	Name      string
	Overloads []apiOverloadVM
}

type apiOverloadVM struct {
	Signature      string
	Deprecated     string
	DocHTML        template.HTML
	Args           []apiArgVM
	ReturnsDisplay string
	ReturnsDoc     string
	RaisesDisplay  string
}

type apiArgVM struct {
	Name        string
	TypeDisplay string
	Description string
}

// Generate writes a complete static documentation site into opts.OutDir.
func Generate(opts Options) error {
	if strings.TrimSpace(opts.OutDir) == "" {
		return fmt.Errorf("pages: OutDir is required")
	}
	pkgName := strings.TrimSpace(opts.PackageName)
	if pkgName == "" && opts.Doc != nil {
		pkgName = strings.TrimSpace(opts.Doc.Decl.Name)
	}
	if pkgName == "" {
		pkgName = "package"
	}

	if err := os.MkdirAll(filepath.Join(opts.OutDir, "assets"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(opts.OutDir, "api"), 0o755); err != nil {
		return err
	}

	// Write static assets.
	if err := os.WriteFile(filepath.Join(opts.OutDir, "assets", "style.css"), []byte(assetStyleCSS), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opts.OutDir, "assets", "app.js"), []byte(assetAppJS), 0o644); err != nil {
		return err
	}

	// Load optional README.md.
	var readmeHTML template.HTML
	if opts.ReadmePath != "" {
		raw, err := os.ReadFile(opts.ReadmePath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reading README %s: %w", opts.ReadmePath, err)
		}
		if err == nil {
			readmeHTML = renderMarkdownHTML(string(raw))
		}
	}

	// Load optional docs/*.md guides.
	guides, err := loadGuides(opts.DocsDir)
	if err != nil {
		return err
	}

	// Build API view models and search index entries.
	apiModules, apiSummary, symCount, searchEntries := buildAPIViewModels(opts.Doc, pkgName, opts.Description)

	// Add overview & guide entries to search index.
	searchEntries = append([]SearchEntry{{
		Title:   pkgName,
		Kind:    "overview",
		Summary: strings.TrimSpace(opts.Description),
		URL:     "index.html",
	}}, searchEntries...)

	for _, g := range guides {
		searchEntries = append(searchEntries, SearchEntry{
			Title:   g.Title,
			Kind:    "guide",
			Summary: "Documentation guide",
			URL:     "docs/" + g.Slug + ".html",
		})
	}

	safeRepo := ""
	if isSafeHref(opts.RepoURL) && (strings.HasPrefix(opts.RepoURL, "https://") || strings.HasPrefix(opts.RepoURL, "http://")) {
		safeRepo = opts.RepoURL
	}

	installCmd := ""
	if strings.TrimSpace(opts.RepoURL) != "" {
		ver := strings.TrimSpace(opts.Version)
		if ver == "" {
			ver = "latest"
		}
		installCmd = fmt.Sprintf("bojo get %s@%s", strings.TrimSpace(opts.RepoURL), ver)
	}

	base := pageData{
		PackageName: pkgName,
		ModulePath:  strings.TrimSpace(opts.ModulePath),
		SafeRepoURL: safeRepo,
		Version:     strings.TrimSpace(opts.Version),
		Description: strings.TrimSpace(opts.Description),
		License:     strings.TrimSpace(opts.License),
		Keywords:    opts.Keywords,
		Guides:      guides,
	}

	// 1. index.html
	ovData := base
	ovData.PageTitle = "Overview"
	ovData.RootPrefix = ""
	ovData.ActivePage = "overview"
	ovData.InstallCmd = installCmd
	ovData.SymbolCount = symCount
	ovData.ModuleCount = len(apiModules)
	ovData.ReadmeHTML = readmeHTML
	if err := writeTemplate(filepath.Join(opts.OutDir, "index.html"), overviewTmpl, ovData); err != nil {
		return err
	}

	// 2. api/index.html
	apiData := base
	apiData.PageTitle = "API Reference"
	apiData.RootPrefix = "../"
	apiData.ActivePage = "api"
	apiData.APISummary = apiSummary
	apiData.APIModules = apiModules
	if err := writeTemplate(filepath.Join(opts.OutDir, "api", "index.html"), apiTmpl, apiData); err != nil {
		return err
	}

	// 3. docs/<slug>.html
	if len(guides) > 0 {
		if err := os.MkdirAll(filepath.Join(opts.OutDir, "docs"), 0o755); err != nil {
			return err
		}
		for i := range guides {
			g := guides[i]
			dData := base
			dData.PageTitle = g.Title
			dData.RootPrefix = "../"
			dData.ActivePage = "doc"
			dData.ActiveGuide = g.Slug
			dData.GuideTitle = g.Title
			dData.GuideHTML = g.HTML
			if i > 0 {
				prev := guides[i-1]
				dData.PrevGuide = &prev
			}
			if i+1 < len(guides) {
				next := guides[i+1]
				dData.NextGuide = &next
			}
			outPath := filepath.Join(opts.OutDir, "docs", g.Slug+".html")
			if err := writeTemplate(outPath, docTmpl, dData); err != nil {
				return err
			}
		}
	}

	// 4. search.json
	rawSearch, err := json.MarshalIndent(searchEntries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opts.OutDir, "search.json"), append(rawSearch, '\n'), 0o644); err != nil {
		return err
	}

	return nil
}

func writeTemplate(path string, tmpl *template.Template, data pageData) error {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering %s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func loadGuides(docsDir string) ([]guidePage, error) {
	if strings.TrimSpace(docsDir) == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(docsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading docs dir %s: %w", docsDir, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	guides := make([]guidePage, 0, len(files))
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(docsDir, name))
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		slug := slugify(base)
		if slug == "" {
			slug = "guide"
		}
		text := string(raw)
		title := extractMarkdownTitle(text)
		if title == "" {
			title = humanizeSlug(base)
		}
		guides = append(guides, guidePage{
			Slug:  slug,
			Title: title,
			HTML:  renderMarkdownHTML(text),
		})
	}
	return guides, nil
}

func buildAPIViewModels(doc *apidoc.Doc, pkgName, fallbackDesc string) ([]apiModuleVM, string, int, []SearchEntry) {
	if doc == nil {
		return nil, strings.TrimSpace(fallbackDesc), 0, nil
	}
	views := apidoc.FlattenPublic(doc)
	apiSummary := strings.TrimSpace(doc.Decl.Summary)
	if apiSummary == "" {
		apiSummary = strings.TrimSpace(fallbackDesc)
	}

	var mods []apiModuleVM
	var entries []SearchEntry
	symCount := 0

	for _, v := range views {
		hasItems := len(v.Aliases) > 0 || len(v.Traits) > 0 || len(v.Structs) > 0 || len(v.Functions) > 0
		if !hasItems {
			continue
		}
		display := v.Path
		modSlug := slugify(v.Path)
		if display == "" {
			display = pkgName
			modSlug = "root"
		}
		modAnchor := "mod-" + modSlug

		entries = append(entries, SearchEntry{
			Title:   display,
			Kind:    "module",
			Summary: v.Summary,
			URL:     "api/index.html#" + modAnchor,
		})

		vm := apiModuleVM{
			Anchor:          modAnchor,
			Path:            v.Path,
			DisplayName:     display,
			Summary:         v.Summary,
			Description:     v.Description,
			DescriptionHTML: renderMarkdownHTML(v.Description),
		}

		for _, a := range v.Aliases {
			anchor := fmt.Sprintf("alias-%s-%s", modSlug, slugify(a.Name))
			vm.Aliases = append(vm.Aliases, apiAliasVM{
				Anchor:     anchor,
				Name:       a.Name,
				Signature:  apidoc.AliasSignature(a),
				Deprecated: strings.TrimSpace(a.Deprecated),
				DocHTML:    renderMarkdownHTML(joinDoc(a.Summary, a.Description)),
			})
			symCount++
			entries = append(entries, SearchEntry{
				Title:   a.Name,
				Kind:    "alias",
				Module:  display,
				Summary: strings.TrimSpace(a.Summary),
				URL:     "api/index.html#" + anchor,
			})
		}

		for _, tr := range v.Traits {
			anchor := fmt.Sprintf("trait-%s-%s", modSlug, slugify(tr.Name))
			tVM := apiTraitVM{
				Anchor:     anchor,
				Name:       tr.Name,
				Signature:  apidoc.TraitSignature(tr),
				Deprecated: strings.TrimSpace(tr.Deprecated),
				DocHTML:    renderMarkdownHTML(joinDoc(tr.Summary, tr.Description)),
			}
			for _, fn := range tr.Functions {
				mAnchor := fmt.Sprintf("method-%s-%s-%s", modSlug, slugify(tr.Name), slugify(fn.Name))
				tVM.Methods = append(tVM.Methods, buildFuncVM(mAnchor, fn))
				entries = append(entries, SearchEntry{
					Title:   tr.Name + "." + fn.Name,
					Kind:    "method",
					Module:  display,
					Summary: firstOverloadSummary(fn),
					URL:     "api/index.html#" + mAnchor,
				})
			}
			vm.Traits = append(vm.Traits, tVM)
			symCount++
			entries = append(entries, SearchEntry{
				Title:   tr.Name,
				Kind:    "trait",
				Module:  display,
				Summary: strings.TrimSpace(tr.Summary),
				URL:     "api/index.html#" + anchor,
			})
		}

		for _, st := range v.Structs {
			anchor := fmt.Sprintf("struct-%s-%s", modSlug, slugify(st.Name))
			sVM := apiStructVM{
				Anchor:     anchor,
				Name:       st.Name,
				Signature:  apidoc.StructSignature(st),
				Deprecated: strings.TrimSpace(st.Deprecated),
				DocHTML:    renderMarkdownHTML(joinDoc(st.Summary, st.Description)),
			}
			for _, f := range st.Fields {
				desc := strings.TrimSpace(f.Summary)
				if desc == "" {
					desc = strings.TrimSpace(f.Description)
				}
				sVM.Fields = append(sVM.Fields, apiFieldVM{
					Name:        f.Name,
					Type:        f.Type,
					Description: desc,
				})
			}
			for _, fn := range st.Functions {
				mAnchor := fmt.Sprintf("method-%s-%s-%s", modSlug, slugify(st.Name), slugify(fn.Name))
				sVM.Methods = append(sVM.Methods, buildFuncVM(mAnchor, fn))
				entries = append(entries, SearchEntry{
					Title:   st.Name + "." + fn.Name,
					Kind:    "method",
					Module:  display,
					Summary: firstOverloadSummary(fn),
					URL:     "api/index.html#" + mAnchor,
				})
			}
			vm.Structs = append(vm.Structs, sVM)
			symCount++
			entries = append(entries, SearchEntry{
				Title:   st.Name,
				Kind:    "struct",
				Module:  display,
				Summary: strings.TrimSpace(st.Summary),
				URL:     "api/index.html#" + anchor,
			})
		}

		for _, fn := range v.Functions {
			anchor := fmt.Sprintf("func-%s-%s", modSlug, slugify(fn.Name))
			vm.Functions = append(vm.Functions, buildFuncVM(anchor, fn))
			symCount++
			entries = append(entries, SearchEntry{
				Title:   fn.Name,
				Kind:    "function",
				Module:  display,
				Summary: firstOverloadSummary(fn),
				URL:     "api/index.html#" + anchor,
			})
		}

		mods = append(mods, vm)
	}

	return mods, apiSummary, symCount, entries
}

func buildFuncVM(anchor string, fn apidoc.Function) apiFuncVM {
	vm := apiFuncVM{
		Anchor: anchor,
		Name:   fn.Name,
	}
	for _, ov := range fn.Overloads {
		oVM := apiOverloadVM{
			Signature:  apidoc.OverloadSignature(fn.Name, ov),
			Deprecated: strings.TrimSpace(ov.Deprecated),
			DocHTML:    renderMarkdownHTML(joinDoc(ov.Summary, ov.Description)),
		}
		for _, a := range ov.Args {
			td := a.Type
			if a.Convention != "" && a.Convention != "imm" && a.Convention != "read" {
				td = a.Convention + " " + td
			}
			if a.Default != "" {
				td = td + " = " + a.Default
			}
			oVM.Args = append(oVM.Args, apiArgVM{
				Name:        a.Name,
				TypeDisplay: strings.TrimSpace(td),
				Description: strings.TrimSpace(a.Description),
			})
		}
		if ov.Returns != nil {
			oVM.ReturnsDisplay = strings.TrimSpace(ov.Returns.Type)
			oVM.ReturnsDoc = strings.TrimSpace(ov.Returns.Doc)
		}
		if oVM.ReturnsDoc == "" {
			oVM.ReturnsDoc = strings.TrimSpace(ov.ReturnsDoc)
		}
		if ov.Raises || strings.TrimSpace(ov.RaisesDoc) != "" {
			oVM.RaisesDisplay = strings.TrimSpace(ov.RaisesDoc)
			if oVM.RaisesDisplay == "" {
				oVM.RaisesDisplay = "Yes"
			}
		}
		vm.Overloads = append(vm.Overloads, oVM)
	}
	return vm
}

func firstOverloadSummary(fn apidoc.Function) string {
	if len(fn.Overloads) == 0 {
		return ""
	}
	return strings.TrimSpace(fn.Overloads[0].Summary)
}

func joinDoc(summary, description string) string {
	s := strings.TrimSpace(summary)
	d := strings.TrimSpace(description)
	switch {
	case s == "" && d == "":
		return ""
	case s == "":
		return d
	case d == "" || d == s:
		return s
	default:
		return s + "\n\n" + d
	}
}

func extractMarkdownTitle(md string) string {
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return ""
}

func humanizeSlug(name string) string {
	s := strings.NewReplacer("-", " ", "_", " ").Replace(strings.TrimSpace(name))
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlnum {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// isSafeHref validates link targets so javascript:, data:, vbscript: etc. can
// never be injected into generated HTML.
func isSafeHref(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "mailto:") {
		return true
	}
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return !strings.HasPrefix(s, "//")
	}
	// Relative URL without any scheme prefix.
	colon := strings.IndexByte(s, ':')
	slash := strings.IndexByte(s, '/')
	if colon >= 0 && (slash < 0 || colon < slash) {
		return false
	}
	return true
}

// renderMarkdownHTML converts Markdown into strictly escaped, XSS-safe HTML.
// All raw text is HTML-escaped before any inline tags are synthesized; raw HTML
// tags in input Markdown are rendered literally as escaped text.
func renderMarkdownHTML(src string) template.HTML {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if strings.TrimSpace(src) == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	var out strings.Builder

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			i++
			continue
		}

		// Fenced code block.
		if strings.HasPrefix(trimmed, "```") {
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			i++
			var codeLines []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				codeLines = append(codeLines, lines[i])
				i++
			}
			if i < len(lines) {
				i++ // consume closing fence
			}
			out.WriteString("<pre><code")
			if lang != "" {
				out.WriteString(` class="language-`)
				out.WriteString(html.EscapeString(slugify(lang)))
				out.WriteString(`"`)
			}
			out.WriteString(">")
			out.WriteString(html.EscapeString(strings.Join(codeLines, "\n")))
			out.WriteString("</code></pre>\n")
			continue
		}

		// ATX Heading.
		if level, headingText, ok := parseATXHeading(trimmed); ok {
			id := slugify(headingText)
			fmt.Fprintf(&out, `<h%d id="%s">%s</h%d>`+"\n", level, html.EscapeString(id), renderInline(headingText), level)
			i++
			continue
		}

		// Horizontal rule.
		if isHorizontalRule(trimmed) {
			out.WriteString("<hr>\n")
			i++
			continue
		}

		// Blockquote.
		if strings.HasPrefix(trimmed, ">") {
			var qLines []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(t, ">") {
					break
				}
				qLines = append(qLines, strings.TrimSpace(strings.TrimPrefix(t, ">")))
				i++
			}
			out.WriteString("<blockquote><p>")
			out.WriteString(renderInline(strings.Join(qLines, " ")))
			out.WriteString("</p></blockquote>\n")
			continue
		}

		// GFM Table.
		if i+1 < len(lines) && isTableRow(trimmed) && isTableSeparator(strings.TrimSpace(lines[i+1])) {
			headers := splitTableCells(trimmed)
			i += 2
			out.WriteString("<table><thead><tr>")
			for _, h := range headers {
				out.WriteString("<th>")
				out.WriteString(renderInline(h))
				out.WriteString("</th>")
			}
			out.WriteString("</tr></thead><tbody>")
			for i < len(lines) && isTableRow(strings.TrimSpace(lines[i])) {
				cells := splitTableCells(strings.TrimSpace(lines[i]))
				out.WriteString("<tr>")
				for _, c := range cells {
					out.WriteString("<td>")
					out.WriteString(renderInline(c))
					out.WriteString("</td>")
				}
				out.WriteString("</tr>")
				i++
			}
			out.WriteString("</tbody></table>\n")
			continue
		}

		// Unordered list.
		if item, ok := parseUnorderedItem(trimmed); ok {
			out.WriteString("<ul>\n")
			out.WriteString("<li>")
			out.WriteString(renderInline(item))
			out.WriteString("</li>\n")
			i++
			for i < len(lines) {
				if next, ok := parseUnorderedItem(strings.TrimSpace(lines[i])); ok {
					out.WriteString("<li>")
					out.WriteString(renderInline(next))
					out.WriteString("</li>\n")
					i++
				} else {
					break
				}
			}
			out.WriteString("</ul>\n")
			continue
		}

		// Ordered list.
		if item, ok := parseOrderedItem(trimmed); ok {
			out.WriteString("<ol>\n")
			out.WriteString("<li>")
			out.WriteString(renderInline(item))
			out.WriteString("</li>\n")
			i++
			for i < len(lines) {
				if next, ok := parseOrderedItem(strings.TrimSpace(lines[i])); ok {
					out.WriteString("<li>")
					out.WriteString(renderInline(next))
					out.WriteString("</li>\n")
					i++
				} else {
					break
				}
			}
			out.WriteString("</ol>\n")
			continue
		}

		// Paragraph: accumulate until blank line or next block construct.
		var pLines []string
		for i < len(lines) {
			t := strings.TrimSpace(lines[i])
			if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, ">") || isHorizontalRule(t) {
				break
			}
			if _, _, ok := parseATXHeading(t); ok {
				break
			}
			if _, ok := parseUnorderedItem(t); ok {
				break
			}
			if _, ok := parseOrderedItem(t); ok {
				break
			}
			pLines = append(pLines, t)
			i++
		}
		out.WriteString("<p>")
		out.WriteString(renderInline(strings.Join(pLines, " ")))
		out.WriteString("</p>\n")
	}

	return template.HTML(out.String())
}

func parseATXHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	text := strings.TrimSpace(line[level+1:])
	text = strings.TrimRight(text, "# ")
	return level, text, true
}

func isHorizontalRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	ch := line[0]
	if ch != '-' && ch != '*' && ch != '_' {
		return false
	}
	count := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ch {
			count++
		} else if line[i] != ' ' {
			return false
		}
	}
	return count >= 3
}

func parseUnorderedItem(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):]), true
		}
	}
	return "", false
}

func parseOrderedItem(line string) (string, bool) {
	j := 0
	for j < len(line) && line[j] >= '0' && line[j] <= '9' {
		j++
	}
	if j == 0 || j+1 >= len(line) || line[j] != '.' || line[j+1] != ' ' {
		return "", false
	}
	return strings.TrimSpace(line[j+2:]), true
}

func isTableRow(line string) bool {
	return strings.Contains(line, "|")
}

func isTableSeparator(line string) bool {
	if !strings.Contains(line, "-") || !strings.Contains(line, "|") {
		return false
	}
	for _, r := range line {
		if r != '|' && r != '-' && r != ':' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func splitTableCells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// renderInline safely escapes raw text and renders inline Markdown (`code`,
// [label](href), **bold**, *italic*, ~~strike~~).
func renderInline(s string) string {
	var b strings.Builder
	n := len(s)
	i := 0
	for i < n {
		// Inline code span.
		if s[i] == '`' {
			end := strings.IndexByte(s[i+1:], '`')
			if end >= 0 {
				code := s[i+1 : i+1+end]
				b.WriteString("<code>")
				b.WriteString(html.EscapeString(code))
				b.WriteString("</code>")
				i = i + 2 + end
				continue
			}
		}

		// Markdown link [label](href).
		if s[i] == '[' {
			closeBracket := strings.IndexByte(s[i+1:], ']')
			if closeBracket >= 0 && i+1+closeBracket+1 < n && s[i+1+closeBracket+1] == '(' {
				label := s[i+1 : i+1+closeBracket]
				hrefStart := i + 1 + closeBracket + 2
				closeParen := strings.IndexByte(s[hrefStart:], ')')
				if closeParen >= 0 {
					href := strings.TrimSpace(s[hrefStart : hrefStart+closeParen])
					if isSafeHref(href) {
						b.WriteString(`<a href="`)
						b.WriteString(html.EscapeString(href))
						b.WriteString(`">`)
						b.WriteString(renderInline(label))
						b.WriteString("</a>")
					} else {
						b.WriteString(html.EscapeString(label))
					}
					i = hrefStart + closeParen + 1
					continue
				}
			}
		}

		// Bold **text**.
		if i+1 < n && s[i] == '*' && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "**")
			if end > 0 {
				b.WriteString("<strong>")
				b.WriteString(renderInline(s[i+2 : i+2+end]))
				b.WriteString("</strong>")
				i = i + 4 + end
				continue
			}
		}

		// Strikethrough ~~text~~.
		if i+1 < n && s[i] == '~' && s[i+1] == '~' {
			end := strings.Index(s[i+2:], "~~")
			if end > 0 {
				b.WriteString("<del>")
				b.WriteString(renderInline(s[i+2 : i+2+end]))
				b.WriteString("</del>")
				i = i + 4 + end
				continue
			}
		}

		// Italic *text*.
		if s[i] == '*' {
			end := strings.IndexByte(s[i+1:], '*')
			if end > 0 {
				b.WriteString("<em>")
				b.WriteString(renderInline(s[i+1 : i+1+end]))
				b.WriteString("</em>")
				i = i + 2 + end
				continue
			}
		}

		// Plain character (escaped).
		b.WriteString(html.EscapeString(s[i : i+1]))
		i++
	}
	return b.String()
}
