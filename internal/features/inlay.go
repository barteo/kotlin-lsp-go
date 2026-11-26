package features

import (
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// HintOptions choose the inlay hints, as the kcls.hints settings do.
type HintOptions struct {
	// Types shows the inferred types of properties, variables and
	// expression-bodied functions that don't declare them, but where the
	// initializer makes them plain: a literal, a constructor call, a cast.
	Types bool `json:"types"`
	// ParameterNames is "literals" to name the parameters that literal
	// arguments are passed to, "all" for every positional argument, or
	// "none".
	ParameterNames string `json:"parameterNames"`
	// Lambdas shows a lambda's implicit parameter it, and its receiver
	// this, with their types.
	Lambdas bool `json:"lambdas"`
}

// DefaultHints are the hints kcls shows unless told otherwise.
func DefaultHints() HintOptions {
	return HintOptions{Types: true, ParameterNames: "literals", Lambdas: true}
}

// InlayHints returns the hints in the range r of f.
func InlayHints(a *Analysis, f *File, r protocol.Range, o HintOptions) []protocol.InlayHint {
	out := []protocol.InlayHint{}
	if a == nil {
		return out
	}
	af := a.File(f.Snapshot.Path())
	if af == nil {
		return out
	}
	start, end, err := af.Mapper.OffsetRange(r)
	if err != nil {
		return out
	}
	fact := func(n psi.NodeRef) (analysis.Fact, bool) {
		return a.Result.Fact(analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: n.ID()})
	}
	symType := func(n psi.NodeRef) (analysis.Type, bool) {
		fa, ok := fact(n)
		if !ok || fa.Symbol == 0 {
			return analysis.Type{}, false
		}
		s, ok := a.Result.Symbol(fa.Symbol)
		if !ok || s.Type.Name == "" {
			return analysis.Type{}, false
		}
		return s.Type, true
	}
	hint := func(off int, label string, kind protocol.InlayHintKind) {
		h := protocol.InlayHint{Position: af.Mapper.Position(off), Label: label, Kind: kind}
		if kind == protocol.InlayHintParameter {
			h.PaddingRight = true
		}
		out = append(out, h)
	}
	typeHint := func(after psi.NodeRef, t analysis.Type) {
		if t.Name != "" {
			hint(after.Range().End, ": "+RenderType(t, false), protocol.InlayHintType)
		}
	}
	psi.WalkNodes(af.Snapshot.RootNode(), func(n psi.NodeRef) bool {
		if n.IsLeaf() {
			return false
		}
		if nr := n.Range(); nr.End < start || nr.Start > end {
			return false
		}
		switch n.NumericKind() {
		case psi.KindPROPERTY:
			if o.Types && !n.TypeReference().Valid() && n.NameIdentifier().Valid() && !obviousType(a, af, n) {
				if t, ok := symType(n); ok {
					typeHint(n.NameIdentifier(), t)
				}
			}
		case psi.KindDESTRUCTURING_DECLARATION_ENTRY:
			if o.Types && !n.TypeReference().Valid() && n.Name() != "_" {
				if t, ok := symType(n); ok {
					typeHint(n.NameIdentifier(), t)
				}
			}
		case psi.KindVALUE_PARAMETER:
			// A for loop's variable.
			if o.Types && n.Parent().NumericKind() == psi.KindFOR && !n.TypeReference().Valid() && n.NameIdentifier().Valid() {
				if t, ok := symType(n); ok {
					typeHint(n.NameIdentifier(), t)
				}
			}
		case psi.KindFUN:
			if ps := n.ChildKind(psi.KindVALUE_PARAMETER_LIST); o.Types && ps.Valid() && !n.ReturnType().Valid() && n.ChildKind(psi.KindEQ).Valid() {
				if t, ok := symType(n); ok && t.Name != "kotlin.Unit" {
					typeHint(ps, t)
				}
			}
		case psi.KindLAMBDA_EXPRESSION:
			if o.Lambdas {
				lambdaHints(a, af, n, fact, hint)
			}
		case psi.KindVALUE_ARGUMENT_LIST:
			if o.ParameterNames == "literals" || o.ParameterNames == "all" {
				parameterHints(af, n, fact, o.ParameterNames == "all", hint)
			}
		}
		return true
	})
	return out
}

// obviousType reports whether a property's initializer shows its type: a
// literal, a constructor call, a cast.
func obviousType(a *Analysis, af *File, prop psi.NodeRef) bool {
	var init psi.NodeRef
	for c := prop.ChildKind(psi.KindEQ).NextSibling(); c.Valid(); c = c.NextSibling() {
		if !c.IsTrivia() {
			init = c
			break
		}
	}
	if !init.Valid() || !prop.ChildKind(psi.KindEQ).Valid() {
		return false
	}
	switch init.NumericKind() {
	case psi.KindSTRING_TEMPLATE, psi.KindINTEGER_CONSTANT, psi.KindFLOAT_CONSTANT, psi.KindBOOLEAN_CONSTANT,
		psi.KindCHARACTER_CONSTANT, psi.KindBINARY_WITH_TYPE:
		return true
	case psi.KindDOT_QUALIFIED_EXPRESSION:
		// pkg.Foo(), Outer.Nested()
		var last psi.NodeRef
		for c := init.FirstChild(); c.Valid(); c = c.NextSibling() {
			if !c.IsTrivia() {
				last = c
			}
		}
		init = last
	}
	if init.NumericKind() != psi.KindCALL_EXPRESSION {
		return false
	}
	fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: init.ID()})
	return ok && fa.Call != nil && strings.HasSuffix(fa.Call.Name, ".<init>")
}

