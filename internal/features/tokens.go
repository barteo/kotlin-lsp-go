package features

import (
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// The semantic token types and modifiers kcls reports, in legend order.
var (
	TokenTypes = []string{
		"namespace", "type", "class", "enum", "interface", "typeParameter", "parameter",
		"variable", "property", "enumMember", "function", "method", "keyword", "comment",
		"string", "number", "operator", "decorator", "label",
	}
	TokenModifiers = []string{"declaration", "readonly", "static", "deprecated", "documentation", "defaultLibrary"}
)

const (
	tNamespace = iota
	tType
	tClass
	tEnum
	tInterface
	tTypeParameter
	tParameter
	tVariable
	tProperty
	tEnumMember
	tFunction
	tMethod
	tKeyword
	tComment
	tString
	tNumber
	tOperator
	tDecorator
	tLabel
)

const (
	mDeclaration = 1 << iota
	mReadonly
	mStatic
	mDeprecated
	mDocumentation
	mDefaultLibrary
)

// Legend returns the semantic tokens legend.
func Legend() protocol.SemanticTokensLegend {
	return protocol.SemanticTokensLegend{TokenTypes: TokenTypes, TokenModifiers: TokenModifiers}
}

// token is one classified leaf.
type token struct {
	start, end int // bytes
	typ, mods  uint32
}

// SemanticTokens classifies the file's tokens from syntax alone:
// keywords (soft keywords only where they are keywords), comments,
// strings, numbers, annotations, labels, named arguments, the names
// declarations declare, type names, package names and the callees of
// calls. If r is not nil, only tokens that overlap it are returned.
// Tokens that span lines (block comments, multi-line strings) are split
// at line breaks, which every client accepts.
func SemanticTokens(f *File, r *protocol.Range) (protocol.SemanticTokens, error) {
	return SemanticTokensOf(nil, f, r)
}

// SemanticTokensOf classifies the file's tokens as SemanticTokens does
// and, if a analyzed f, the names its references resolve to: by the
// declarations they refer to, with their modifiers, and deprecated where
// the checker warns of a deprecated use.
func SemanticTokensOf(a *Analysis, f *File, r *protocol.Range) (protocol.SemanticTokens, error) {
	var res *resolved
	if a != nil {
		if af := a.File(f.Snapshot.Path()); af != nil && af.Snapshot == f.Snapshot {
			res = newResolved(a, af)
		}
	}
	lo, hi := 0, len(f.Snapshot.Source())
	if r != nil {
		var err error
		if lo, hi, err = f.Mapper.OffsetRange(*r); err != nil {
			return protocol.SemanticTokens{}, err
		}
	}
	var toks []token
	psi.WalkNodes(f.Snapshot.RootNode(), func(n psi.NodeRef) bool {
		nr := n.Range()
		if nr.End < lo || nr.Start > hi {
			return false
		}
		if !n.IsLeaf() {
			return true
		}
		typ, mods, ok := classify(n)
		if res != nil && n.NumericKind() == psi.KindIDENTIFIER {
			typ, mods, ok = res.classify(n, typ, mods, ok)
		}
		if ok && nr.Start < nr.End {
			toks = append(toks, token{nr.Start, nr.End, typ, mods})
		}
		return false
	})
	return protocol.SemanticTokens{Data: encode(f.Mapper, toks)}, nil
}

// classify returns a leaf's token type and modifiers.
func classify(n psi.NodeRef) (typ, mods uint32, ok bool) {
	k := n.NumericKind()
	parent := n.Parent()
	switch {
	case k == psi.KindKDOC:
		return tComment, mDocumentation, true
	case k == psi.KindEOL_COMMENT, k == psi.KindBLOCK_COMMENT, k == psi.KindSHEBANG_COMMENT:
		return tComment, 0, true
	case k == psi.KindINTEGER_LITERAL, k == psi.KindFLOAT_LITERAL:
		return tNumber, 0, true
	case k == psi.KindCHARACTER_LITERAL, k == psi.KindREGULAR_STRING_PART, k == psi.KindOPEN_QUOTE, k == psi.KindCLOSING_QUOTE:
		return tString, 0, true
	case k == psi.KindAT:
		switch parent.NumericKind() {
		case psi.KindANNOTATION_ENTRY, psi.KindANNOTATION:
			return tDecorator, 0, true
		case psi.KindLABEL:
			return tLabel, 0, true
		}
		return 0, 0, false
	case psi.IsKeyword(k):
		return tKeyword, 0, true
	case k == psi.KindIDENTIFIER:
		return classifyIdentifier(n, parent)
	}
	return 0, 0, false
}

func classifyIdentifier(n, parent psi.NodeRef) (typ, mods uint32, ok bool) {
	decl := uint32(mDeclaration)
	if annotated(parent, "Deprecated") {
		decl |= mDeprecated
	}
	switch parent.NumericKind() {
	case psi.KindLABEL:
		return tLabel, 0, true
	case psi.KindCLASS:
		switch {
		case parent.ChildKind(psi.KindINTERFACE_KEYWORD).Valid():
			return tInterface, decl, true
		case hasModifier(parent, psi.KindENUM_KEYWORD):
			return tEnum, decl, true
		case hasModifier(parent, psi.KindANNOTATION_KEYWORD):
			return tDecorator, decl, true
		}
		return tClass, decl, true
	case psi.KindOBJECT_DECLARATION:
		return tClass, decl | mStatic, true
	case psi.KindTYPEALIAS:
		return tType, decl, true
	case psi.KindTYPE_PARAMETER:
		return tTypeParameter, decl, true
	case psi.KindENUM_ENTRY:
		return tEnumMember, decl | mReadonly | mStatic, true
	case psi.KindFUN:
		if parent.Parent().NumericKind() == psi.KindCLASS_BODY {
			return tMethod, decl, true
		}
		return tFunction, decl, true
	case psi.KindPROPERTY:
		if parent.ChildKind(psi.KindVAL_KEYWORD).Valid() {
			decl |= mReadonly
		}
		switch parent.Parent().NumericKind() {
		case psi.KindFILE, psi.KindCLASS_BODY:
			return tProperty, decl, true
		}
		return tVariable, decl, true
	case psi.KindDESTRUCTURING_DECLARATION_ENTRY:
		return tVariable, decl | mReadonly, true
	case psi.KindVALUE_PARAMETER:
		list := parent.Parent()
		switch {
		case list.NumericKind() == psi.KindFOR:
			// A for loop's variable.
			return tVariable, decl | mReadonly, true
		case list.Parent().NumericKind() == psi.KindPRIMARY_CONSTRUCTOR &&
			(parent.ChildKind(psi.KindVAL_KEYWORD).Valid() || parent.ChildKind(psi.KindVAR_KEYWORD).Valid()):
			if parent.ChildKind(psi.KindVAL_KEYWORD).Valid() {
				decl |= mReadonly
			}
			return tProperty, decl, true
		}
		return tParameter, decl | mReadonly, true
	case psi.KindREFERENCE_EXPRESSION:
		return classifyReference(n, parent)
	}
	return 0, 0, false
}

// classifyReference classifies a name in a reference expression, from
// where it is: without resolution, only some places tell what a name is.
func classifyReference(n, ref psi.NodeRef) (typ, mods uint32, ok bool) {
	up := ref.Parent()
	for p := up; p.Valid(); p = p.Parent() {
		switch p.NumericKind() {
		case psi.KindPACKAGE_DIRECTIVE:
			return tNamespace, 0, true
		case psi.KindIMPORT_DIRECTIVE:
			// The last name of an import may be a class or a callable;
			// the names before it are packages, or outer classes if they
			// are capitalized.
			if capitalized(n.Text()) {
				return tClass, 0, true
			}
			if !p.IsAllUnder() && lastImportName(p) == ref {
				return 0, 0, false
			}
			return tNamespace, 0, true
		}
		if p.NumericKind() != psi.KindDOT_QUALIFIED_EXPRESSION {
			break
		}
	}
	switch up.NumericKind() {
	case psi.KindVALUE_ARGUMENT_NAME:
		return tParameter, 0, true
	case psi.KindUSER_TYPE:
		if inAnnotation(up) {
			return tDecorator, 0, true
		}
		// A qualifier of a type: a package, or an outer class.
		if q := up.Parent(); q.NumericKind() == psi.KindUSER_TYPE && q.FirstChild() == up && !capitalized(n.Text()) {
			return tNamespace, 0, true
		}
		return tType, 0, true
	case psi.KindCALL_EXPRESSION:
		if up.FirstChild() == ref {
			if capitalized(n.Text()) {
				return tClass, 0, true
			}
			return tFunction, 0, true
		}
	}
	return 0, 0, false
}

// lastImportName returns the last name of an import directive.
func lastImportName(imp psi.NodeRef) psi.NodeRef {
	var last psi.NodeRef
	psi.WalkNodes(imp, func(n psi.NodeRef) bool {
		if n.NumericKind() == psi.KindREFERENCE_EXPRESSION {
			last = n
		}
		return true
	})
	return last
}

// inAnnotation reports whether a user type names an annotation's class.
func inAnnotation(t psi.NodeRef) bool {
	for p := t.Parent(); p.Valid(); p = p.Parent() {
		switch p.NumericKind() {
		case psi.KindANNOTATION_ENTRY:
			return true
		case psi.KindUSER_TYPE, psi.KindTYPE_REFERENCE, psi.KindCONSTRUCTOR_CALLEE:
			continue
		}
		return false
	}
	return false
}

func capitalized(s string) bool {
	if len(s) > 0 && s[0] == '`' {
		s = s[1:]
	}
	return len(s) > 0 && 'A' <= s[0] && s[0] <= 'Z'
}

// encode turns tokens, in source order, into the protocol's relative
// encoding: per token, line delta, start delta, length, type, modifiers.
func encode(m *protocol.Mapper, toks []token) []uint32 {
	data := make([]uint32, 0, len(toks)*5)
	var line, char uint32
	text := m.Text()
	for _, t := range toks {
		for s := t.start; s < t.end; {
			l := m.Line(s)
			next := min(t.end, m.LineStart(l+1))
			if next <= s {
				break
			}
			end := next
			for end > s && (text[end-1] == '\n' || text[end-1] == '\r') {
				end--
			}
			p, e := m.Position(s), m.Position(end)
			if length := e.Character - p.Character; e.Line == p.Line && length > 0 {
				if p.Line != line {
					char = 0
				}
				data = append(data, p.Line-line, p.Character-char, length, t.typ, t.mods)
				line, char = p.Line, p.Character
			}
			s = next
		}
	}
	return data
}
