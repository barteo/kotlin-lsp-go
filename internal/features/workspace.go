package features

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// Searcher finds the files of the workspace that may refer to a
// declaration, and their analyses.
type Searcher interface {
	// Search returns, for each module that can see the declarations of
	// the file at from (every module if from is empty), its analysis and
	// its own files that contain one of words.
	Search(ctx context.Context, words []string, from string) ([]Scope, error)
}

// Scope is a module's analysis and some of its own files.
type Scope struct {
	Analysis *Analysis
	Files    []*File
}

// find returns the occurrences of k in the workspace: in f alone for a
// local, else in the files of the modules that can see it.
func find(ctx context.Context, s Searcher, a *Analysis, f *File, k Key) ([]Occurrence, error) {
	if k.Local() {
		if a == nil {
			return nil, nil
		}
		if f == nil {
			f = a.File(k.Path)
		}
		if f == nil {
			return nil, nil
		}
		return Occurrences(a, f, k), nil
	}
	scopes, err := s.Search(ctx, []string{k.Name}, k.Path)
	if err != nil {
		return nil, err
	}
	seen := map[[2]any]bool{}
	var out []Occurrence
	for _, sc := range scopes {
		for _, sf := range sc.Files {
			for _, o := range Occurrences(sc.Analysis, sf, k) {
				id := [2]any{sf.Snapshot.Path(), o.Node.Range().Start}
				if !seen[id] {
					seen[id] = true
					out = append(out, o)
				}
			}
		}
	}
	sortOccurrences(out)
	return out, nil
}

func sortOccurrences(os []Occurrence) {
	slices.SortFunc(os, func(a, b Occurrence) int {
		return cmp.Or(strings.Compare(a.File.Snapshot.Path(), b.File.Snapshot.Path()), a.Node.Range().Start-b.Node.Range().Start)
	})
}

// References returns the locations of the references to the declaration
// at pos, and of the declaration itself if includeDecl is set.
func References(ctx context.Context, s Searcher, a *Analysis, f *File, pos protocol.Position, includeDecl bool) ([]protocol.Location, error) {
	k, ok := KeyAt(a, f, pos)
	if !ok || !k.Valid() {
		return nil, nil
	}
	// A member's references include those of the members that override
	// it: a call of an override may be a call of it.
	keys := []Key{k}
	if k.Class != "" && (k.Kind == "function" || k.Kind == "property") {
		decls, err := overriders(ctx, s, k)
		if err != nil {
			return nil, err
		}
		keys = appendDeclKeys(keys, decls)
	}
	var occs []Occurrence
	seen := map[[2]any]bool{}
	for _, kk := range keys {
		os, err := find(ctx, s, a, f, kk)
		if err != nil {
			return nil, err
		}
		for _, o := range os {
			if o.Kind == OccurrenceDeclaration && kk != k {
				continue // an override is an implementation, not a reference
			}
			id := [2]any{o.File.Snapshot.Path(), o.Node.Range().Start}
			if !seen[id] {
				seen[id] = true
				occs = append(occs, o)
			}
		}
	}
	sortOccurrences(occs)
	out := []protocol.Location{}
	for _, o := range occs {
		if o.Kind == OccurrenceDeclaration && !includeDecl {
			continue
		}
		out = append(out, protocol.Location{URI: o.File.URI, Range: o.Range()})
	}
	return out, nil
}

// ---- rename ----

// ErrRename is the error of a rename that can't be done.
var ErrRename = errors.New("can't rename")

var identifier = regexp.MustCompile(`^[\p{L}_][\p{L}\p{N}_]*$`)

// hardKeywords can't be names unless quoted in backticks.
var hardKeywords = map[string]bool{
	"as": true, "break": true, "class": true, "continue": true, "do": true, "else": true, "false": true,
	"for": true, "fun": true, "if": true, "in": true, "interface": true, "is": true, "null": true,
	"object": true, "package": true, "return": true, "super": true, "this": true, "throw": true,
	"true": true, "try": true, "typealias": true, "typeof": true, "val": true, "var": true,
	"when": true, "while": true,
}

