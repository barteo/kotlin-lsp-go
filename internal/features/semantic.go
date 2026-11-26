package features

import (
	"context"
	"strings"
	"unicode"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// target is what a position in a file refers to.
type target struct {
	f      *File       // the analyzed file
	origin psi.NodeRef // the reference, type or declaration name at the position
	node   psi.NodeRef // the node whose fact it is
	fact   analysis.Fact
	sym    analysis.Symbol // zero if the fact has none
	decl   bool            // the position is on a declaration's name
}

// targetAt returns what the position pos in f refers to, in a's facts.
func targetAt(a *Analysis, f *File, pos protocol.Position) (target, bool) {
	if a == nil {
		return target{}, false
	}
	af := a.File(f.Snapshot.Path())
	if af == nil {
		return target{}, false
	}
	off, err := af.Mapper.Offset(pos)
	if err != nil {
		return target{}, false
	}
	leaf := identifierAt(af.Snapshot, off)
	if !leaf.Valid() {
		return target{}, false
	}
	t := target{f: af, origin: leaf}
	fact := func(n psi.NodeRef) (analysis.Fact, bool) {
		return a.Result.Fact(analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: n.ID()})
	}
	if p := leaf.Parent(); isDeclaration(p) && p.NameIdentifier() == leaf {
		if fa, ok := fact(p); ok && fa.Symbol != 0 {
			t.node, t.fact, t.decl = p, fa, true
			t.sym, _ = a.Result.Symbol(fa.Symbol)
			return t, true
		}
	}
	for n, depth := leaf.Parent(), 0; n.Valid() && depth < 4; n, depth = n.Parent(), depth+1 {
		switch n.NumericKind() {
		case psi.KindREFERENCE_EXPRESSION, psi.KindUSER_TYPE, psi.KindTYPE_REFERENCE, psi.KindNULLABLE_TYPE,
			psi.KindOPERATION_REFERENCE, psi.KindTHIS_EXPRESSION, psi.KindSUPER_EXPRESSION,
			psi.KindCONSTRUCTOR_CALLEE, psi.KindLABEL_QUALIFIER:
		default:
			return target{}, false
		}
		fa, ok := fact(n)
		if !ok || fa.Symbol == 0 && fa.Call == nil && fa.Type == nil {
			continue
		}
		if fa.Symbol == 0 && fa.Call == nil && isCallee(n) {
			// An unresolved call: a callee's type is its receiver's.
			return target{}, false
		}
		t.node, t.fact = n, fa
		if n.NumericKind() == psi.KindREFERENCE_EXPRESSION || n.NumericKind() == psi.KindOPERATION_REFERENCE {
			t.origin = n
		}
		if fa.Symbol != 0 {
			t.sym, _ = a.Result.Symbol(fa.Symbol)
		}
		return t, true
	}
	return target{}, false
}

// isCallee reports whether a reference is the callee of a call.
func isCallee(n psi.NodeRef) bool {
	p := n.Parent()
	return n.NumericKind() == psi.KindREFERENCE_EXPRESSION && p.NumericKind() == psi.KindCALL_EXPRESSION && p.FirstChild() == n
}

// identifierAt returns the identifier or keyword leaf at off, or the one
// that ends there.
func identifierAt(s *psi.Snapshot, off int) psi.NodeRef {
	for _, o := range []int{off, off - 1} {
		if o < 0 {
			continue
		}
		n := s.NodeAt(o)
		if n.Valid() && n.IsLeaf() && !n.IsTrivia() && isWord(n.Text()) {
			return n
		}
	}
	return psi.NodeRef{}
}

