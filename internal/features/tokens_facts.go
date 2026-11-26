package features

import (
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// resolved classifies names by what they resolve to.
type resolved struct {
	a          *Analysis
	f          *File
	deprecated map[int]bool // the offsets of deprecated uses
}

func newResolved(a *Analysis, f *File) *resolved {
	r := &resolved{a: a, f: f, deprecated: map[int]bool{}}
	for _, d := range a.Result.Diagnostics() {
		if d.Path == f.Snapshot.Path() && (d.Name == "DEPRECATION" || strings.HasPrefix(d.Name, "DEPRECATION_")) {
			r.deprecated[f.offset(d.Line, d.Column)] = true
		}
	}
	return r
}

func (r *resolved) fact(n psi.NodeRef) (analysis.Fact, bool) {
	return r.a.Result.Fact(analysis.ElementID{Snapshot: r.f.Snapshot.ID(), Node: n.ID()})
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
		if fa, found := r.fact(up); found && fa.Type != nil {
			if fa.Type.Parameter {
				return tTypeParameter, 0, true
			}
			if cls, found := r.a.Class(fa.Type.Name); found {
				return r.symbol(id, cls, typ, mods, ok)
			}
			if fa.Type.Name != "" && !inAnnotation(up) {
				return tClass, r.libraryMods(id, fa.Type.Name), true
			}
		}
		return typ, mods, ok
	}
	fa, found := r.fact(ref)
	if !found {
		return typ, mods, ok
	}
	switch {
	case fa.Symbol != 0:
		sym, _ := r.a.Result.Symbol(fa.Symbol)
		return r.symbol(id, sym, typ, mods, ok)
	case fa.Call != nil:
		t := uint32(tFunction)
		if strings.HasSuffix(fa.Call.Name, ".<init>") {
			t = tClass
		} else if fa.Call.Receiver != nil {
			t = tMethod
		}
		return t, r.libraryMods(id, fa.Call.Name), true
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