// PrepareRename returns the range of the name at pos and the name, if
// the declaration it refers to is in the sources and can be renamed.
func PrepareRename(a *Analysis, f *File, pos protocol.Position) (*protocol.PrepareRenameResult, error) {
	t, ok := targetAt(a, f, pos)
	if !ok {
		return nil, nil
	}
	k := t.key(a)
	if !k.Source() {
		return nil, errors.New("can't rename a library's declaration")
	}
	name := t.origin
	if !name.IsLeaf() {
		name = identifierAt(t.f.Snapshot, name.Range().Start)
	}
	return &protocol.PrepareRenameResult{Range: t.f.Range(name), Placeholder: strings.Trim(name.Text(), "`")}, nil
}

// Rename returns the edits that rename the declaration at pos, its
// references, the declarations it overrides and those that override it.
func Rename(ctx context.Context, s Searcher, a *Analysis, f *File, pos protocol.Position, newName string) (*protocol.WorkspaceEdit, error) {
	text := strings.Trim(newName, "`")
	if !identifier.MatchString(text) {
		return nil, errors.New("not a Kotlin name: " + newName)
	}
	if hardKeywords[text] || strings.HasPrefix(newName, "`") {
		text = "`" + text + "`"
	}
	k, ok := KeyAt(a, f, pos)
	if !ok || !k.Valid() {
		return nil, errors.New("no declaration here to rename")
	}
	if !k.Source() {
		return nil, errors.New("can't rename a library's declaration")
	}
	if k.Kind == "constructor" {
		// A constructor is renamed with its class.
		if ck := classKey(a, fqOfBinary(k.Class)); ck.Source() {
			k = ck
		}
	}
	keys, err := related(ctx, s, a, k)
	if err != nil {
		return nil, err
	}
	edit := &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{}}
	seen := map[[2]any]bool{}
	for _, kk := range keys {
		occs, err := find(ctx, s, a, f, kk)
		if err != nil {
			return nil, err
		}
		for _, o := range occs {
			id := [2]any{o.File.Snapshot.Path(), o.Node.Range().Start}
			if seen[id] {
				continue
			}
			seen[id] = true
			edit.Changes[o.File.URI] = append(edit.Changes[o.File.URI], protocol.TextEdit{Range: o.Range(), NewText: text})
		}
	}
	return edit, nil
}

func fqOfBinary(bin string) string {
	return strings.NewReplacer("/", ".", "$", ".").Replace(bin)
}

// related returns k and, for a member, the members it overrides and those
// that override it, in the sources.
func related(ctx context.Context, s Searcher, a *Analysis, k Key) ([]Key, error) {
	keys := []Key{k}
	if k.Class == "" || k.Kind != "function" && k.Kind != "property" {
		return keys, nil
	}
	// Up: the members of the supertypes in the sources with its name.
	if cls, ok := a.Class(fqOfBinary(k.Class)); ok {
		for _, sup := range cls.Supertypes {
			if sk, ok := memberKey(a, sup, k); ok {
				keys = append(keys, sk)
			}
		}
	}
	// Down: their overrides.
	for _, kk := range slices.Clone(keys) {
		decls, err := overriders(ctx, s, kk)
		if err != nil {
			return nil, err
		}
		keys = appendDeclKeys(keys, decls)
	}
	return keys, nil
}

// appendDeclKeys appends the keys of declarations that keys lacks.
func appendDeclKeys(keys []Key, decls []decl) []Key {
	for _, d := range decls {
		if fa, ok := d.a.Result.Fact(analysis.ElementID{Snapshot: d.f.Snapshot.ID(), Node: d.n.ID()}); ok && fa.Symbol != 0 {
			sym, _ := d.a.Result.Symbol(fa.Symbol)
			if dk := keyOf(d.a, sym, nil); !slices.Contains(keys, dk) {
				keys = append(keys, dk)
			}
		}
	}
	return keys
}

// memberKey returns the key of the member like k of the class fq in the
// sources.
func memberKey(a *Analysis, fq string, k Key) (Key, bool) {
	cls, ok := a.Class(fq)
	if !ok || cls.Declaration == nil {
		return Key{}, false
	}
	df, dn, ok := a.Node(*cls.Declaration)
	if !ok {
		return Key{}, false
	}
	m := member(dn, k)
	if !m.Valid() {
		return Key{}, false
	}
	fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: df.Snapshot.ID(), Node: m.ID()})
	if !ok || fa.Symbol == 0 {
		return Key{}, false
	}
	sym, _ := a.Result.Symbol(fa.Symbol)
	return keyOf(a, sym, nil), true
}