func isWord(s string) bool {
	s = strings.Trim(s, "`")
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

// Definition returns where the symbol at pos is declared: in the
// sources, or in a library's sources jar.
func Definition(ctx context.Context, a *Analysis, f *File, pos protocol.Position) []protocol.LocationLink {
	t, ok := targetAt(a, f, pos)
	if !ok {
		return nil
	}
	origin := t.f.Range(t.origin)
	if t.sym.Declaration != nil {
		if df, dn, ok := a.Node(*t.sym.Declaration); ok {
			return []protocol.LocationLink{declLink(origin, df, dn)}
		}
	}
	if t.sym.Kind == "local" || t.sym.Kind == "parameter" {
		return nil
	}
	if t.sym.Kind == "" && t.fact.Call == nil && t.fact.Type != nil {
		// A type reference: its class.
		return typeLinks(ctx, a, origin, *t.fact.Type)
	}
	if lt, ok := findLibrary(ctx, a, t); ok {
		return []protocol.LocationLink{libraryLink(origin, lt)}
	}
	return nil
}

// TypeDefinition returns where the type of the expression or declaration
// at pos is declared.
func TypeDefinition(ctx context.Context, a *Analysis, f *File, pos protocol.Position) []protocol.LocationLink {
	t, ok := targetAt(a, f, pos)
	if !ok {
		return nil
	}
	typ := t.fact.Type
	if t.sym.Kind != "" && (t.decl || typ == nil || t.fact.Call != nil) {
		typ = &t.sym.Type
		if t.fact.Call != nil && t.fact.Call.ReturnType != nil {
			typ = t.fact.Call.ReturnType
		}
	}
	if typ == nil || typ.Name == "" {
		return nil
	}
	return typeLinks(ctx, a, t.f.Range(t.origin), *typ)
}

func typeLinks(ctx context.Context, a *Analysis, origin protocol.Range, typ analysis.Type) []protocol.LocationLink {
	if typ.Parameter {
		if typ.ParameterOwner != nil {
			if df, dn, ok := a.Node(*typ.ParameterOwner); ok {
				for _, tp := range typeParameters(dn) {
					if tp.Name() == typ.Name {
						return []protocol.LocationLink{declLink(origin, df, tp)}
					}
				}
			}
		}
		return nil
	}
	if cls, ok := a.Class(typ.Name); ok && cls.Declaration != nil {
		if df, dn, ok := a.Node(*cls.Declaration); ok {
			return []protocol.LocationLink{declLink(origin, df, dn)}
		}
	}
	if a.Libraries == nil {
		return nil
	}
	if lt, ok := a.Libraries.Find(ctx, LibraryDecl{Kind: "class", Class: binaryName(typ.Name), Parameters: -1}); ok {
		return []protocol.LocationLink{libraryLink(origin, lt)}
	}
	return nil
}

func typeParameters(decl psi.NodeRef) []psi.NodeRef {
	var out []psi.NodeRef
	for c := decl.ChildKind(psi.KindTYPE_PARAMETER_LIST).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() == psi.KindTYPE_PARAMETER {
			out = append(out, c)
		}
	}
	return out
}

// findLibrary finds the target's declaration in the libraries' sources.
func findLibrary(ctx context.Context, a *Analysis, t target) (LibraryTarget, bool) {
	if a.Libraries == nil {
		return LibraryTarget{}, false
	}
	d, ok := libraryDecl(t)
	if !ok {
		return LibraryTarget{}, false
	}
	return a.Libraries.Find(ctx, d)
}

// libraryDecl names the target's declaration for Libraries.Find.
func libraryDecl(t target) (LibraryDecl, bool) {
	params := -1
	if t.fact.Call != nil {
		params = len(t.fact.Call.Parameters)
	}
	switch {
	case t.sym.Kind == "class":
		return LibraryDecl{Kind: "class", Class: t.sym.Name, QualifiedName: t.sym.QualifiedName, Parameters: -1}, true
	case t.sym.Kind != "" && t.sym.QualifiedName != "":
		return LibraryDecl{Kind: t.sym.Kind, QualifiedName: t.sym.QualifiedName, Owner: t.sym.Owner, Parameters: params}, true
	case t.fact.Call != nil && t.fact.Call.Name != "":
		// A call without a symbol, as of a built-in member: pkg.C.m.
		name := t.fact.Call.Name
		i := strings.LastIndexByte(name, '.')
		if i < 0 {
			return LibraryDecl{}, false
		}
		owner := ""
		if seg := name[strings.LastIndexByte(name[:i], '.')+1 : i]; seg != "" && unicode.IsUpper(rune(seg[0])) {
			owner = binaryName(name[:i])
		}
		kind := "function"
		if strings.HasSuffix(name, ".<init>") {
			kind, owner = "constructor", binaryName(name[:i])
		}
		return LibraryDecl{Kind: kind, QualifiedName: name, Owner: owner, Parameters: params}, true
	}
	return LibraryDecl{}, false
}

