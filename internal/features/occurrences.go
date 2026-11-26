package features

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// Key identifies a declaration in every analysis that sees it: a source
// declaration by its file and the offset of its node, which the analyses
// of all modules share, as they analyze the same snapshots; a library
// declaration by its kind, qualified name and parameters.
type Key struct {
	// Path and Offset locate a source declaration; Lib names a library
	// one: "function:kotlin.io.println(kotlin.Any)".
	Path   string
	Offset int
	Lib    string
	// Kind is the symbol's kind: class, function, constructor, property,
	// local or parameter.
	Kind string
	// Name is the simple name its references are written with.
	Name string
	// FQ is a class's or a top-level declaration's qualified name; Class
	// a class's JVM binary name, or a member's class's.
	FQ, Class string
	// QName is the symbol's qualified name, to find a library's
	// declaration with.
	QName string `json:",omitempty"`
	// Owner is the offset of the function, class or constructor whose
	// parameter a parameter is, for named arguments; -1 if none.
	Owner int
	// Params is a function's number of parameters, or -1.
	Params int
}

// Valid reports whether k names a declaration.
func (k Key) Valid() bool { return k.Path != "" || k.Lib != "" }

// Local reports whether only its own file can refer to the declaration.
func (k Key) Local() bool {
	return k.Kind == "local" || k.Kind == "parameter" && k.Path != "" || strings.HasPrefix(k.Lib, "local#")
}

// Source reports whether the declaration is in the sources.
func (k Key) Source() bool { return k.Path != "" }

// keyOf returns the key of a symbol, with the call through which it is
// referred to, if any.
func keyOf(a *Analysis, sym analysis.Symbol, call *analysis.Call) Key {
	k := Key{Kind: sym.Kind, QName: sym.QualifiedName, Owner: -1, Params: -1}
	switch sym.Kind {
	case "class":
		k.Name = sym.Name[strings.LastIndexAny(sym.Name, "/$")+1:]
		k.FQ, k.Class = sym.QualifiedName, sym.Name
	case "constructor":
		k.Class = sym.Owner
		k.Name = sym.Owner[strings.LastIndexAny(sym.Owner, "/$")+1:]
	default:
		k.Name = sym.Name
		k.Class = sym.Owner
		if sym.Owner == "" {
			k.FQ = sym.QualifiedName
		}
	}
	if call != nil {
		k.Params = len(call.Parameters)
	}
	if sym.Declaration != nil {
		if f, n, ok := a.Node(*sym.Declaration); ok {
			k.Path, k.Offset = f.Snapshot.Path(), n.Range().Start
			if n.NumericKind() == psi.KindFUN {
				k.Params = len(n.Parameters())
			}
			if n.NumericKind() == psi.KindVALUE_PARAMETER {
				if o := parameterOwner(n); o.Valid() {
					k.Owner = o.Range().Start
				}
			}
			return k
		}
	}
	if sym.QualifiedName == "" && sym.Kind != "class" {
		// A local without a declaration node, as an implicit it or one in
		// a statement with errors: its symbol's ID, which only this
		// analysis knows.
		k.Lib = fmt.Sprintf("local#%d", sym.ID)
		return k
	}
	name := sym.QualifiedName
	if sym.Kind == "class" {
		name = sym.Name
	}
	k.Lib = sym.Kind + ":" + name
	if call != nil && (sym.Kind == "function" || sym.Kind == "constructor") {
		k.Lib += "(" + erasedParams(call) + ")"
	}
	return k
}

// callKey returns the key of a call's callee that has no symbol, as a
// built-in member's.
func callKey(c *analysis.Call) Key {
	name := c.Name
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return Key{}
	}
	k := Key{Kind: "function", Name: name[i+1:], QName: name, Owner: -1, Params: len(c.Parameters)}
	if seg := name[strings.LastIndexByte(name[:i], '.')+1 : i]; seg != "" && unicode.IsUpper(rune(seg[0])) {
		k.Class = binaryName(name[:i]) // a member's: pkg.C.m
	}
	if k.Name == "<init>" {
		k.Kind, k.Class = "constructor", binaryName(name[:i])
		k.Name = name[strings.LastIndexByte(name[:i], '.')+1 : i]
	}
	k.Lib = k.Kind + ":" + name + "(" + erasedParams(c) + ")"
	return k
}

// classKey returns the key of the class whose qualified name is fq.
func classKey(a *Analysis, fq string) Key {
	if cls, ok := a.Class(fq); ok {
		return keyOf(a, cls, nil)
	}
	bin := binaryName(fq)
	return Key{Kind: "class", Name: fq[strings.LastIndexByte(fq, '.')+1:], FQ: fq, Class: bin, Lib: "class:" + bin, Owner: -1, Params: -1}
}

func erasedParams(c *analysis.Call) string {
	var ps []string
	for _, p := range c.Parameters {
		ps = append(ps, p.Name)
	}
	return strings.Join(ps, ",")
}

// parameterOwner returns the function, class (for its primary
// constructor) or secondary constructor a parameter belongs to.
func parameterOwner(p psi.NodeRef) psi.NodeRef {
	list := p.Parent()
	if list.NumericKind() != psi.KindVALUE_PARAMETER_LIST {
		return psi.NodeRef{}
	}
	o := list.Parent()
	if o.NumericKind() == psi.KindPRIMARY_CONSTRUCTOR {
		o = o.Parent()
	}
	switch o.NumericKind() {
	case psi.KindFUN, psi.KindCLASS, psi.KindSECONDARY_CONSTRUCTOR:
		return o
	}
	return psi.NodeRef{}
}

