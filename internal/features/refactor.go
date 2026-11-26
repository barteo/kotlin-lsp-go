package features

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// extractVariable returns the refactoring that declares the expression
// at [start, end) as a local val before its statement, and names it
// there: the expression the selection covers, or, with none, the call
// or chain at the caret.
func (ac *actions) extractVariable(start, end int) (protocol.CodeAction, bool) {
	f := ac.f
	x := ac.expressionAt(start, end)
	if !x.Valid() {
		return protocol.CodeAction{}, false
	}
	stmt, ok := statementOf(x)
	if !ok {
		return protocol.CodeAction{}, false
	}
	text := f.Mapper.Text()
	name := ac.uniqueName(stmt.Parent(), ac.suggestName(x))
	code := text[x.Range().Start:x.Range().End]
	var edits []protocol.TextEdit
	if stmt == x {
		edits = []protocol.TextEdit{ac.replace(x.Range().Start, x.Range().End, "val "+name+" = "+code)}
	} else {
		at := stmt.Range().Start
		indent := lineIndent(text, at)
		if strings.TrimSpace(text[strings.LastIndexByte(text[:at], '\n')+1:at]) != "" {
			indent = "" // not first on its line: the declaration takes a line of its own
		}
		edits = []protocol.TextEdit{
			ac.replace(at, at, "val "+name+" = "+code+"\n"+indent),
			ac.replace(x.Range().Start, x.Range().End, name),
		}
	}
	return protocol.CodeAction{Title: "Extract variable '" + name + "'", Edit: ac.edit(edits...)}, true
}

// expressionAt returns the expression that [start, end) selects, less
// the whitespace around it, or the one at start if it selects nothing.
func (ac *actions) expressionAt(start, end int) psi.NodeRef {
	text := ac.f.Mapper.Text()
	for start < end && unicode.IsSpace(rune(text[start])) {
		start++
	}
	for end > start && unicode.IsSpace(rune(text[end-1])) {
		end--
	}
	n := ac.f.Snapshot.NodeAt(start)
	if start == end {
		// The innermost expression at the caret, as a whole call or
		// chain.
		plain := func(n psi.NodeRef) bool { // a variable alone, which needs no other
			p := n.Parent()
			return n.NumericKind() == psi.KindREFERENCE_EXPRESSION && !isCallee(n) && !(qualified(p) && p.FirstChild() != n)
		}
		for ; n.Valid() && (!extractable(n) || plain(n)); n = n.Parent() {
			if isDeclaration(n) || n.NumericKind() == psi.KindBLOCK {
				return psi.NodeRef{}
			}
		}
		for n.Valid() {
			p := n.Parent()
			if isCallee(n) || (qualified(p) || p.NumericKind() == psi.KindCALLABLE_REFERENCE_EXPRESSION) && p.FirstChild() != n {
				n = p
				continue
			}
			break
		}
		return n
	}
	for ; n.Valid(); n = n.Parent() {
		r := n.Range()
		if r.Start < start || r.End > end {
			return psi.NodeRef{}
		}
		if r.Start == start && r.End == end && extractable(n) {
			if p := n.Parent(); isCallee(n) || qualified(p) && p.FirstChild() != n {
				return psi.NodeRef{} // a callee, a selector: not a value
			}
			return n
		}
	}
	return psi.NodeRef{}
}

func qualified(n psi.NodeRef) bool {
	k := n.NumericKind()
	return k == psi.KindDOT_QUALIFIED_EXPRESSION || k == psi.KindSAFE_ACCESS_EXPRESSION
}

// extractable reports whether n is an expression a variable can hold.
func extractable(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindCALL_EXPRESSION, psi.KindDOT_QUALIFIED_EXPRESSION, psi.KindSAFE_ACCESS_EXPRESSION, psi.KindBINARY_EXPRESSION,
		psi.KindBINARY_WITH_TYPE, psi.KindIS_EXPRESSION, psi.KindSTRING_TEMPLATE, psi.KindINTEGER_CONSTANT, psi.KindFLOAT_CONSTANT,
		psi.KindBOOLEAN_CONSTANT, psi.KindCHARACTER_CONSTANT, psi.KindPARENTHESIZED, psi.KindARRAY_ACCESS_EXPRESSION,
		psi.KindLAMBDA_EXPRESSION, psi.KindOBJECT_LITERAL, psi.KindCALLABLE_REFERENCE_EXPRESSION, psi.KindCLASS_LITERAL_EXPRESSION,
		psi.KindCOLLECTION_LITERAL_EXPRESSION, psi.KindREFERENCE_EXPRESSION, psi.KindTHIS_EXPRESSION, psi.KindIF, psi.KindWHEN:
	case psi.KindPREFIX_EXPRESSION, psi.KindPOSTFIX_EXPRESSION:
		op := n.ChildKind(psi.KindOPERATION_REFERENCE).Text()
		return op != "++" && op != "--"
	default:
		return false
	}
	if isAssigned(n) {
		return false
	}
	p := n.Parent()
	if n.NumericKind() == psi.KindBINARY_EXPRESSION {
		switch n.ChildKind(psi.KindOPERATION_REFERENCE).Text() {
		case "=", "+=", "-=", "*=", "/=", "%=":
			return false // an assignment, not a value
		}
	}
	switch p.NumericKind() {
	case psi.KindVALUE_ARGUMENT_NAME, psi.KindUSER_TYPE, psi.KindLABEL_QUALIFIER, psi.KindIMPORT_DIRECTIVE, psi.KindPACKAGE_DIRECTIVE:
		return false
	}
	if n.NumericKind() == psi.KindIF && !n.ChildKind(psi.KindELSE).Valid() {
		return false
	}
	return true
}

