package features

import (
	"context"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// SignatureHelp returns the signatures of the functions or constructors
// that the call whose arguments pos is in could call, and the parameter
// that the argument at pos is for. The signatures are what the code could
// name at the callee: the overloads in scope, or a receiver's.
// labelOffsets has a parameter's label be its offsets in the signature's.
func SignatureHelp(ctx context.Context, c Completer, f *File, pos protocol.Position, labelOffsets bool) (*protocol.SignatureHelp, error) {
	off, err := f.Mapper.Offset(pos)
	if err != nil {
		return nil, err
	}
	if off > 0 && inCommentOrString(f.Snapshot.NodeAt(off-1)) {
		return nil, nil
	}
	list := argumentListAt(f.Snapshot, off)
	if !list.Valid() {
		return nil, nil
	}
	callee := list.Parent().FirstChild()
	if list.Parent().NumericKind() != psi.KindCALL_EXPRESSION || callee.NumericKind() != psi.KindREFERENCE_EXPRESSION {
		return nil, nil
	}
	name := strings.Trim(callee.Name(), "`")
	r := callee.Range()
	text := f.Mapper.Text()
	spliced := NewFile(f.URI, f.Snapshot.Path(), text[:r.Start]+analysis.Placeholder+text[r.End:off]+closer(f.Snapshot, off)+text[off:], f.Mapper.Encoding())
	a, err := c.Complete(ctx, spliced, &analysis.Complete{Path: f.Snapshot.Path(), Match: func(n string) bool { return n == name }})
	if err != nil || a == nil {
		return nil, err
	}
	comp := a.Result.Completion()
	if comp == nil || !comp.Found {
		return nil, nil
	}
	type signature struct {
		call analysis.Call
		sym  analysis.Symbol
	}
	var sigs []signature
	for _, ci := range comp.Items {
		if ci.Name != name {
			continue
		}
		sym, _ := a.Result.Symbol(ci.Symbol)
		switch {
		case ci.Kind == "function" && ci.Call != nil:
			sigs = append(sigs, signature{*ci.Call, sym})
		case ci.Kind == "class":
			for _, ctor := range ci.Constructors {
				sigs = append(sigs, signature{ctor, sym})
			}
		case ci.Type != nil && functionType(*ci.Type) && !ci.Type.SAM && len(ci.Type.Arguments) > 0:
			// A value of a function type, which the call invokes.
			args := ci.Type.Arguments
			ret := args[len(args)-1]
			sigs = append(sigs, signature{analysis.Call{Name: name, Parameters: args[:len(args)-1], DeclaredReturnType: &ret}, sym})
		}
	}
	if len(sigs) == 0 {
		return nil, nil
	}

	// The argument at the caret: its index, and its name if it names one.
	index, argName, args := 0, "", 0
	var named []string
	for ch := list.FirstChild(); ch.Valid(); ch = ch.NextSibling() {
		switch ch.NumericKind() {
		case psi.KindCOMMA:
			if ch.Range().End <= off {
				index++
			}
		case psi.KindVALUE_ARGUMENT:
			args++
			n := ch.ChildKind(psi.KindVALUE_ARGUMENT_NAME).ChildKind(psi.KindREFERENCE_EXPRESSION).Name()
			if n != "" {
				named = append(named, n)
			}
			if rr := ch.Range(); rr.Start <= off && off <= rr.End {
				argName = n
			}
		}
	}
	args = max(args, index+1)
	param := func(c analysis.Call) int {
		if argName != "" {
			for i, n := range c.ParameterNames {
				if n == argName {
					return i
				}
			}
			return -1
		}
		if index >= len(c.Parameters) && len(c.Varargs) > 0 && c.Varargs[len(c.Varargs)-1] {
			return len(c.Parameters) - 1
		}
		return index
	}
	// The types of the positional arguments written, from the analysis of
	// the spliced file, whose arguments are where they were, shifted by
	// the callee's change of length.
	var argTypes []string
	delta := len(analysis.Placeholder) - (r.End - r.Start)
	if sl := spliced.Snapshot.NodeAt(list.Range().Start + delta).Parent(); sl.NumericKind() == psi.KindVALUE_ARGUMENT_LIST {
		for ch := sl.FirstChild(); ch.Valid(); ch = ch.NextSibling() {
			if ch.NumericKind() != psi.KindVALUE_ARGUMENT || ch.ChildKind(psi.KindVALUE_ARGUMENT_NAME).Valid() {
				continue
			}
			t := ""
			for e := ch.FirstChild(); e.Valid(); e = e.NextSibling() {
				if e.IsTrivia() || e.IsLeaf() {
					continue
				}
				if fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: spliced.Snapshot.ID(), Node: e.ID()}); ok && fa.Type != nil {
					t = fa.Type.Name
				}
				break
			}
			argTypes = append(argTypes, t)
		}
	}
	// The active signature: the one that takes the arguments written, and
	// has the most parameters of their types, and the fewest parameters.
	active, best := -1, -1
	for i, s := range sigs {
		c := s.call
		fits := args <= len(c.Parameters) || len(c.Varargs) > 0 && c.Varargs[len(c.Varargs)-1]
		for _, n := range named {
			if !slices.Contains(c.ParameterNames, n) {
				fits = false
			}
		}
		if !fits {
			continue
		}
		score := 0
		for j, t := range argTypes {
			if j < len(c.Parameters) && t != "" && c.Parameters[j].Name == t {
				score++
			}
		}
		if active < 0 || score > best || score == best && len(c.Parameters) < len(sigs[active].call.Parameters) {
			active, best = i, score
		}
	}
	if active < 0 {
		active = 0
	}

	help := &protocol.SignatureHelp{ActiveSignature: uint32(active)}
	for i, s := range sigs {
		info := signatureInfo(name, s.call, labelOffsets)
		if p := param(s.call); p >= 0 && p < len(s.call.Parameters) {
			u := uint32(p)
			info.ActiveParameter = &u
			if i == active {
				help.ActiveParameter = u
			}
		} else if i == active {
			help.ActiveParameter = uint32(len(s.call.Parameters)) // none
		}
		if doc := signatureDoc(ctx, a, s.sym, s.call, i == active); doc != "" {
			info.Documentation = &protocol.MarkupContent{Kind: "markdown", Value: doc}
		}
		help.Signatures = append(help.Signatures, info)
	}
	return help, nil
}

