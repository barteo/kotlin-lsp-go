package features

import (
	"regexp"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// OrganizeImports returns the edits that remove f's unused and duplicate
// imports and sort the rest as IntelliJ does: others, then java, javax,
// kotlin, then aliases. It leaves an import list with comments in it
// alone.
func OrganizeImports(f *File) []protocol.TextEdit {
	list := f.Snapshot.RootNode().ChildKind(psi.KindIMPORT_LIST)
	if !list.Valid() || list.Range().End == list.Range().Start {
		return nil
	}
	var imports []psi.NodeRef
	for c := list.FirstChild(); c.Valid(); c = c.NextSibling() {
		switch c.NumericKind() {
		case psi.KindIMPORT_DIRECTIVE:
			imports = append(imports, c)
		case psi.KindWHITE_SPACE:
		default:
			return nil
		}
	}
	unused := map[psi.NodeRef]bool{}
	for _, imp := range UnusedImports(f) {
		unused[imp] = true
	}
	seen := map[string]bool{}
	var keep []string
	for _, imp := range imports {
		line := importText(imp)
		if unused[imp] || seen[line] || line == "" {
			continue
		}
		seen[line] = true
		keep = append(keep, line)
	}
	slices.SortStableFunc(keep, func(a, b string) int {
		if ga, gb := importGroup(a), importGroup(b); ga != gb {
			return ga - gb
		}
		return strings.Compare(a, b)
	})
	text := f.Mapper.Text()
	start, end := list.Range().Start, list.Range().End
	if len(keep) == 0 {
		// Up to what follows, so the package directive and the
		// declarations keep the blank line between them.
		for end < len(text) && strings.ContainsRune(" \t\r\n", rune(text[end])) {
			end++
		}
		if end == len(text) && start > 0 {
			start = len(strings.TrimRight(text[:start], " \t\r\n")) + 1
		}
		return []protocol.TextEdit{{Range: f.Mapper.Range(start, end), NewText: ""}}
	}
	repl := "import " + strings.Join(keep, "\nimport ")
	if repl == text[start:end] {
		return nil
	}
	return []protocol.TextEdit{{Range: f.Mapper.Range(start, end), NewText: repl}}
}

// importText returns what follows import in an import directive, in a
// normal form: a.b.C, a.b.*, a.b.C as D.
func importText(imp psi.NodeRef) string {
	p := imp.Path()
	if p == "" {
		return ""
	}
	switch {
	case imp.IsAllUnder():
		return p + ".*"
	case imp.Alias() != "":
		return p + " as " + imp.Alias()
	}
	return p
}

func importGroup(s string) int {
	switch {
	case strings.Contains(s, " as "):
		return 4
	case strings.HasPrefix(s, "java."):
		return 1
	case strings.HasPrefix(s, "javax."):
		return 2
	case strings.HasPrefix(s, "kotlin."):
		return 3
	}
	return 0
}

var importKDocLink = regexp.MustCompile(`\[([\p{L}_][\p{L}\p{N}_]*)`)

// UnusedImports returns f's imports that nothing in it names: no
// identifier outside the imports and package directive, and no KDoc link.
// All-under imports, and imports of operator functions that code calls
// without naming them, count as used.
func UnusedImports(f *File) []psi.NodeRef {
	root := f.Snapshot.RootNode()
	list := root.ChildKind(psi.KindIMPORT_LIST)
	if !list.Valid() {
		return nil
	}
	used := map[string]bool{}
	psi.WalkNodes(root, func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindIMPORT_LIST, psi.KindPACKAGE_DIRECTIVE:
			return false
		case psi.KindKDOC:
			for _, m := range importKDocLink.FindAllStringSubmatch(n.Text(), -1) {
				used[m[1]] = true
			}
			return false
		case psi.KindIDENTIFIER:
			used[strings.Trim(n.Text(), "`")] = true
		}
		return true
	})
	var out []psi.NodeRef
	for c := list.FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() != psi.KindIMPORT_DIRECTIVE || c.IsAllUnder() || c.Path() == "" {
			continue
		}
		name := c.Alias()
		if name == "" {
			p := c.Path()
			name = strings.Trim(p[strings.LastIndexByte(p, '.')+1:], "`")
		}
		if !used[name] && !operatorName(name) {
			out = append(out, c)
		}
	}
	return out
}

// operatorName reports whether code may call a function of this name by
// an operator or convention, without naming it.
func operatorName(name string) bool {
	switch name {
	case "get", "set", "getValue", "setValue", "provideDelegate", "invoke", "contains", "iterator", "next", "hasNext",
		"compareTo", "equals", "plus", "minus", "times", "div", "rem", "mod", "rangeTo", "rangeUntil", "inc", "dec",
		"unaryPlus", "unaryMinus", "not", "plusAssign", "minusAssign", "timesAssign", "divAssign", "remAssign", "modAssign":
		return true
	}
	if n, ok := strings.CutPrefix(name, "component"); ok && n != "" && strings.Trim(n, "0123456789") == "" {
		return true
	}
	return false
}

// unusedImportDiagnostics reports f's unused imports.
func unusedImportDiagnostics(f *File) []protocol.Diagnostic {
	var out []protocol.Diagnostic
	for _, imp := range UnusedImports(f) {
		out = append(out, protocol.Diagnostic{
			Range:    f.Mapper.Range(imp.Range().Start, imp.Range().End),
			Severity: protocol.SeverityHint,
			Code:     "UNUSED_IMPORT",
			Source:   "kcls",
			Message:  "unused import directive",
			Tags:     []protocol.DiagnosticTag{protocol.TagUnnecessary},
		})
	}
	return out
}