// statementOf returns the statement of a block that holds x, if x is
// evaluated each time that statement is, and before it does anything
// else: not in a branch, a loop's body or condition, a lambda, or the
// right side of &&, || or ?:.
func statementOf(x psi.NodeRef) (psi.NodeRef, bool) {
	child := x
	for p := x.Parent(); p.Valid(); child, p = p, p.Parent() {
		switch p.NumericKind() {
		case psi.KindBLOCK:
			if lb := p.ChildKind(psi.KindLBRACE); lb.Valid() || p.Parent().NumericKind() == psi.KindFUNCTION_LITERAL {
				return child, true
			}
			return psi.NodeRef{}, false // a script's
		case psi.KindTHEN, psi.KindELSE, psi.KindBODY, psi.KindWHEN_ENTRY, psi.KindFUNCTION_LITERAL, psi.KindCLASS_BODY,
			psi.KindWHILE, psi.KindDO_WHILE, psi.KindFILE, psi.KindLONG_STRING_TEMPLATE_ENTRY:
			return psi.NodeRef{}, false
		case psi.KindBINARY_EXPRESSION:
			switch p.ChildKind(psi.KindOPERATION_REFERENCE).Text() {
			case "&&", "||", "?:":
				if child != p.FirstChild() {
					return psi.NodeRef{}, false
				}
			}
		case psi.KindSAFE_ACCESS_EXPRESSION:
			if child != p.FirstChild() {
				return psi.NodeRef{}, false
			}
		}
		if isDeclaration(p) && p.NumericKind() != psi.KindPROPERTY {
			return psi.NodeRef{}, false
		}
		if p.NumericKind() == psi.KindPROPERTY && p.Parent().NumericKind() != psi.KindBLOCK {
			return psi.NodeRef{}, false // a member's initializer
		}
	}
	return psi.NodeRef{}, false
}

// suggestName returns a name for a variable that holds x: of what it
// calls or reads, else of its type.
func (ac *actions) suggestName(x psi.NodeRef) string {
	n := x
	for qualified(n) {
		var last psi.NodeRef
		for c := n.FirstChild(); c.Valid(); c = c.NextSibling() {
			if !c.IsTrivia() {
				last = c
			}
		}
		n = last
	}
	var name string
	switch n.NumericKind() {
	case psi.KindCALL_EXPRESSION:
		name = n.FirstChild().Name()
	case psi.KindREFERENCE_EXPRESSION:
		name = n.Name()
	}
	for _, prefix := range []string{"get", "is", "create", "new", "find", "to", "as", "compute", "build", "read", "load"} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && rest != "" && unicode.IsUpper(firstRune(rest)) {
			name = rest
			break
		}
	}
	if rest, ok := strings.CutSuffix(name, "Of"); ok && rest != "" {
		name = rest // listOf, mapOf
	}
	if name == "" {
		if fa, ok := ac.fact(x); ok && fa.Type != nil && fa.Type.Name != "" {
			switch t := fa.Type.Name[strings.LastIndexByte(fa.Type.Name, '.')+1:]; t {
			case "Int", "Long", "Short", "Byte", "Double", "Float", "Char", "Boolean", "Unit", "Any", "Nothing", "Number":
			default:
				name = t
			}
		}
	}
	if name == "" || !isWord(name) {
		return "value"
	}
	r, size := utf8.DecodeRuneInString(name)
	// URL → url, HTMLParser → htmlParser.
	upper := 0
	for _, c := range name {
		if !unicode.IsUpper(c) {
			break
		}
		upper++
	}
	if upper > 1 && upper < utf8.RuneCountInString(name) {
		upper--
	}
	if upper > 1 {
		return strings.ToLower(name[:upper]) + name[upper:]
	}
	return string(unicode.ToLower(r)) + name[size:]
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// uniqueName returns name, or name with a number, so that no name in the
// declaration or block around scope has it already.
func (ac *actions) uniqueName(scope psi.NodeRef, name string) string {
	for p := scope; p.Valid(); p = p.Parent() {
		if isDeclaration(p) && p.NumericKind() != psi.KindPROPERTY || p.NumericKind() == psi.KindFUNCTION_LITERAL {
			scope = p
			break
		}
	}
	// The names declared, and those read, which a new local would hide:
	// not those called, which name functions.
	used := map[string]bool{}
	psi.WalkNodes(scope, func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindPROPERTY, psi.KindVALUE_PARAMETER, psi.KindDESTRUCTURING_DECLARATION_ENTRY:
			used[n.Name()] = true
		case psi.KindREFERENCE_EXPRESSION:
			if !isCallee(n) {
				used[n.Name()] = true
			}
		}
		return true
	})
	if hardKeywords[name] {
		name += "1"
	}
	base := name
	for i := 2; used[name]; i++ {
		name = base + strconv.Itoa(i)
	}
	return name
}