// binaryName guesses the JVM binary name of a class's qualified name:
// the segments before the first capitalized one are its package.
func binaryName(fq string) string {
	parts := strings.Split(fq, ".")
	for i, p := range parts {
		if p != "" && unicode.IsUpper(rune(p[0])) {
			return strings.Join(parts[:i], "/") + "/" + strings.Join(parts[i:], "$")
		}
	}
	return strings.ReplaceAll(fq, ".", "/")
}

func declLink(origin protocol.Range, df *File, dn psi.NodeRef) protocol.LocationLink {
	sel := dn
	if id := dn.NameIdentifier(); id.Valid() {
		sel = id
	}
	return protocol.LocationLink{OriginSelectionRange: &origin, TargetURI: df.URI, TargetRange: df.Range(declRange(dn)), TargetSelectionRange: df.Range(sel)}
}

// declRange returns the node whose range a declaration's is: the
// declaration less its KDoc.
func declRange(dn psi.NodeRef) psi.NodeRef { return dn }

func libraryLink(origin protocol.Range, lt LibraryTarget) protocol.LocationLink {
	if lt.Node.Valid() {
		return declLink(origin, lt.File, lt.Node)
	}
	r := lt.File.Mapper.Range(lt.Start, lt.End)
	return protocol.LocationLink{OriginSelectionRange: &origin, TargetURI: lt.File.URI, TargetRange: r, TargetSelectionRange: r}
}

// Locations turns links into locations, for a client without link
// support: the name of each definition.
func Locations(links []protocol.LocationLink) []protocol.Location {
	out := make([]protocol.Location, 0, len(links))
	for _, l := range links {
		out = append(out, protocol.Location{URI: l.TargetURI, Range: l.TargetSelectionRange})
	}
	return out
}

// DocumentHighlights returns the occurrences in f of the declaration at
// pos: its declaration, if in f, and its references, reads and writes.
func DocumentHighlights(a *Analysis, f *File, pos protocol.Position) []protocol.DocumentHighlight {
	k, ok := KeyAt(a, f, pos)
	if !ok {
		return nil
	}
	var out []protocol.DocumentHighlight
	for _, o := range Occurrences(a, f, k) {
		kind := protocol.HighlightRead
		switch o.Kind {
		case OccurrenceDeclaration:
			kind = protocol.HighlightText
		case OccurrenceWrite:
			kind = protocol.HighlightWrite
		}
		out = append(out, protocol.DocumentHighlight{Range: o.Range(), Kind: kind})
	}
	return out
}

// isAssigned reports whether a reference is the left side of an
// assignment, x = …, x += …, or an increment.
func isAssigned(ref psi.NodeRef) bool {
	p := ref.Parent()
	switch p.NumericKind() {
	case psi.KindBINARY_EXPRESSION:
		if p.FirstChild() != ref {
			return false
		}
		op := p.ChildKind(psi.KindOPERATION_REFERENCE).Text()
		return op == "=" || op == "+=" || op == "-=" || op == "*=" || op == "/=" || op == "%="
	case psi.KindPOSTFIX_EXPRESSION, psi.KindPREFIX_EXPRESSION:
		op := p.ChildKind(psi.KindOPERATION_REFERENCE).Text()
		return op == "++" || op == "--"
	}
	return false
}

// Hover returns the signature and documentation of the symbol at pos, or
// the type of the expression there.
func Hover(ctx context.Context, a *Analysis, f *File, pos protocol.Position) *protocol.Hover {
	t, ok := targetAt(a, f, pos)
	if !ok {
		return nil
	}
	r := t.f.Range(t.origin)
	var sig, doc, where string
	switch {
	case t.sym.Kind != "":
		var dn psi.NodeRef
		if t.sym.Declaration != nil {
			_, dn, _ = a.Node(*t.sym.Declaration)
		}
		var lt LibraryTarget
		if !dn.Valid() && t.sym.Kind != "local" && t.sym.Kind != "parameter" {
			if x, ok := findLibrary(ctx, a, t); ok {
				lt = x
				dn = x.Node
			}
		}
		if dn.Valid() && dn.NumericKind() != psi.KindFILE {
			sig = declSignature(dn, t.sym)
			doc = kdoc(dn)
		} else {
			sig = symbolSignature(t.sym, t.fact.Call)
			if lt.Doc != "" {
				doc = javadoc(lt.Doc)
			}
		}
		where = container(t.sym)
	case t.fact.Call != nil:
		sig = callSignature(t.fact.Call)
		if lt, ok := findLibrary(ctx, a, t); ok {
			if lt.Node.Valid() {
				sig, doc = declSignature(lt.Node, analysis.Symbol{}), kdoc(lt.Node)
			} else if lt.Doc != "" {
				doc = javadoc(lt.Doc)
			}
		}
	case t.fact.Type != nil:
		if cls, ok := a.Class(t.fact.Type.Name); ok && cls.Declaration != nil {
			if _, dn, ok := a.Node(*cls.Declaration); ok {
				sig, doc, where = declSignature(dn, cls), kdoc(dn), container(cls)
				break
			}
		}
		sig = RenderType(*t.fact.Type, true)
	default:
		return nil
	}
	if sig == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("```kotlin\n" + sig + "\n```")
	if where != "" {
		b.WriteString("\n\n*" + where + "*")
	}
	if doc != "" {
		b.WriteString("\n\n---\n\n" + doc)
	}
	return &protocol.Hover{Contents: protocol.MarkupContent{Kind: "markdown", Value: b.String()}, Range: &r}
}

// container names where a symbol is declared: a member's class, a
// top-level declaration's package.
func container(s analysis.Symbol) string {
	switch s.Kind {
	case "local", "parameter", "":
		return ""
	}
	q := s.QualifiedName
	if s.Kind == "class" {
		q = strings.ReplaceAll(strings.ReplaceAll(s.Name, "/", "."), "$", ".")
	}
	if s.Kind == "constructor" {
		q = strings.TrimSuffix(q, ".<init>")
	}
	i := strings.LastIndexByte(q, '.')
	if i < 0 {
		return ""
	}
	return q[:i]
}

// declSignature returns a declaration's header, as written up to its
// body, less its KDoc and its annotations' arguments, with the type kc
// inferred for a property or function that doesn't write one.
func declSignature(dn psi.NodeRef, sym analysis.Symbol) string {
	hasType := false
	stopped := false
	s := compactWith(dn, func(n psi.NodeRef) walk {
		if stopped {
			return skipNode
		}
		if n.Parent() == dn {
			switch n.NumericKind() {
			case psi.KindKDOC:
				return skipNode
			case psi.KindBLOCK, psi.KindCLASS_BODY, psi.KindEQ, psi.KindPROPERTY_DELEGATE,
				psi.KindPROPERTY_ACCESSOR, psi.KindBY_KEYWORD:
				stopped = true
				return skipNode
			case psi.KindTYPE_REFERENCE:
				hasType = true
			case psi.KindCOLON:
				switch dn.NumericKind() {
				case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
					return spacedColon
				}
			}
		}
		if n.NumericKind() == psi.KindVALUE_ARGUMENT_LIST && n.Parent().NumericKind() == psi.KindANNOTATION_ENTRY {
			return skipNode
		}
		return visit
	})
	switch dn.NumericKind() {
	case psi.KindPROPERTY, psi.KindFUN:
		if !hasType && sym.Type.Name != "" {
			s += ": " + RenderType(sym.Type, false)
		}
	}
	return s
}

