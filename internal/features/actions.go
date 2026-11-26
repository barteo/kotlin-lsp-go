package features

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// CodeActions returns the code actions for the range r of f: the quick
// fixes of the diagnostics given, which the client says are in r, the
// intentions at r, and organizing the imports. only, if set, limits them
// to the kinds it names or their subkinds.
func CodeActions(ctx context.Context, a *Analysis, c Completer, f *File, r protocol.Range, diags []protocol.Diagnostic, only []string) []protocol.CodeAction {
	out := []protocol.CodeAction{}
	want := func(kind string) bool {
		if len(only) == 0 {
			return true
		}
		for _, o := range only {
			if kind == o || strings.HasPrefix(kind, o+".") {
				return true
			}
		}
		return false
	}
	ac := &actions{ctx: ctx, a: a, c: c, f: f}
	if want(protocol.QuickFix) {
		for _, d := range diags {
			if d.Source != "kc" && d.Source != "kcls" {
				continue
			}
			for _, act := range ac.fixes(d) {
				act.Kind = protocol.QuickFix
				act.Diagnostics = []protocol.Diagnostic{d}
				out = append(out, act)
			}
		}
	}
	if want(protocol.RefactorRewrite) && a != nil {
		if start, end, err := f.Mapper.OffsetRange(r); err == nil {
			for _, act := range ac.intentions(start, end) {
				act.Kind = protocol.RefactorRewrite
				out = append(out, act)
			}
		}
	}
	if a != nil && (want(protocol.RefactorExtract) || want(protocol.RefactorInline)) {
		if start, end, err := f.Mapper.OffsetRange(r); err == nil {
			if act, ok := ac.extractVariable(start, end); ok && want(protocol.RefactorExtract) {
				act.Kind = protocol.RefactorExtract
				out = append(out, act)
			}
			if act, ok := ac.inlineVariable(start); ok && want(protocol.RefactorInline) {
				act.Kind = protocol.RefactorInline
				out = append(out, act)
			}
		}
	}
	if want(protocol.SourceOrganizeImports) {
		if edits := OrganizeImports(f); len(edits) > 0 {
			out = append(out, protocol.CodeAction{Title: "Organize imports", Kind: protocol.SourceOrganizeImports, Edit: ac.edit(edits...)})
		}
	}
	return out
}

type actions struct {
	ctx context.Context
	a   *Analysis
	c   Completer
	f   *File
}

func (ac *actions) edit(edits ...protocol.TextEdit) *protocol.WorkspaceEdit {
	return &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{ac.f.URI: edits}}
}

func (ac *actions) replace(start, end int, text string) protocol.TextEdit {
	return protocol.TextEdit{Range: ac.f.Mapper.Range(start, end), NewText: text}
}

func (ac *actions) fact(n psi.NodeRef) (analysis.Fact, bool) {
	if ac.a == nil {
		return analysis.Fact{}, false
	}
	af := ac.a.File(ac.f.Snapshot.Path())
	if af == nil || af.Snapshot != ac.f.Snapshot {
		return analysis.Fact{}, false
	}
	return ac.a.Result.Fact(analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: n.ID()})
}

// enclosing returns the innermost node of kind k at off, or an invalid
// one.
func enclosing(f *File, off int, kinds ...psi.Kind) psi.NodeRef {
	for n := f.Snapshot.NodeAt(off); n.Valid(); n = n.Parent() {
		if slices.Contains(kinds, n.NumericKind()) {
			return n
		}
	}
	return psi.NodeRef{}
}