// argumentListAt returns the innermost argument list whose parentheses
// enclose off.
func argumentListAt(s *psi.Snapshot, off int) psi.NodeRef {
	if off == 0 {
		return psi.NodeRef{}
	}
	for n := s.NodeAt(off - 1); n.Valid(); n = n.Parent() {
		if n.NumericKind() != psi.KindVALUE_ARGUMENT_LIST {
			continue
		}
		lpar := n.FirstChild()
		if lpar.NumericKind() != psi.KindLPAR || off < lpar.Range().End {
			continue
		}
		if rpar := closingParen(n); rpar.Valid() && off > rpar.Range().Start {
			continue
		}
		return n
	}
	return psi.NodeRef{}
}

// closingParen returns an argument list's closing parenthesis, if the
// text has it.
func closingParen(list psi.NodeRef) psi.NodeRef {
	var last psi.NodeRef
	for ch := list.FirstChild(); ch.Valid(); ch = ch.NextSibling() {
		last = ch
	}
	if last.NumericKind() != psi.KindRPAR {
		return psi.NodeRef{}
	}
	return last
}

// closer returns ")" if off is in the argument list of a call that the
// text doesn't close, for a splice to close it there: println( on a line
// of its own would take the next lines for its arguments, and the checker
// wouldn't see the call.
func closer(s *psi.Snapshot, off int) string {
	if list := argumentListAt(s, off); list.Valid() && !closingParen(list).Valid() {
		return ")"
	}
	return ""
}

// signatureInfo renders a signature: name(p: T, …): R.
func signatureInfo(name string, c analysis.Call, labelOffsets bool) protocol.SignatureInformation {
	var b strings.Builder
	b.WriteString(name + "(")
	var ps []protocol.ParameterInformation
	for i, t := range c.Parameters {
		if i > 0 {
			b.WriteString(", ")
		}
		p := ""
		if i < len(c.ParameterNames) && c.ParameterNames[i] != "" {
			p = c.ParameterNames[i] + ": "
		}
		if i < len(c.Varargs) && c.Varargs[i] {
			p = "vararg " + p + RenderType(varargElement(t), false)
		} else {
			p += RenderType(t, false)
		}
		start := utf16Len(b.String())
		b.WriteString(p)
		if labelOffsets {
			ps = append(ps, protocol.ParameterInformation{Label: []uint32{uint32(start), uint32(start + utf16Len(p))}})
		} else {
			ps = append(ps, protocol.ParameterInformation{Label: p})
		}
	}
	b.WriteString(")")
	if c.DeclaredReturnType != nil && c.DeclaredReturnType.Name != "kotlin.Unit" && !strings.HasSuffix(c.Name, "<init>") {
		b.WriteString(": " + RenderType(*c.DeclaredReturnType, false))
	}
	return protocol.SignatureInformation{Label: b.String(), Parameters: ps}
}

// signatureDoc returns the KDoc of a signature's declaration: a source
// one's, or, if library is set, a library one's from its sources.
func signatureDoc(ctx context.Context, a *Analysis, sym analysis.Symbol, c analysis.Call, library bool) string {
	if sym.Declaration != nil {
		if _, dn, ok := a.Node(*sym.Declaration); ok {
			return kdoc(dn)
		}
		return ""
	}
	if !library || a.Libraries == nil || sym.QualifiedName == "" {
		return ""
	}
	d := LibraryDecl{Kind: sym.Kind, QualifiedName: sym.QualifiedName, Owner: sym.Owner, Parameters: len(c.Parameters)}
	if sym.Kind == "class" {
		d = LibraryDecl{Kind: "constructor", QualifiedName: sym.QualifiedName + ".<init>", Owner: sym.Name, Parameters: len(c.Parameters)}
	}
	lt, ok := a.Libraries.Find(ctx, d)
	switch {
	case !ok:
		return ""
	case lt.Node.Valid():
		return kdoc(lt.Node)
	}
	return javadoc(lt.Doc)
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }
