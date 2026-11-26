package features

import (
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// DocumentSymbols returns the file's outline: its classes, objects,
// functions, properties, type aliases, constructors and enum entries,
// nested as they are declared. Local declarations inside bodies are left
// out, as IntelliJ's structure view leaves them out. Detail carries the
// signature: a function's parameters and return type, a property's type.
func DocumentSymbols(f *File) []protocol.DocumentSymbol {
	return f.symbols(f.Snapshot.RootNode(), false)
}

// symbols returns the symbols of the declarations among parent's
// children; inClass is set inside a class body.
func (f *File) symbols(parent psi.NodeRef, inClass bool) []protocol.DocumentSymbol {
	out := []protocol.DocumentSymbol{}
	for c := parent.FirstChild(); c.Valid(); c = c.NextSibling() {
		if s, ok := f.symbol(c, inClass); ok {
			out = append(out, s)
		}
	}
	return out
}

func (f *File) symbol(n psi.NodeRef, inClass bool) (protocol.DocumentSymbol, bool) {
	s := protocol.DocumentSymbol{Range: f.declRange(n)}
	sel := n.NameIdentifier()
	s.Name = n.Name()
	switch n.NumericKind() {
	case psi.KindCLASS:
		switch {
		case n.ChildKind(psi.KindINTERFACE_KEYWORD).Valid():
			s.Kind = protocol.SymbolInterface
		case hasModifier(n, psi.KindENUM_KEYWORD):
			s.Kind = protocol.SymbolEnum
		default:
			s.Kind = protocol.SymbolClass
		}
		if tp := n.ChildKind(psi.KindTYPE_PARAMETER_LIST); tp.Valid() {
			s.Detail = compact(tp)
		}
		var members []protocol.DocumentSymbol
		for _, p := range n.ChildKind(psi.KindPRIMARY_CONSTRUCTOR).Parameters() {
			if !p.ChildKind(psi.KindVAL_KEYWORD).Valid() && !p.ChildKind(psi.KindVAR_KEYWORD).Valid() {
				continue
			}
			if m, ok := f.property(p); ok {
				members = append(members, m)
			}
		}
		s.Children = append(members, f.symbols(n.ChildKind(psi.KindCLASS_BODY), true)...)
	case psi.KindOBJECT_DECLARATION:
		s.Kind = protocol.SymbolObject
		if !sel.Valid() {
			sel = n.ChildKind(psi.KindOBJECT_KEYWORD)
		}
		s.Children = f.symbols(n.ChildKind(psi.KindCLASS_BODY), true)
	case psi.KindFUN:
		s.Kind = protocol.SymbolFunction
		if inClass {
			s.Kind = protocol.SymbolMethod
		}
		s.Name = receiverPrefix(n) + s.Name
		s.Detail = funDetail(n)
	case psi.KindPROPERTY:
		p, ok := f.property(n)
		if !ok {
			return s, false
		}
		if !inClass && hasModifier(n, psi.KindCONST_KEYWORD) {
			p.Kind = protocol.SymbolConstant
		}
		return p, true
	case psi.KindTYPEALIAS:
		s.Kind = protocol.SymbolClass
		if t := n.ChildKind(psi.KindTYPE_REFERENCE); t.Valid() {
			s.Detail = "= " + compact(t)
		}
	case psi.KindSECONDARY_CONSTRUCTOR:
		s.Kind = protocol.SymbolConstructor
		s.Name = "constructor"
		sel = n.ChildKind(psi.KindCONSTRUCTOR_KEYWORD)
		s.Detail = compact(n.ChildKind(psi.KindVALUE_PARAMETER_LIST))
	case psi.KindENUM_ENTRY:
		s.Kind = protocol.SymbolEnumMember
		if body := n.ChildKind(psi.KindCLASS_BODY); body.Valid() {
			s.Children = f.symbols(body, true)
		}
	default:
		return s, false
	}
	if s.Name == "" || !sel.Valid() {
		return s, false
	}
	s.SelectionRange = f.Range(sel)
	if annotated(n, "Deprecated") {
		s.Tags = []protocol.SymbolTag{protocol.SymbolTagDeprecated}
	}
	return s, true
}

// property returns the symbol of a property, or of a val or var
// parameter of a primary constructor.
func (f *File) property(n psi.NodeRef) (protocol.DocumentSymbol, bool) {
	sel := n.NameIdentifier()
	if !sel.Valid() {
		// A destructuring declaration at the top level has no name.
		return protocol.DocumentSymbol{}, false
	}
	s := protocol.DocumentSymbol{
		Name:           receiverPrefix(n) + n.Name(),
		Kind:           protocol.SymbolProperty,
		Range:          f.declRange(n),
		SelectionRange: f.Range(sel),
	}
	if t := n.TypeReference(); t.Valid() {
		s.Detail = compact(t)
	}
	if annotated(n, "Deprecated") {
		s.Tags = []protocol.SymbolTag{protocol.SymbolTagDeprecated}
	}
	return s, true
}

// receiverPrefix returns "R." for an extension's receiver type R.
func receiverPrefix(n psi.NodeRef) string {
	if r := n.ReceiverType(); r.Valid() {
		return compact(r) + "."
	}
	return ""
}

// funDetail returns a function's signature after its name:
// "(x: Int): String", with its type parameters first.
func funDetail(n psi.NodeRef) string {
	d := ""
	if tp := n.ChildKind(psi.KindTYPE_PARAMETER_LIST); tp.Valid() {
		d = compact(tp) + " "
	}
	d += compact(n.ChildKind(psi.KindVALUE_PARAMETER_LIST))
	if r := n.ReturnType(); r.Valid() {
		d += ": " + compact(r)
	}
	return d
}

// FlatSymbols returns the outline as SymbolInformation, for clients
// without hierarchical document symbols.
func FlatSymbols(f *File, syms []protocol.DocumentSymbol) []protocol.SymbolInformation {
	out := []protocol.SymbolInformation{}
	var walk func(ss []protocol.DocumentSymbol, container string)
	walk = func(ss []protocol.DocumentSymbol, container string) {
		for _, s := range ss {
			out = append(out, protocol.SymbolInformation{
				Name: s.Name, Kind: s.Kind, Tags: s.Tags, ContainerName: container,
				Location: protocol.Location{URI: f.URI, Range: s.SelectionRange},
			})
			walk(s.Children, s.Name)
		}
	}
	walk(syms, "")
	return out
}

// declRange returns the range of a declaration less its leading KDoc
// and comments, so that it starts on the line of its modifiers or
// keyword, where readers, coding agents among them, look for it.
func (f *File) declRange(n psi.NodeRef) protocol.Range {
	start := n.Range().Start
	for c := n.FirstChild(); c.Valid(); c = c.NextSibling() {
		switch c.NumericKind() {
		case psi.KindKDOC, psi.KindWHITE_SPACE, psi.KindEOL_COMMENT, psi.KindBLOCK_COMMENT:
			continue
		}
		start = c.Range().Start
		break
	}
	return f.Mapper.Range(start, n.Range().End)
}