// fixes returns the quick fixes of a diagnostic.
func (ac *actions) fixes(d protocol.Diagnostic) []protocol.CodeAction {
	f := ac.f
	start, end, err := f.Mapper.OffsetRange(d.Range)
	if err != nil {
		return nil
	}
	var out []protocol.CodeAction
	fix := func(title string, edits ...protocol.TextEdit) {
		out = append(out, protocol.CodeAction{Title: title, Edit: ac.edit(edits...)})
	}
	switch {
	case strings.HasPrefix(d.Message, "unresolved reference: "):
		out = append(out, ac.importFixes(start, end)...)
	case strings.HasPrefix(d.Message, "'when' expression must be exhaustive"), d.Code == "NO_ELSE_IN_WHEN",
		d.Code == "NON_EXHAUSTIVE_WHEN_STATEMENT":
		out = append(out, ac.whenFixes(start)...)
	case strings.Contains(d.Message, "is not abstract and does not implement abstract member"):
		if act, ok := ac.implementMembers(start); ok {
			out = append(out, act)
		}
	case d.Code == "UNUSED_IMPORT":
		if imp := enclosing(f, start, psi.KindIMPORT_DIRECTIVE); imp.Valid() {
			s, e := lineSpan(f.Mapper.Text(), imp.Range().Start, imp.Range().End)
			fix("Remove unused import", ac.replace(s, e, ""))
		}
	case d.Code == "UNUSED_VARIABLE":
		if p := enclosing(f, start, psi.KindPROPERTY); p.Valid() {
			fix("Remove variable '"+p.Name()+"'", ac.removeVariable(p))
		}
	case d.Code == "UNUSED_ANONYMOUS_PARAMETER" || d.Code == "UNUSED_DESTRUCTURED_PARAMETER_ENTRY":
		if p := enclosing(f, start, psi.KindVALUE_PARAMETER, psi.KindDESTRUCTURING_DECLARATION_ENTRY); p.Valid() && p.NameIdentifier().Valid() {
			id := p.NameIdentifier().Range()
			fix("Rename to _", ac.replace(id.Start, id.End, "_"))
		}
	case strings.HasPrefix(d.Code, "REDUNDANT_") && strings.HasSuffix(d.Code, "_MODIFIER"):
		if n := f.Snapshot.NodeAt(start); n.Valid() && n.IsLeaf() && psi.IsKeyword(n.NumericKind()) {
			fix("Remove redundant '"+n.Text()+"' modifier", ac.replace(n.Range().Start, skipSpaces(f.Mapper.Text(), n.Range().End), ""))
		}
	case d.Code == "UNNECESSARY_SAFE_CALL":
		if op := operatorAt(f, start, end, psi.KindSAFE_ACCESS); op.Valid() {
			fix("Replace '?.' with '.'", ac.replace(op.Range().Start, op.Range().End, "."))
		}
	case d.Code == "UNNECESSARY_NOT_NULL_ASSERTION":
		if op := operatorAt(f, start, end, psi.KindEXCLEXCL); op.Valid() {
			fix("Remove '!!'", ac.replace(op.Range().Start, op.Range().End, ""))
		}
	case d.Code == "USELESS_CAST":
		if cast := enclosing(f, start, psi.KindBINARY_WITH_TYPE); cast.Valid() {
			if x := cast.FirstChild(); x.Valid() {
				fix("Remove useless cast", ac.replace(x.Range().End, cast.Range().End, ""))
			}
		}
	}
	if d.Code != "" && d.Severity == protocol.SeverityWarning && !strings.HasPrefix(d.Code, "unsupported") && d.Code != "UNUSED_IMPORT" {
		out = append(out, ac.suppress(d.Code, start)...)
	}
	return out
}

// lineSpan widens [start, end) to the lines it is on, with their line
// break, if nothing else is on them.
func lineSpan(text string, start, end int) (int, int) {
	s := strings.LastIndexByte(text[:start], '\n') + 1
	e := end
	if i := strings.IndexByte(text[end:], '\n'); i >= 0 {
		e = end + i + 1
	} else {
		e = len(text)
	}
	if strings.TrimSpace(text[s:start]) != "" || strings.TrimSpace(text[end:e]) != "" {
		return start, end
	}
	return s, e
}

func skipSpaces(text string, off int) int {
	for off < len(text) && (text[off] == ' ' || text[off] == '\t') {
		off++
	}
	return off
}

// operatorAt returns the leaf of kind k in [start, end), or the first one
// after start.
func operatorAt(f *File, start, end int, k psi.Kind) psi.NodeRef {
	var found psi.NodeRef
	n := f.Snapshot.NodeAt(start)
	for n.Valid() && n.Range().End <= max(end, start+1) && !n.IsLeaf() {
		n = n.FirstChild()
	}
	for a := f.Snapshot.NodeAt(start); a.Valid(); a = a.Parent() {
		psi.WalkNodes(a, func(c psi.NodeRef) bool {
			if found.Valid() || c.Range().End < start {
				return false
			}
			if c.NumericKind() == k {
				found = c
				return false
			}
			return true
		})
		if found.Valid() {
			return found
		}
		if a.NumericKind() == psi.KindBLOCK || isDeclaration(a) {
			break
		}
	}
	return found
}