// inlineVariable returns the refactoring that replaces the references to
// the local val at [start, end) with its initializer, and removes it.
func (ac *actions) inlineVariable(start int) (protocol.CodeAction, bool) {
	f := ac.f
	if ac.a == nil {
		return protocol.CodeAction{}, false
	}
	k, ok := KeyAt(ac.a, f, f.Mapper.Position(start))
	if !ok {
		return protocol.CodeAction{}, false
	}
	var decl psi.NodeRef
	var refs []psi.NodeRef
	for _, o := range Occurrences(ac.a, f, k) {
		switch o.Kind {
		case OccurrenceDeclaration:
			decl = o.Node
			for decl.Valid() && decl.NumericKind() != psi.KindPROPERTY {
				decl = decl.Parent()
			}
		case OccurrenceWrite:
			return protocol.CodeAction{}, false
		default:
			ref := o.Node
			if ref.NumericKind() == psi.KindIDENTIFIER && ref.Parent().NumericKind() == psi.KindREFERENCE_EXPRESSION {
				ref = ref.Parent()
			}
			refs = append(refs, ref)
		}
	}
	if !decl.Valid() || decl.Parent().NumericKind() != psi.KindBLOCK || !decl.ChildKind(psi.KindVAL_KEYWORD).Valid() ||
		decl.ChildKind(psi.KindPROPERTY_DELEGATE).Valid() || decl.ContainingFile() != f.Snapshot {
		return protocol.CodeAction{}, false
	}
	eq := decl.ChildKind(psi.KindEQ)
	var init psi.NodeRef
	for c := eq.NextSibling(); eq.Valid() && c.Valid(); c = c.NextSibling() {
		if !c.IsTrivia() {
			init = c
			break
		}
	}
	if !init.Valid() || len(refs) > 1 && effectful(init) {
		return protocol.CodeAction{}, false // evaluating it more than once may not do the same
	}
	text := f.Mapper.Text()
	code := text[init.Range().Start:init.Range().End]
	s, e := lineSpan(text, decl.Range().Start, decl.Range().End)
	edits := []protocol.TextEdit{ac.replace(s, e, "")}
	for _, ref := range refs {
		r := ref.Range()
		switch p := ref.Parent(); {
		case p.NumericKind() == psi.KindSHORT_STRING_TEMPLATE_ENTRY:
			edits = append(edits, ac.replace(p.Range().Start, p.Range().End, "${"+code+"}"))
		case needsParens(init, ref):
			edits = append(edits, ac.replace(r.Start, r.End, "("+code+")"))
		default:
			edits = append(edits, ac.replace(r.Start, r.End, code))
		}
	}
	return protocol.CodeAction{Title: "Inline variable '" + decl.Name() + "'", Edit: ac.edit(edits...)}, true
}

// needsParens reports whether the expression x needs parentheses where
// ref is.
func needsParens(x, ref psi.NodeRef) bool {
	switch x.NumericKind() {
	case psi.KindBINARY_EXPRESSION, psi.KindBINARY_WITH_TYPE, psi.KindIS_EXPRESSION, psi.KindPREFIX_EXPRESSION, psi.KindIF,
		psi.KindWHEN, psi.KindLAMBDA_EXPRESSION, psi.KindTRY, psi.KindLABELED_EXPRESSION, psi.KindANNOTATED_EXPRESSION:
	default:
		return false
	}
	switch p := ref.Parent(); p.NumericKind() {
	case psi.KindVALUE_ARGUMENT, psi.KindPROPERTY, psi.KindBLOCK, psi.KindRETURN, psi.KindPARENTHESIZED, psi.KindTHEN,
		psi.KindELSE, psi.KindCONDITION, psi.KindWHEN_CONDITION_EXPRESSION, psi.KindLONG_STRING_TEMPLATE_ENTRY:
		return false
	case psi.KindBINARY_EXPRESSION:
		return !(p.ChildKind(psi.KindOPERATION_REFERENCE).Text() == "=" && p.FirstChild() != ref)
	}
	return true
}

// effectful reports whether evaluating x may do something but compute
// a value: call a function, assign, increment. Operators and property
// reads count as computing.
func effectful(x psi.NodeRef) bool {
	found := false
	psi.WalkNodes(x, func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindCALL_EXPRESSION, psi.KindLAMBDA_EXPRESSION, psi.KindOBJECT_LITERAL:
			found = true
		case psi.KindPOSTFIX_EXPRESSION, psi.KindPREFIX_EXPRESSION, psi.KindBINARY_EXPRESSION:
			switch n.ChildKind(psi.KindOPERATION_REFERENCE).Text() {
			case "++", "--", "=", "+=", "-=", "*=", "/=", "%=":
				found = true
			}
		}
		return !found
	})
	return found
}