// KeyAt returns the key of the declaration that the position refers to
// or declares.
func KeyAt(a *Analysis, f *File, pos protocol.Position) (Key, bool) {
	t, ok := targetAt(a, f, pos)
	if !ok {
		return Key{}, false
	}
	return t.key(a), true
}

func (t target) key(a *Analysis) Key {
	switch {
	case t.sym.Kind != "":
		return keyOf(a, t.sym, t.fact.Call)
	case t.fact.Call != nil:
		return callKey(t.fact.Call)
	case t.fact.Type != nil && !t.fact.Type.Parameter && t.fact.Type.Name != "":
		return classKey(a, t.fact.Type.Name)
	}
	return Key{}
}

// The kinds of occurrences.
const (
	OccurrenceRead = iota
	OccurrenceWrite
	OccurrenceDeclaration
	OccurrenceImport
	OccurrenceNamedArgument
)

// Occurrence is where a file declares or refers to a declaration.
type Occurrence struct {
	File *File
	// Node is the name: an identifier, a reference, or a named argument's
	// name.
	Node psi.NodeRef
	Kind int
}

// Range returns the occurrence's range.
func (o Occurrence) Range() protocol.Range { return o.File.Range(o.Node) }

// Occurrences returns the occurrences of the declaration k in f, one of
// a's files, in order: its declaration, references, a class's
// constructor calls and type references, a parameter's named arguments,
// and imports.
func Occurrences(a *Analysis, f *File, k Key) []Occurrence {
	af := a.File(f.Snapshot.Path())
	if af == nil || !k.Valid() {
		return nil
	}
	id := func(n psi.NodeRef) analysis.ElementID {
		return analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: n.ID()}
	}
	var out []Occurrence
	add := func(n psi.NodeRef, kind int) { out = append(out, Occurrence{File: af, Node: n, Kind: kind}) }
	// Cache the keys of the symbols seen: a file refers to few.
	keys := map[uint32]Key{}
	symKey := func(fa analysis.Fact) Key {
		if kk, ok := keys[fa.Symbol]; ok {
			return kk
		}
		s, _ := a.Result.Symbol(fa.Symbol)
		kk := keyOf(a, s, fa.Call)
		keys[fa.Symbol] = kk
		return kk
	}
	matches := func(kk Key) bool {
		if kk.Path != "" || k.Path != "" {
			return kk.Path == k.Path && kk.Offset == k.Offset
		}
		return kk.Lib == k.Lib
	}
	psi.WalkNodes(af.Snapshot.RootNode(), func(n psi.NodeRef) bool {
		if n.IsLeaf() {
			return false
		}
		switch kind := n.NumericKind(); {
		case kind == psi.KindIMPORT_DIRECTIVE:
			if k.FQ != "" && n.Alias() == "" && !n.IsAllUnder() && n.Path() == k.FQ {
				if last := lastImportName(n); last.Valid() {
					add(last, OccurrenceImport)
				}
			}
			return false
		case kind == psi.KindPACKAGE_DIRECTIVE:
			return false
		case isDeclaration(n):
			if k.Path == af.Snapshot.Path() && n.Range().Start == k.Offset && n.NumericKind() != psi.KindSECONDARY_CONSTRUCTOR &&
				n.NumericKind() != psi.KindPRIMARY_CONSTRUCTOR {
				if name := n.NameIdentifier(); name.Valid() {
					add(name, OccurrenceDeclaration)
				}
			}
		case kind == psi.KindUSER_TYPE:
			if k.Kind == "class" && k.FQ != "" {
				if fa, ok := a.Result.Fact(id(n)); ok && fa.Type != nil && fa.Type.Name == k.FQ {
					if ref := n.ChildKind(psi.KindREFERENCE_EXPRESSION); ref.Valid() {
						add(ref, OccurrenceRead)
					}
				}
			}
		case kind == psi.KindREFERENCE_EXPRESSION:
			if n.Parent().NumericKind() == psi.KindVALUE_ARGUMENT_NAME {
				if k.Owner >= 0 && n.Name() == k.Name && namedArgumentOf(a, af, n, k) {
					add(n, OccurrenceNamedArgument)
				}
				return false
			}
			fa, ok := a.Result.Fact(id(n))
			if !ok {
				return true
			}
			var kk Key
			switch {
			case fa.Symbol != 0:
				kk = symKey(fa)
			case fa.Call != nil:
				kk = callKey(fa.Call)
			default:
				return true
			}
			match := matches(kk)
			if !match && k.Kind == "class" && kk.Kind == "constructor" && kk.Class == k.Class && k.Class != "" {
				match = true // a secondary constructor's call
			}
			if match {
				kind := OccurrenceRead
				if isAssigned(n) {
					kind = OccurrenceWrite
				}
				add(n, kind)
			}
		}
		return true
	})
	return out
}

// namedArgumentOf reports whether the named argument ref names the
// parameter k: whether its call's callee is the parameter's owner.
func namedArgumentOf(a *Analysis, af *File, ref psi.NodeRef, k Key) bool {
	call := ref.Parent()
	for call.Valid() && call.NumericKind() != psi.KindCALL_EXPRESSION && call.NumericKind() != psi.KindSUPER_TYPE_CALL_ENTRY &&
		call.NumericKind() != psi.KindCONSTRUCTOR_DELEGATION_CALL && call.NumericKind() != psi.KindANNOTATION_ENTRY {
		call = call.Parent()
	}
	if !call.Valid() {
		return false
	}
	fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: af.Snapshot.ID(), Node: call.ID()})
	if !ok || fa.Call == nil || fa.Call.Declaration == nil {
		return false
	}
	df, dn, ok := a.Node(*fa.Call.Declaration)
	return ok && df.Snapshot.Path() == k.Path && dn.Range().Start == k.Owner
}