// importFixes returns the imports that would resolve the reference at
// [start, end): the names a completion of it would import.
func (ac *actions) importFixes(start, end int) []protocol.CodeAction {
	f := ac.f
	text := f.Mapper.Text()
	name := strings.Trim(text[start:end], "`")
	if !isWord(name) || ac.c == nil {
		return nil
	}
	l, err := Completion(ac.ctx, ac.c, f, f.Mapper.Position(end), CompletionOptions{})
	if err != nil || l == nil {
		return nil
	}
	var out []protocol.CodeAction
	seen := map[string]bool{}
	for _, it := range l.Items {
		if it.Label != name || len(it.AdditionalTextEdits) != 1 {
			continue
		}
		e := it.AdditionalTextEdits[0]
		fq := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e.NewText), "import"))
		if seen[fq] {
			continue
		}
		seen[fq] = true
		out = append(out, protocol.CodeAction{Title: "Import '" + fq + "'", Edit: ac.edit(e)})
	}
	if len(out) == 1 {
		out[0].IsPreferred = true
	}
	return out
}

// whenFixes returns the fixes of a when that isn't exhaustive at off:
// adding its missing branches, or an else branch.
func (ac *actions) whenFixes(off int) []protocol.CodeAction {
	f := ac.f
	w := enclosing(f, off, psi.KindWHEN)
	if !w.Valid() {
		return nil
	}
	rbrace := w.ChildKind(psi.KindRBRACE)
	if !rbrace.Valid() {
		return nil
	}
	text := f.Mapper.Text()
	indent := lineIndent(text, w.Range().Start)
	entry := indent + indentUnit
	// Insert at the start of the closing brace's line if it is alone on
	// it, else before the brace on a new line.
	at := rbrace.Range().Start
	prefix, suffix := "", ""
	if ls := strings.LastIndexByte(text[:at], '\n') + 1; strings.TrimSpace(text[ls:at]) == "" {
		at = ls
	} else {
		prefix, suffix = "\n", indent
	}
	branches := func(conds []string) string {
		var b strings.Builder
		b.WriteString(prefix)
		for _, c := range conds {
			b.WriteString(entry + c + " -> TODO()\n")
		}
		b.WriteString(suffix)
		return b.String()
	}
	var out []protocol.CodeAction
	if fa, ok := ac.fact(w); ok && fa.When != nil && len(fa.When.Missing) > 0 && fa.When.Category != "" {
		var conds []string
		qual := ""
		if fa.When.Category == "enum class" {
			if st := ac.subjectType(w); st != "" {
				qual = st + "."
			}
		}
		for _, m := range fa.When.Missing {
			switch {
			case m == "null" || m == "true" || m == "false" || strings.HasPrefix(m, "is "):
				conds = append(conds, m)
			default:
				conds = append(conds, qual+m)
			}
		}
		out = append(out, protocol.CodeAction{Title: "Add remaining branches", IsPreferred: true, Edit: ac.edit(ac.replace(at, at, branches(conds)))})
	}
	out = append(out, protocol.CodeAction{Title: "Add 'else' branch", Edit: ac.edit(ac.replace(at, at, branches([]string{"else"})))})
	return out
}

// subjectType returns the simple name of the type of a when's subject.
func (ac *actions) subjectType(w psi.NodeRef) string {
	for c := w.FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.IsLeaf() || c.IsTrivia() {
			continue
		}
		if c.NumericKind() == psi.KindWHEN_ENTRY {
			break
		}
		n := c
		if n.NumericKind() == psi.KindPROPERTY {
			if fa, ok := ac.fact(n); ok && fa.Symbol != 0 {
				if s, ok := ac.a.Result.Symbol(fa.Symbol); ok {
					return simpleTypeName(s.Type.Name)
				}
			}
			continue
		}
		if fa, ok := ac.fact(n); ok && fa.Type != nil {
			return simpleTypeName(fa.Type.Name)
		}
	}
	return ""
}

// simpleTypeName returns a class's name as code in its package writes
// it: Outer.Inner for pkg.Outer.Inner.
func simpleTypeName(fq string) string {
	parts := strings.Split(fq, ".")
	for i, p := range parts {
		if p != "" && p[0] >= 'A' && p[0] <= 'Z' {
			return strings.Join(parts[i:], ".")
		}
	}
	return parts[len(parts)-1]
}

// implementMembers returns the fix that implements the abstract members
// of the class or object declared at off.
func (ac *actions) implementMembers(off int) (protocol.CodeAction, bool) {
	f := ac.f
	cls := enclosing(f, off, psi.KindCLASS, psi.KindOBJECT_DECLARATION)
	if !cls.Valid() || ac.c == nil {
		return protocol.CodeAction{}, false
	}
	text := f.Mapper.Text()
	indent := lineIndent(text, cls.Range().Start)
	body := cls.ChildKind(psi.KindCLASS_BODY)
	var at int
	var before, after string
	if body.Valid() {
		rb := body.ChildKind(psi.KindRBRACE)
		if !rb.Valid() {
			return protocol.CodeAction{}, false
		}
		at = rb.Range().Start
	} else {
		at = cls.Range().End
		before, after = " {\n", "\n}"
	}
	a, items, err := superMembers(ac.ctx, ac.c, f, at, at, before, after, nil)
	if err != nil || a == nil {
		return protocol.CodeAction{}, false
	}
	var stubs []string
	for _, ci := range items {
		if ci.Modality == "abstract" {
			_, code := overrideCode(ci, indent+indentUnit)
			stubs = append(stubs, indent+indentUnit+code)
		}
	}
	if len(stubs) == 0 {
		return protocol.CodeAction{}, false
	}
	code := strings.Join(stubs, "\n\n")
	var e protocol.TextEdit
	if body.Valid() {
		rb := body.ChildKind(psi.KindRBRACE)
		lb := body.ChildKind(psi.KindLBRACE)
		inner := strings.TrimSpace(text[lb.Range().End:rb.Range().Start])
		switch {
		case inner == "":
			e = ac.replace(lb.Range().End, rb.Range().Start, "\n"+code+"\n"+indent)
		default:
			ls := strings.LastIndexByte(text[:rb.Range().Start], '\n') + 1
			if strings.TrimSpace(text[ls:rb.Range().Start]) == "" {
				e = ac.replace(ls, ls, "\n"+code+"\n")
			} else {
				e = ac.replace(rb.Range().Start, rb.Range().Start, "\n\n"+code+"\n"+indent)
			}
		}
	} else {
		e = ac.replace(at, at, " {\n"+code+"\n"+indent+"}")
	}
	return protocol.CodeAction{Title: "Implement members", IsPreferred: true, Edit: ac.edit(e)}, true
}

// removeVariable returns the edit that removes an unused local: its line,
// or, if its initializer calls something, all but the initializer.
func (ac *actions) removeVariable(p psi.NodeRef) protocol.TextEdit {
	text := ac.f.Mapper.Text()
	eq := p.ChildKind(psi.KindEQ)
	var init psi.NodeRef
	if eq.Valid() {
		for c := eq.NextSibling(); c.Valid(); c = c.NextSibling() {
			if !c.IsTrivia() {
				init = c
				break
			}
		}
	}
	if init.Valid() && hasCall(init) {
		return ac.replace(p.Range().Start, init.Range().Start, "")
	}
	s, e := lineSpan(text, p.Range().Start, p.Range().End)
	return ac.replace(s, e, "")
}

// hasCall reports whether an expression calls a function: it may have an
// effect.
func hasCall(x psi.NodeRef) bool {
	found := false
	psi.WalkNodes(x, func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindCALL_EXPRESSION, psi.KindBINARY_EXPRESSION, psi.KindPREFIX_EXPRESSION, psi.KindPOSTFIX_EXPRESSION,
			psi.KindARRAY_ACCESS_EXPRESSION:
			found = true
		case psi.KindDOT_QUALIFIED_EXPRESSION, psi.KindSAFE_ACCESS_EXPRESSION:
			found = true // a property's getter may have an effect too
		}
		return !found
	})
	return found
}