// member returns the member of the class cls with k's name and kind, and
// for a function the same number of parameters.
func member(cls psi.NodeRef, k Key) psi.NodeRef {
	for c := cls.ChildKind(psi.KindCLASS_BODY).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.Name() != k.Name {
			continue
		}
		switch c.NumericKind() {
		case psi.KindFUN:
			if k.Kind == "function" && (k.Params < 0 || len(c.Parameters()) == k.Params) {
				return c
			}
		case psi.KindPROPERTY:
			if k.Kind == "property" {
				return c
			}
		}
	}
	if k.Kind == "property" {
		for _, p := range cls.ChildKind(psi.KindPRIMARY_CONSTRUCTOR).Parameters() {
			if p.Name() == k.Name && (p.ChildKind(psi.KindVAL_KEYWORD).Valid() || p.ChildKind(psi.KindVAR_KEYWORD).Valid()) {
				return p
			}
		}
	}
	return psi.NodeRef{}
}

// ---- implementations ----

// decl is a declaration in an analyzed file.
type decl struct {
	a *Analysis
	f *File
	n psi.NodeRef
}

func (d decl) location() protocol.Location {
	sel := d.n
	if id := d.n.NameIdentifier(); id.Valid() {
		sel = id
	}
	return protocol.Location{URI: d.f.URI, Range: d.f.Range(sel)}
}

// Implementations returns the classes that extend or implement the class
// at pos, directly or not, or the members that override the member at
// pos.
func Implementations(ctx context.Context, s Searcher, a *Analysis, f *File, pos protocol.Position) ([]protocol.Location, error) {
	k, ok := KeyAt(a, f, pos)
	if !ok {
		return nil, nil
	}
	var decls []decl
	var err error
	switch {
	case k.Kind == "class":
		decls, err = subclasses(ctx, s, k, false)
	case k.Class != "" && (k.Kind == "function" || k.Kind == "property"):
		decls, err = overriders(ctx, s, k)
	}
	if err != nil {
		return nil, err
	}
	out := []protocol.Location{}
	for _, d := range decls {
		out = append(out, d.location())
	}
	return out, nil
}

// classDecls calls fn with each class, interface and object declared in
// the scopes' files, and its symbol.
func classDecls(scopes []Scope, fn func(d decl, sym analysis.Symbol)) {
	for _, sc := range scopes {
		for _, sf := range sc.Files {
			psi.WalkNodes(sf.Snapshot.RootNode(), func(n psi.NodeRef) bool {
				switch n.NumericKind() {
				case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
				case psi.KindIMPORT_LIST, psi.KindPACKAGE_DIRECTIVE:
					return false
				default:
					return true
				}
				fa, ok := sc.Analysis.Result.Fact(analysis.ElementID{Snapshot: sf.Snapshot.ID(), Node: n.ID()})
				if ok && fa.Symbol != 0 {
					if sym, ok := sc.Analysis.Result.Symbol(fa.Symbol); ok && sym.Kind == "class" {
						fn(decl{sc.Analysis, sf, n}, sym)
					}
				}
				return true
			})
		}
	}
}

// subclasses returns the classes in the sources whose supertypes include
// the class k: its direct subclasses if direct is set, else all.
func subclasses(ctx context.Context, s Searcher, k Key, direct bool) ([]decl, error) {
	if k.FQ == "" {
		return nil, nil
	}
	names := []string{k.Name}
	searched := map[string]bool{}
	seen := map[[2]any]bool{}
	var out []decl
	for len(names) > 0 {
		for _, n := range names {
			searched[n] = true
		}
		scopes, err := s.Search(ctx, names, k.Path)
		if err != nil {
			return nil, err
		}
		names = nil
		classDecls(scopes, func(d decl, sym analysis.Symbol) {
			id := [2]any{d.f.Snapshot.Path(), d.n.Range().Start}
			if seen[id] || !slices.Contains(sym.Supertypes, k.FQ) || sym.QualifiedName == k.FQ {
				return
			}
			if direct && !slices.Contains(directSupertypes(d), k.FQ) {
				return
			}
			seen[id] = true
			out = append(out, d)
			if name := d.n.Name(); name != "" && !searched[name] && !direct {
				names = append(names, name)
			}
		})
	}
	slices.SortFunc(out, func(a, b decl) int {
		return cmp.Or(strings.Compare(a.f.Snapshot.Path(), b.f.Snapshot.Path()), a.n.Range().Start-b.n.Range().Start)
	})
	return out, nil
}