// lambdaHints adds a lambda's implicit it and receiver this, after its
// opening brace.
func lambdaHints(a *Analysis, af *File, n psi.NodeRef, fact func(psi.NodeRef) (analysis.Fact, bool), hint func(int, string, protocol.InlayHintKind)) {
	lit := n.ChildKind(psi.KindFUNCTION_LITERAL)
	lbrace := lit.ChildKind(psi.KindLBRACE)
	if !lbrace.Valid() {
		return
	}
	fa, ok := fact(n)
	if !ok || fa.LambdaParameters == nil {
		fa, ok = fact(lit)
	}
	if !ok {
		return
	}
	var labels []string
	if fa.LambdaReceiver != nil && fa.LambdaReceiver.Name != "" {
		labels = append(labels, "this: "+RenderType(*fa.LambdaReceiver, false))
	}
	if fa.ImplicitParameter && len(fa.LambdaParameters) == 1 {
		if s, ok := a.Result.Symbol(fa.LambdaParameters[0]); ok && s.Type.Name != "" {
			labels = append(labels, "it: "+RenderType(s.Type, false))
		}
	}
	if len(labels) > 0 {
		hint(lbrace.Range().End, strings.Join(labels, ", "), protocol.InlayHintParameter)
	}
}

// parameterHints adds the names of the parameters that the positional
// arguments of a call are passed to: of literal ones, or of all if all is
// set.
func parameterHints(af *File, list psi.NodeRef, fact func(psi.NodeRef) (analysis.Fact, bool), all bool, hint func(int, string, protocol.InlayHintKind)) {
	call := list.Parent()
	switch call.NumericKind() {
	case psi.KindCALL_EXPRESSION, psi.KindSUPER_TYPE_CALL_ENTRY, psi.KindCONSTRUCTOR_DELEGATION_CALL:
	default:
		return
	}
	fa, ok := fact(call)
	if !ok || fa.Call == nil {
		return
	}
	c := fa.Call
	if !c.StableNames || len(c.Parameters) < 2 && !all {
		return
	}
	i := -1
	vararg := -1
	for arg := list.FirstChild(); arg.Valid(); arg = arg.NextSibling() {
		if arg.NumericKind() != psi.KindVALUE_ARGUMENT {
			continue
		}
		i++
		if arg.ChildKind(psi.KindVALUE_ARGUMENT_NAME).Valid() {
			return // the arguments after a named one are named too, or should be
		}
		if i >= len(c.ArgumentMapping) {
			return
		}
		p := c.ArgumentMapping[i]
		if p < 0 || p >= len(c.ParameterNames) || c.ParameterNames[p] == "" {
			continue
		}
		name := c.ParameterNames[p]
		isVararg := p < len(c.Varargs) && c.Varargs[p]
		if isVararg {
			if vararg == p {
				continue // a vararg's name, on its first argument only
			}
			vararg = p
		}
		x := argumentExpression(arg)
		if !x.Valid() || x.NumericKind() == psi.KindLAMBDA_EXPRESSION {
			continue
		}
		if !all && !literal(x) {
			continue
		}
		if x.NumericKind() == psi.KindREFERENCE_EXPRESSION && x.Name() == name {
			continue
		}
		label := name + ":"
		if isVararg {
			label = "..." + label
		}
		hint(arg.Range().Start, label, protocol.InlayHintParameter)
	}
}

// argumentExpression returns an argument's expression, less a spread.
func argumentExpression(arg psi.NodeRef) psi.NodeRef {
	for c := arg.FirstChild(); c.Valid(); c = c.NextSibling() {
		if !c.IsTrivia() && !c.IsLeaf() {
			return c
		}
	}
	return psi.NodeRef{}
}

// literal reports whether an expression is a literal: a constant, a
// string, null, or a negated number.
func literal(x psi.NodeRef) bool {
	switch x.NumericKind() {
	case psi.KindSTRING_TEMPLATE, psi.KindINTEGER_CONSTANT, psi.KindFLOAT_CONSTANT, psi.KindBOOLEAN_CONSTANT,
		psi.KindCHARACTER_CONSTANT, psi.KindNULL:
		return true
	case psi.KindPREFIX_EXPRESSION:
		var last psi.NodeRef
		for c := x.FirstChild(); c.Valid(); c = c.NextSibling() {
			if !c.IsTrivia() {
				last = c
			}
		}
		return literal(last)
	}
	return false
}