// suppress returns the fixes that suppress the warning name at off: with
// @Suppress on the declarations around it, innermost first, and on the
// file.
func (ac *actions) suppress(name string, off int) []protocol.CodeAction {
	f := ac.f
	text := f.Mapper.Text()
	var out []protocol.CodeAction
	for n := f.Snapshot.NodeAt(off); n.Valid() && len(out) < 2; n = n.Parent() {
		switch n.NumericKind() {
		case psi.KindFUN, psi.KindPROPERTY, psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindSECONDARY_CONSTRUCTOR:
		default:
			continue
		}
		what := map[psi.Kind]string{psi.KindFUN: "function", psi.KindPROPERTY: "property", psi.KindCLASS: "class",
			psi.KindOBJECT_DECLARATION: "object", psi.KindSECONDARY_CONSTRUCTOR: "constructor"}[n.NumericKind()]
		if n.Name() != "" {
			what += " " + n.Name()
		}
		if e, ok := ac.addSuppress(n.ChildKind(psi.KindMODIFIER_LIST), name); ok {
			out = append(out, protocol.CodeAction{Title: fmt.Sprintf("Suppress '%s' for %s", name, what), Edit: ac.edit(e)})
			continue
		}
		at := n.Range().Start
		for c := n.FirstChild(); c.Valid(); c = c.NextSibling() {
			if c.NumericKind() != psi.KindKDOC && !c.IsTrivia() {
				at = c.Range().Start
				break
			}
		}
		e := ac.replace(at, at, "@Suppress(\""+name+"\")\n"+lineIndent(text, at))
		out = append(out, protocol.CodeAction{Title: fmt.Sprintf("Suppress '%s' for %s", name, what), Edit: ac.edit(e)})
	}
	root := f.Snapshot.RootNode()
	fileName := f.Snapshot.Path()[strings.LastIndexAny(f.Snapshot.Path(), "/\\")+1:]
	title := fmt.Sprintf("Suppress '%s' for file %s", name, fileName)
	if e, ok := ac.addSuppress(root.ChildKind(psi.KindFILE_ANNOTATION_LIST), name); ok {
		return append(out, protocol.CodeAction{Title: title, Edit: ac.edit(e)})
	}
	at := 0
	if pkg := root.ChildKind(psi.KindPACKAGE_DIRECTIVE); pkg.Valid() && pkg.Range().End > pkg.Range().Start {
		at = pkg.Range().Start
	}
	return append(out, protocol.CodeAction{Title: title, Edit: ac.edit(ac.replace(at, at, "@file:Suppress(\""+name+"\")\n\n"))})
}

// addSuppress returns the edit that adds name to a @Suppress annotation
// in mods, if it has one.
func (ac *actions) addSuppress(mods psi.NodeRef, name string) (protocol.TextEdit, bool) {
	for c := mods.FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() != psi.KindANNOTATION_ENTRY {
			continue
		}
		t := c.ChildKind(psi.KindCONSTRUCTOR_CALLEE).ChildKind(psi.KindTYPE_REFERENCE).ChildKind(psi.KindUSER_TYPE)
		if t.ChildKind(psi.KindREFERENCE_EXPRESSION).Name() != "Suppress" {
			continue
		}
		args := c.ChildKind(psi.KindVALUE_ARGUMENT_LIST)
		rpar := closingParen(args)
		if !rpar.Valid() {
			continue
		}
		sep := ", "
		if strings.TrimSpace(ac.f.Mapper.Text()[args.Range().Start+1:rpar.Range().Start]) == "" {
			sep = ""
		}
		at := rpar.Range().Start
		return ac.replace(at, at, sep+"\""+name+"\""), true
	}
	return protocol.TextEdit{}, false
}

// intentions returns the rewrites at [start, end): specifying an inferred
// type, and naming a call's arguments.
func (ac *actions) intentions(start, end int) []protocol.CodeAction {
	var out []protocol.CodeAction
	f := ac.f
	// From the first token of a selection.
	text := f.Mapper.Text()
	for start < end && start < len(text) && strings.ContainsRune(" \t\r\n", rune(text[start])) {
		start++
	}
	if d := enclosing(f, start, psi.KindPROPERTY, psi.KindFUN, psi.KindBLOCK, psi.KindCLASS_BODY); d.NumericKind() == psi.KindPROPERTY || d.NumericKind() == psi.KindFUN {
		if act, ok := ac.specifyType(d, start, end); ok {
			out = append(out, act)
		}
	}
	if call := enclosing(f, start, psi.KindCALL_EXPRESSION, psi.KindBLOCK, psi.KindCLASS_BODY); call.NumericKind() == psi.KindCALL_EXPRESSION {
		if act, ok := ac.nameArguments(call); ok {
			out = append(out, act)
		}
	}
	return out
}