// directSupertypes returns the qualified names of the types a class's
// declaration lists as its supertypes.
func directSupertypes(d decl) []string {
	var out []string
	list := d.n.ChildKind(psi.KindSUPER_TYPE_LIST)
	psi.WalkNodes(list, func(n psi.NodeRef) bool {
		if n.NumericKind() != psi.KindUSER_TYPE {
			return true
		}
		if fa, ok := d.a.Result.Fact(analysis.ElementID{Snapshot: d.f.Snapshot.ID(), Node: n.ID()}); ok && fa.Type != nil {
			out = append(out, fa.Type.Name)
		}
		return false
	})
	return out
}

// overriders returns the members of the subclasses of k's class that
// override the member k.
func overriders(ctx context.Context, s Searcher, k Key) ([]decl, error) {
	owner := fqOfBinary(k.Class)
	ck := Key{Kind: "class", Name: owner[strings.LastIndexByte(owner, '.')+1:], FQ: owner, Path: k.Path}
	classes, err := subclasses(ctx, s, ck, false)
	if err != nil {
		return nil, err
	}
	var out []decl
	for _, c := range classes {
		if m := member(c.n, k); m.Valid() && hasModifier(m, psi.KindOVERRIDE_KEYWORD) {
			out = append(out, decl{c.a, c.f, m})
		}
	}
	return out, nil
}

// ---- hierarchies ----

// declAt returns the declaration whose node starts at off in f.
func declAt(f *File, off int) psi.NodeRef {
	for n := f.Snapshot.NodeAt(off); n.Valid(); n = n.Parent() {
		if n.Range().Start != off {
			if n.Range().Start < off {
				break
			}
			continue
		}
		if isDeclaration(n) || n.NumericKind() == psi.KindENUM_ENTRY {
			return n
		}
	}
	return psi.NodeRef{}
}

// item returns the hierarchy item of a declaration in the sources.
func item(f *File, n psi.NodeRef, k Key) (protocol.HierarchyItem, bool) {
	inClass := n.Parent().NumericKind() == psi.KindCLASS_BODY
	var sym protocol.DocumentSymbol
	var ok bool
	if n.NumericKind() == psi.KindVALUE_PARAMETER {
		sym, ok = f.property(n)
	} else {
		sym, ok = f.symbol(n, inClass)
	}
	if !ok {
		return protocol.HierarchyItem{}, false
	}
	if k.Kind == "constructor" && n.NumericKind() == psi.KindCLASS {
		sym.Kind = protocol.SymbolConstructor
	}
	data, _ := json.Marshal(k)
	return protocol.HierarchyItem{
		Name: sym.Name, Kind: sym.Kind, Tags: sym.Tags, Detail: containerOf(n), URI: f.URI,
		Range: sym.Range, SelectionRange: sym.SelectionRange, Data: data,
	}, true
}

// containerOf names a declaration's class, or its file's package.
func containerOf(n psi.NodeRef) string {
	var names []string
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.NumericKind() {
		case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
			if p.Name() != "" {
				names = append([]string{p.Name()}, names...)
			}
		case psi.KindFUN:
			names = append([]string{p.Name() + "()"}, names...)
		}
	}
	if pkg := n.ContainingFile().RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE); pkg.Valid() {
		if p := pkg.Path(); p != "" {
			names = append([]string{p}, names...)
		}
	}
	return strings.Join(names, ".")
}

// keyItem returns the item of the declaration k: in the sources, or in a
// library's sources.
func keyItem(ctx context.Context, a *Analysis, k Key) (protocol.HierarchyItem, bool) {
	if k.Source() {
		f := a.File(k.Path)
		if f == nil {
			return protocol.HierarchyItem{}, false
		}
		n := declAt(f, k.Offset)
		if !n.Valid() {
			return protocol.HierarchyItem{}, false
		}
		return item(f, n, k)
	}
	if a.Libraries == nil || strings.HasPrefix(k.Lib, "local#") {
		return protocol.HierarchyItem{}, false
	}
	d := LibraryDecl{Kind: k.Kind, QualifiedName: k.QName, Owner: k.Class, Parameters: k.Params}
	if k.Kind == "class" {
		d = LibraryDecl{Kind: "class", Class: k.Class, Parameters: -1}
	}
	lt, ok := a.Libraries.Find(ctx, d)
	if !ok {
		return protocol.HierarchyItem{}, false
	}
	data, _ := json.Marshal(k)
	if lt.Node.Valid() {
		it, ok := item(lt.File, lt.Node, k)
		it.Data = data
		return it, ok
	}
	r := lt.File.Mapper.Range(lt.Start, lt.End)
	kind := protocol.SymbolMethod
	if k.Kind == "class" {
		kind = protocol.SymbolClass
	}
	return protocol.HierarchyItem{Name: k.Name, Kind: kind, URI: lt.File.URI, Range: r, SelectionRange: r, Data: data}, true
}

