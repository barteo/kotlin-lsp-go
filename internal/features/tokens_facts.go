package features

import (
	"cmp"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// resolved classifies names by what they resolve to.
type resolved struct {
	a          *Analysis
	f          *File
	deprecated map[int]bool // the offsets of deprecated uses
	facts      []brief      // of the file's references and type references, by node
}

// brief is what classify reads of a reference's or a type reference's
// fact, copied out of Result.Facts, whose facts are only valid while it
// calls its function.
type brief struct {
	node      psi.NodeID
	symbol    uint32
	typeName  string // "" if the fact has no type
	hasType   bool
	typeParam bool
	callName  string
	hasCall   bool
	receiver  bool // the call has a receiver
}

func newResolved(a *Analysis, f *File) *resolved {
	r := &resolved{a: a, f: f, deprecated: map[int]bool{}}
	for _, d := range a.Result.Diagnostics() {
		if d.Path == f.Snapshot.Path() && (d.Name == "DEPRECATION" || strings.HasPrefix(d.Name, "DEPRECATION_")) {
			r.deprecated[f.offset(d.Line, d.Column)] = true
		}
	}
	// One pass over the file's facts, in node order, rather than a copy
	// of each fact a name looks up.
	a.Result.Facts(f.Snapshot.ID(), func(fa *analysis.Fact) bool {
		switch f.Snapshot.Node(fa.Element.Node).NumericKind() {
		case psi.KindREFERENCE_EXPRESSION, psi.KindUSER_TYPE:
		default:
			return true
		}
		b := brief{node: fa.Element.Node, symbol: fa.Symbol}
		if fa.Type != nil {
			b.hasType, b.typeName, b.typeParam = true, fa.Type.Name, fa.Type.Parameter
		}
		if fa.Call != nil {
			b.hasCall, b.callName, b.receiver = true, fa.Call.Name, fa.Call.Receiver != nil
		}
		r.facts = append(r.facts, b)
		return true
	})
	return r
}

// fact returns what the fact of the reference or type reference n says.
func (r *resolved) fact(n psi.NodeRef) (brief, bool) {
	i, ok := slices.BinarySearchFunc(r.facts, n.ID(), func(b brief, id psi.NodeID) int { return cmp.Compare(b.node, id) })
	if !ok {
		return brief{}, false
	}
	return r.facts[i], true
}

// classify refines the syntactic classification typ, mods, ok of an
// identifier.
func (r *resolved) classify(id psi.NodeRef, typ, mods uint32, ok bool) (uint32, uint32, bool) {
	ref := id.Parent()
	if ref.NumericKind() != psi.KindREFERENCE_EXPRESSION {
		return typ, mods, ok
	}
	switch up := ref.Parent(); {
	case up.NumericKind() == psi.KindVALUE_ARGUMENT_NAME:
		return typ, mods, ok
	case inImportOrPackage(ref):
		// An import's last name, of a class in the sources.
		imp := ref.Parent()
		for imp.NumericKind() == psi.KindDOT_QUALIFIED_EXPRESSION {
			imp = imp.Parent()
		}
		if imp.NumericKind() == psi.KindIMPORT_DIRECTIVE && !imp.IsAllUnder() && lastImportName(imp) == ref {
			if cls, found := r.a.Class(imp.Path()); found {
				return r.symbol(id, cls, typ, mods, ok)
			}
		}
		return typ, mods, ok
	case up.NumericKind() == psi.KindUSER_TYPE && up.ChildKind(psi.KindREFERENCE_EXPRESSION) == ref:
		if ok && typ == tNamespace {
			return typ, mods, ok
		}
		if fa, found := r.fact(up); found && fa.hasType {
			if fa.typeParam {
				return tTypeParameter, 0, true
			}
			if cls, found := r.a.Class(fa.typeName); found {
				return r.symbol(id, cls, typ, mods, ok)
			}
			if fa.typeName != "" && !inAnnotation(up) {
				return tClass, r.libraryMods(id, fa.typeName), true
			}
		}
		return typ, mods, ok
	}
	fa, found := r.fact(ref)
	if !found {
		return typ, mods, ok
	}
	switch {
	case fa.symbol != 0:
		sym, _ := r.a.Result.Symbol(fa.symbol)
		return r.symbol(id, sym, typ, mods, ok)
	case fa.hasCall:
		t := uint32(tFunction)
		if strings.HasSuffix(fa.callName, ".<init>") {
			t = tClass
		} else if fa.receiver {
			t = tMethod
		}
		return t, r.libraryMods(id, fa.callName), true
	}
	return typ, mods, ok
}

// symbol classifies a reference to sym: as the declaration's name if it
// is in the sources, else by the symbol's kind.
func (r *resolved) symbol(id psi.NodeRef, sym analysis.Symbol, typ, mods uint32, ok bool) (uint32, uint32, bool) {
	if sym.Declaration != nil {
		if _, dn, found := r.a.Node(*sym.Declaration); found {
			if sym.Kind == "constructor" && dn.NumericKind() == psi.KindSECONDARY_CONSTRUCTOR {
				dn = dn.Parent().Parent() // the class, through its body
			}
			if name := dn.NameIdentifier(); name.Valid() {
				if t, m, found := classifyIdentifier(name, dn); found {
					m &^= mDeclaration
					if r.deprecated[id.Range().Start] {
						m |= mDeprecated
					}
					return t, m, true
				}
			}
		}
	}
	var t uint32
	switch sym.Kind {
	case "class", "constructor":
		t = tClass
	case "function":
		t = tFunction
		if sym.Owner != "" {
			t = tMethod
		}
	case "property":
		t = tProperty
	case "local":
		t = tVariable
	case "parameter":
		t = tParameter
	default:
		return typ, mods, ok
	}
	name := sym.QualifiedName
	if name == "" {
		name = strings.ReplaceAll(sym.Owner, "/", ".")
	}
	return t, r.libraryMods(id, name), true
}

// libraryMods returns the modifiers of a reference to a library's
// declaration, whose qualified name is name.
func (r *resolved) libraryMods(id psi.NodeRef, name string) uint32 {
	var m uint32
	if strings.HasPrefix(name, "kotlin.") || strings.HasPrefix(name, "java.") {
		m |= mDefaultLibrary
	}
	if r.deprecated[id.Range().Start] {
		m |= mDeprecated
	}
	return m
}

func inImportOrPackage(n psi.NodeRef) bool {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.NumericKind() {
		case psi.KindIMPORT_DIRECTIVE, psi.KindPACKAGE_DIRECTIVE:
			return true
		case psi.KindDOT_QUALIFIED_EXPRESSION:
			continue
		}
		return false
	}
	return false
}