// specifyType returns the rewrite that writes the inferred type of a
// property or an expression-bodied function, when [start, end) is on its
// header.
func (ac *actions) specifyType(d psi.NodeRef, start, end int) (protocol.CodeAction, bool) {
	name := d.NameIdentifier()
	if !name.Valid() || d.TypeReference().Valid() || start > name.Range().End {
		return protocol.CodeAction{}, false
	}
	after := name
	if d.NumericKind() == psi.KindFUN {
		ps := d.ChildKind(psi.KindVALUE_PARAMETER_LIST)
		if !ps.Valid() || !d.ChildKind(psi.KindEQ).Valid() {
			return protocol.CodeAction{}, false
		}
		after = ps
	}
	fa, ok := ac.fact(d)
	if !ok || fa.Symbol == 0 {
		return protocol.CodeAction{}, false
	}
	s, ok := ac.a.Result.Symbol(fa.Symbol)
	if !ok || s.Type.Name == "" || s.Type.Name == "kotlin.Nothing" {
		return protocol.CodeAction{}, false
	}
	t := declarable(s.Type)
	edits := []protocol.TextEdit{ac.replace(after.Range().End, after.Range().End, ": "+RenderType(t, false))}
	for _, fq := range typeNames(t) {
		if !ac.inScope(fq) {
			if e, ok := importEdit(ac.f, fq); ok {
				edits = append(edits, e)
			}
		}
	}
	return protocol.CodeAction{Title: "Specify type explicitly", Edit: ac.edit(edits...)}, true
}

// typeNames returns the classes a type names, its arguments' too.
func typeNames(t analysis.Type) []string {
	var out []string
	if !t.Parameter && t.Name != "" && !strings.HasPrefix(t.Name, "kotlin.Function") {
		out = append(out, t.Name)
	}
	for _, a := range t.Arguments {
		out = append(out, typeNames(a)...)
	}
	return out
}

// inScope reports whether the file can name the class fq by the last
// part of its name without another import.
func (ac *actions) inScope(fq string) bool {
	i := strings.LastIndexByte(fq, '.')
	if i < 0 {
		return true
	}
	pkg := fq[:i] // or the outer class of a nested one, which RenderType writes by its own name
	root := ac.f.Snapshot.RootNode()
	if defaultImports[pkg] || root.ChildKind(psi.KindPACKAGE_DIRECTIVE).Path() == pkg {
		return true
	}
	for c := root.ChildKind(psi.KindIMPORT_LIST).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() == psi.KindIMPORT_DIRECTIVE && (c.Path() == fq && c.Alias() == "" || c.IsAllUnder() && c.Path() == pkg) {
			return true
		}
	}
	return false
}

// nameArguments returns the rewrite that names a call's positional
// arguments.
func (ac *actions) nameArguments(call psi.NodeRef) (protocol.CodeAction, bool) {
	list := call.ChildKind(psi.KindVALUE_ARGUMENT_LIST)
	fa, ok := ac.fact(call)
	if !list.Valid() || !ok || fa.Call == nil || !fa.Call.StableNames {
		return protocol.CodeAction{}, false
	}
	c := fa.Call
	var edits []protocol.TextEdit
	i := -1
	for arg := list.FirstChild(); arg.Valid(); arg = arg.NextSibling() {
		if arg.NumericKind() != psi.KindVALUE_ARGUMENT {
			continue
		}
		i++
		if arg.ChildKind(psi.KindVALUE_ARGUMENT_NAME).Valid() {
			continue
		}
		if i >= len(c.ArgumentMapping) {
			break
		}
		p := c.ArgumentMapping[i]
		if p < 0 || p >= len(c.ParameterNames) || c.ParameterNames[p] == "" || p < len(c.Varargs) && c.Varargs[p] {
			break // a vararg's arguments can't be named but by spreading an array
		}
		at := arg.Range().Start
		edits = append(edits, ac.replace(at, at, quoteName(c.ParameterNames[p])+" = "))
	}
	if len(edits) == 0 {
		return protocol.CodeAction{}, false
	}
	return protocol.CodeAction{Title: "Add names to call arguments", Edit: ac.edit(edits...)}, true
}