// ItemKey returns the key an item's data holds.
func ItemKey(it protocol.HierarchyItem) (Key, bool) {
	var k Key
	if len(it.Data) == 0 || json.Unmarshal(it.Data, &k) != nil || !k.Valid() {
		return Key{}, false
	}
	return k, true
}

// PrepareTypeHierarchy returns the item of the class at pos.
func PrepareTypeHierarchy(ctx context.Context, a *Analysis, f *File, pos protocol.Position) []protocol.HierarchyItem {
	k, ok := KeyAt(a, f, pos)
	if !ok {
		return nil
	}
	if k.Kind == "constructor" {
		k = classKey(a, fqOfBinary(k.Class))
	}
	if k.Kind != "class" {
		return nil
	}
	if it, ok := keyItem(ctx, a, k); ok {
		return []protocol.HierarchyItem{it}
	}
	return nil
}

// Supertypes returns the items of the types the class k lists as its
// supertypes; a is the analysis of its file.
func Supertypes(ctx context.Context, a *Analysis, k Key) []protocol.HierarchyItem {
	if !k.Source() || a == nil {
		return nil
	}
	f := a.File(k.Path)
	if f == nil {
		return nil
	}
	n := declAt(f, k.Offset)
	if !n.Valid() {
		return nil
	}
	out := []protocol.HierarchyItem{}
	for _, fq := range directSupertypes(decl{a, f, n}) {
		if it, ok := keyItem(ctx, a, classKey(a, fq)); ok {
			out = append(out, it)
		}
	}
	return out
}