// symbolSignature renders a symbol without its declaration, as a
// library's without sources.
func symbolSignature(s analysis.Symbol, call *analysis.Call) string {
	typ := ""
	if s.Type.Name != "" {
		typ = ": " + RenderType(s.Type, false)
	}
	switch s.Kind {
	case "class":
		return "class " + s.Name[strings.LastIndexAny(s.Name, "/$")+1:]
	case "function", "constructor":
		if call != nil {
			return callSignature(call)
		}
		name := s.Name
		if s.Kind == "constructor" {
			name = container(s)
			name = name[strings.LastIndexByte(name, '.')+1:]
		}
		return "fun " + name + "(…)" + typ
	case "property", "local":
		return "val " + s.Name + typ
	}
	return s.Name + typ
}

// callSignature renders a call's callee: fun name(p: T, …): R.
func callSignature(c *analysis.Call) string {
	name := c.Name[strings.LastIndexByte(c.Name, '.')+1:]
	if name == "<init>" {
		q := strings.TrimSuffix(c.Name, ".<init>")
		return "constructor " + q[strings.LastIndexByte(q, '.')+1:] + params(c)
	}
	var b strings.Builder
	b.WriteString("fun ")
	if len(c.TypeParameterNames) > 0 {
		b.WriteString("<" + strings.Join(c.TypeParameterNames, ", ") + "> ")
	}
	if c.DeclaredReceiver != nil {
		b.WriteString(RenderType(*c.DeclaredReceiver, false) + ".")
	}
	b.WriteString(name + params(c))
	ret := c.DeclaredReturnType
	if ret == nil {
		ret = c.ReturnType
	}
	if ret != nil {
		b.WriteString(": " + RenderType(*ret, false))
	}
	return b.String()
}

func params(c *analysis.Call) string {
	var ps []string
	for i, p := range c.Parameters {
		name := ""
		if i < len(c.ParameterNames) {
			name = c.ParameterNames[i]
		}
		s := RenderType(p, false)
		if i < len(c.Varargs) && c.Varargs[i] {
			s = "vararg " + name + ": " + RenderType(varargElement(p), false)
		} else if name != "" {
			s = name + ": " + s
		}
		ps = append(ps, s)
	}
	return "(" + strings.Join(ps, ", ") + ")"
}

// varargElement returns the element type of a vararg parameter's array
// type: T of Array<out T>, Int of IntArray.
func varargElement(t analysis.Type) analysis.Type {
	switch {
	case t.Name == "kotlin.Array" && len(t.Arguments) == 1:
		e := t.Arguments[0]
		e.Projection = ""
		return e
	case strings.HasPrefix(t.Name, "kotlin.") && strings.HasSuffix(t.Name, "Array") && len(t.Arguments) == 0:
		if e := strings.TrimSuffix(t.Name, "Array"); e != "kotlin." {
			return analysis.Type{Name: e}
		}
	}
	return t
}

// RenderType renders a type as Kotlin writes it, with simple names, or
// qualified ones if qualified is set.
func RenderType(t analysis.Type, qualified bool) string {
	if t.Projection == "*" {
		return "*"
	}
	name := t.Name
	if !qualified && !t.Parameter {
		name = name[strings.LastIndexByte(name, '.')+1:]
	}
	var s string
	switch {
	case strings.HasPrefix(t.Name, "kotlin.Function") && len(t.Arguments) > 0 && !t.SAM,
		strings.HasPrefix(t.Name, "kotlin.coroutines.SuspendFunction") && len(t.Arguments) > 0:
		var in []string
		for _, a := range t.Arguments[:len(t.Arguments)-1] {
			in = append(in, RenderType(a, qualified))
		}
		s = "(" + strings.Join(in, ", ") + ") -> " + RenderType(t.Arguments[len(t.Arguments)-1], qualified)
		if t.Suspend || strings.Contains(t.Name, "Suspend") {
			s = "suspend " + s
		}
		if t.Nullable {
			s = "(" + s + ")"
		}
	default:
		s = name
		if len(t.Arguments) > 0 {
			var args []string
			for _, a := range t.Arguments {
				args = append(args, RenderType(a, qualified))
			}
			s += "<" + strings.Join(args, ", ") + ">"
		}
	}
	switch {
	case t.Platform:
		s += "!"
	case t.MarkedNullable || t.Nullable && !t.Parameter:
		s += "?"
	}
	if t.Projection == "in" || t.Projection == "out" {
		s = t.Projection + " " + s
	}
	return s
}