// Subtypes returns the items of the classes in the sources that list the
// class k as a supertype.
func Subtypes(ctx context.Context, s Searcher, k Key) ([]protocol.HierarchyItem, error) {
	decls, err := subclasses(ctx, s, k, true)
	if err != nil {
		return nil, err
	}
	out := []protocol.HierarchyItem{}
	for _, d := range decls {
		if it, ok := declItem(d); ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// declItem returns the item of a declaration, with its key.
func declItem(d decl) (protocol.HierarchyItem, bool) {
	fa, ok := d.a.Result.Fact(analysis.ElementID{Snapshot: d.f.Snapshot.ID(), Node: d.n.ID()})
	if !ok || fa.Symbol == 0 {
		return protocol.HierarchyItem{}, false
	}
	sym, _ := d.a.Result.Symbol(fa.Symbol)
	return item(d.f, d.n, keyOf(d.a, sym, nil))
}

// PrepareCallHierarchy returns the item of the function or constructor at
// pos.
func PrepareCallHierarchy(ctx context.Context, a *Analysis, f *File, pos protocol.Position) []protocol.HierarchyItem {
	k, ok := KeyAt(a, f, pos)
	if !ok || k.Kind != "function" && k.Kind != "constructor" {
		return nil
	}
	if it, ok := keyItem(ctx, a, k); ok {
		return []protocol.HierarchyItem{it}
	}
	return nil
}

// IncomingCalls returns the declarations that call the function k, with
// the ranges of the calls in each.
func IncomingCalls(ctx context.Context, s Searcher, a *Analysis, k Key) ([]protocol.CallHierarchyIncomingCall, error) {
	occs, err := find(ctx, s, a, nil, k)
	if err != nil {
		return nil, err
	}
	out := []protocol.CallHierarchyIncomingCall{}
	index := map[[2]any]int{}
	for _, o := range occs {
		if o.Kind != OccurrenceRead {
			continue
		}
		caller := enclosingCallable(o.Node)
		if !caller.Valid() {
			continue
		}
		id := [2]any{o.File.Snapshot.Path(), caller.Range().Start}
		i, ok := index[id]
		if !ok {
			it, ok := callerItem(o, caller)
			if !ok {
				continue
			}
			i = len(out)
			index[id] = i
			out = append(out, protocol.CallHierarchyIncomingCall{From: it})
		}
		out[i].FromRanges = append(out[i].FromRanges, o.Range())
	}
	return out, nil
}

// callerItem returns the item of the declaration that holds an
// occurrence, with its key from the analysis the occurrence is in.
func callerItem(o Occurrence, caller psi.NodeRef) (protocol.HierarchyItem, bool) {
	k := Key{Path: o.File.Snapshot.Path(), Offset: caller.Range().Start, Kind: "function", Name: caller.Name(), Owner: -1, Params: -1}
	switch caller.NumericKind() {
	case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
		k.Kind = "class"
	case psi.KindPROPERTY:
		k.Kind = "property"
	case psi.KindSECONDARY_CONSTRUCTOR:
		k.Kind = "constructor"
	}
	return item(o.File, caller, k)
}

// enclosingCallable returns the function, constructor, property or class
// whose code holds n.
func enclosingCallable(n psi.NodeRef) psi.NodeRef {
	for p := n.Parent(); p.Valid(); p = p.Parent() {
		switch p.NumericKind() {
		case psi.KindFUN, psi.KindSECONDARY_CONSTRUCTOR, psi.KindCLASS, psi.KindOBJECT_DECLARATION:
			return p
		case psi.KindPROPERTY:
			if k := p.Parent().NumericKind(); k == psi.KindFILE || k == psi.KindCLASS_BODY {
				return p
			}
		}
	}
	return psi.NodeRef{}
}

// OutgoingCalls returns the functions and constructors the function k
// calls, with the ranges of the calls; a is the analysis of its file.
func OutgoingCalls(ctx context.Context, a *Analysis, k Key) []protocol.CallHierarchyOutgoingCall {
	if !k.Source() || a == nil {
		return nil
	}
	f := a.File(k.Path)
	if f == nil {
		return nil
	}
	fn := declAt(f, k.Offset)
	if !fn.Valid() {
		return nil
	}
	out := []protocol.CallHierarchyOutgoingCall{}
	index := map[string]int{}
	keys := map[uint32]Key{}
	psi.WalkNodes(fn, func(n psi.NodeRef) bool {
		if n.NumericKind() == psi.KindKDOC {
			return false
		}
		at := n // where the call is written
		switch n.NumericKind() {
		case psi.KindREFERENCE_EXPRESSION:
		case psi.KindARRAY_ACCESS_EXPRESSION:
			at = n.ChildKind(psi.KindINDICES)
		case psi.KindBINARY_EXPRESSION, psi.KindPREFIX_EXPRESSION, psi.KindPOSTFIX_EXPRESSION:
			at = n.ChildKind(psi.KindOPERATION_REFERENCE)
		default:
			return true
		}
		if !at.Valid() {
			at = n
		}
		fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: f.Snapshot.ID(), Node: n.ID()})
		if !ok || fa.Call == nil {
			return true
		}
		var tk Key
		switch {
		case fa.Symbol != 0:
			var seen bool
			if tk, seen = keys[fa.Symbol]; !seen {
				sym, _ := a.Result.Symbol(fa.Symbol)
				tk = keyOf(a, sym, fa.Call)
				keys[fa.Symbol] = tk
			}
		case fa.Call.Declaration != nil:
			// An operator in the sources.
			tk = callKey(fa.Call)
			if df, dn, ok := a.Node(*fa.Call.Declaration); ok {
				if dfa, ok := a.Result.Fact(analysis.ElementID{Snapshot: df.Snapshot.ID(), Node: dn.ID()}); ok && dfa.Symbol != 0 {
					sym, _ := a.Result.Symbol(dfa.Symbol)
					tk = keyOf(a, sym, fa.Call)
				}
			}
		default:
			tk = callKey(fa.Call)
		}
		if tk.Kind != "function" && tk.Kind != "constructor" {
			return true
		}
		id := tk.Lib
		if tk.Source() {
			id = fmt.Sprintf("%s#%d", tk.Path, tk.Offset)
		}
		i, ok := index[id]
		if !ok {
			it, ok := keyItem(ctx, a, tk)
			if !ok {
				return true
			}
			i = len(out)
			index[id] = i
			out = append(out, protocol.CallHierarchyOutgoingCall{To: it})
		}
		out[i].FromRanges = append(out[i].FromRanges, f.Range(at))
		return true
	})
	return out
}
