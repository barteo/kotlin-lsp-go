package features

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// Analyzer returns the analysis of the module of the file at path, or
// nil if no module has it.
type Analyzer func(ctx context.Context, path string) (*Analysis, error)

// MoveFiles returns the edits that keep the code compiling when Kotlin
// files move, from the old paths moves maps to their new ones, before
// they do: each moved file's package directive follows its directory, as
// the directories of its old package showed it, and the imports and
// qualified references of its top-level declarations follow it, in the
// files of the modules that can see it. A file of the old package that
// used them gains imports, and so does a moved file that used the old
// package's other declarations. The edits are on the files' old URIs.
// A file whose old directory doesn't match its package moves without
// edits.
func MoveFiles(ctx context.Context, s Searcher, analyze Analyzer, moves map[string]string) (*protocol.WorkspaceEdit, error) {
	type moved struct {
		f        *File
		a        *Analysis
		old, new string // packages
	}
	var ms []moved
	newPkg := map[string]string{} // a moved file's path → its new package
	paths := make([]string, 0, len(moves))
	for p := range moves {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		a, err := analyze(ctx, p)
		if err != nil {
			return nil, err
		}
		if a == nil {
			continue
		}
		f := a.File(p)
		if f == nil {
			continue
		}
		old := f.Snapshot.RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE).Path()
		pkg, ok := packageFor(p, old, moves[p])
		if !ok || pkg == old {
			continue
		}
		ms = append(ms, moved{f, a, old, pkg})
		newPkg[p] = pkg
	}
	if len(ms) == 0 {
		return nil, nil
	}
	edits := map[protocol.DocumentURI][]protocol.TextEdit{}
	imports := map[*File][]string{} // the imports to add to a file
	addImport := func(f *File, fq string) {
		if !slices.Contains(imports[f], fq) {
			imports[f] = append(imports[f], fq)
		}
	}
	// The package a file has once the files have moved.
	packageOf := func(f *File) string {
		if p, ok := newPkg[f.Snapshot.Path()]; ok {
			return p
		}
		return f.Snapshot.RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE).Path()
	}
	for _, m := range ms {
		edits[m.f.URI] = append(edits[m.f.URI], packageEdit(m.f, m.new))
		// The references to its top-level declarations.
		for d := m.f.Snapshot.RootNode().FirstChild(); d.Valid(); d = d.NextSibling() {
			if !topLevel(d) {
				continue
			}
			id := d.NameIdentifier()
			k, ok := KeyAt(m.a, m.f, m.f.Mapper.Position(id.Range().Start))
			if !ok || !k.Valid() {
				continue
			}
			oldFQ, newFQ := qualify(m.old, k.Name), qualify(m.new, k.Name)
			scopes, err := s.Search(ctx, []string{k.Name}, m.f.Snapshot.Path())
			if err != nil {
				return nil, err
			}
			done := map[[2]any]bool{}
			for _, sc := range scopes {
				for _, sf := range sc.Files {
					for _, o := range Occurrences(sc.Analysis, sf, k) {
						if o.Kind == OccurrenceDeclaration || done[[2]any{sf.Snapshot.Path(), o.Node.Range().Start}] {
							continue
						}
						done[[2]any{sf.Snapshot.Path(), o.Node.Range().Start}] = true
						edits[sf.URI] = append(edits[sf.URI], referenceEdits(sf, o.Node, m.old, m.new, oldFQ, newFQ, packageOf(sf), addImport)...)
					}
				}
			}
		}
		// Its references to the other declarations of its old package.
		for _, fq := range oldPackageUses(m.a, m.f, m.old, moves) {
			addImport(m.f, fq)
		}
	}
	for f, fqs := range imports {
		edits[f.URI] = append(edits[f.URI], importEdits(f, fqs)...)
	}
	out := &protocol.WorkspaceEdit{Changes: map[protocol.DocumentURI][]protocol.TextEdit{}}
	for uri, es := range edits {
		out.Changes[uri] = dedupeEdits(es)
	}
	return out, nil
}

// importEdits returns the edits that import fqs into f, in order: as a
// new import list if f has none.
func importEdits(f *File, fqs []string) []protocol.TextEdit {
	slices.Sort(fqs)
	root := f.Snapshot.RootNode()
	if list := root.ChildKind(psi.KindIMPORT_LIST); list.Range().End > list.Range().Start {
		var out []protocol.TextEdit
		for _, fq := range fqs {
			if e, ok := importEdit(f, fq); ok {
				out = append(out, e)
			}
		}
		return out
	}
	block := "import " + strings.Join(fqs, "\nimport ")
	if pd := root.ChildKind(psi.KindPACKAGE_DIRECTIVE); pd.Range().End > pd.Range().Start {
		return []protocol.TextEdit{{Range: f.Mapper.Range(pd.Range().End, pd.Range().End), NewText: "\n\n" + block}}
	}
	return []protocol.TextEdit{{Range: f.Mapper.Range(0, 0), NewText: block + "\n\n"}}
}

// packageFor returns the package of a file of package pkg that moves
// from path to newPath, if path's directory is pkg's: a/b of the
// directory .../src/a/b for package a.b.
func packageFor(path, pkg, newPath string) (string, bool) {
	dir := filepath.ToSlash(filepath.Dir(path))
	base := dir
	if pkg != "" {
		suffix := "/" + strings.ReplaceAll(pkg, ".", "/")
		if !strings.HasSuffix(dir, suffix) {
			return "", false
		}
		base = strings.TrimSuffix(dir, suffix)
	}
	rel, err := filepath.Rel(filepath.FromSlash(base), filepath.Dir(newPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	for _, s := range segs {
		if !identifier.MatchString(s) || hardKeywords[s] {
			return "", false
		}
	}
	return strings.Join(segs, "."), true
}

// packageEdit returns the edit that gives f the package directive of
// pkg.
func packageEdit(f *File, pkg string) protocol.TextEdit {
	text := f.Mapper.Text()
	pd := f.Snapshot.RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE)
	r := pd.Range()
	switch {
	case r.End > r.Start && pkg != "":
		return protocol.TextEdit{Range: f.Mapper.Range(r.Start, r.End), NewText: "package " + pkg}
	case r.End > r.Start:
		end := r.End
		for end < len(text) && strings.ContainsRune(" \t\r\n", rune(text[end])) {
			end++
		}
		return protocol.TextEdit{Range: f.Mapper.Range(r.Start, end), NewText: ""}
	}
	at := r.Start
	return protocol.TextEdit{Range: f.Mapper.Range(at, at), NewText: "package " + pkg + "\n\n"}
}

// topLevel reports whether d is a top-level declaration that other files
// can see.
func topLevel(d psi.NodeRef) bool {
	switch d.NumericKind() {
	case psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindFUN, psi.KindPROPERTY, psi.KindTYPEALIAS:
	default:
		return false
	}
	if mods := d.ChildKind(psi.KindMODIFIER_LIST); mods.ChildKind(psi.KindPRIVATE_KEYWORD).Valid() {
		return false
	}
	return d.NameIdentifier().Valid()
}

// referenceEdits returns the edits of a reference ref in f, of package
// pkg once the files move, to a declaration that moves from package old
// to new: an import of it moves too, a qualified reference is qualified
// with the new package, and an unqualified one in another package than
// new needs an import.
func referenceEdits(f *File, ref psi.NodeRef, old, new, oldFQ, newFQ, pkg string, addImport func(*File, string)) []protocol.TextEdit {
	for n := ref; n.Valid(); n = n.Parent() {
		if n.NumericKind() == psi.KindIMPORT_DIRECTIVE {
			if n.Path() != oldFQ {
				return nil
			}
			repl := "import " + newFQ
			if n.Alias() != "" {
				repl += " as " + n.Alias()
			}
			return []protocol.TextEdit{{Range: f.Mapper.Range(n.Range().Start, n.Range().End), NewText: repl}}
		}
		if n.NumericKind() == psi.KindPACKAGE_DIRECTIVE {
			return nil
		}
	}
	name := ref
	for name.Parent().NumericKind() == psi.KindREFERENCE_EXPRESSION {
		name = name.Parent()
	}
	if isCallee(name) {
		name = name.Parent()
	}
	// a.b.C: the qualifier is the receiver or the type's qualifier.
	p := name.Parent()
	var qual psi.NodeRef
	switch p.NumericKind() {
	case psi.KindDOT_QUALIFIED_EXPRESSION:
		if p.FirstChild() != name {
			qual = p.FirstChild()
		}
	case psi.KindUSER_TYPE:
		if q := p.ChildKind(psi.KindUSER_TYPE); q.Valid() {
			qual = q
		}
	}
	if qual.Valid() {
		if strings.Join(strings.Fields(qual.Text()), "") != old {
			return nil // qualified by an object or class, not by the package
		}
		r := qual.Range()
		if new == "" {
			return []protocol.TextEdit{{Range: f.Mapper.Range(r.Start, name.Range().Start), NewText: ""}}
		}
		return []protocol.TextEdit{{Range: f.Mapper.Range(r.Start, r.End), NewText: new}}
	}
	if pkg != new && !importsName(f, newFQ, ref.Name()) {
		addImport(f, newFQ)
	}
	return nil
}

// importsName reports whether f imports name explicitly, or fq, which is
// moving: its import's edit keeps it.
func importsName(f *File, fq, name string) bool {
	for c := f.Snapshot.RootNode().ChildKind(psi.KindIMPORT_LIST).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() != psi.KindIMPORT_DIRECTIVE || c.IsAllUnder() {
			continue
		}
		if c.Alias() == name || c.Alias() == "" && strings.HasSuffix("."+c.Path(), "."+name) {
			return true
		}
	}
	return false
}

// oldPackageUses returns the declarations of package old that f, which
// moves out of it, names without importing, in files that don't move.
func oldPackageUses(a *Analysis, f *File, old string, moves map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	psi.WalkNodes(f.Snapshot.RootNode(), func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindIMPORT_LIST, psi.KindPACKAGE_DIRECTIVE:
			return false
		case psi.KindREFERENCE_EXPRESSION:
		default:
			return true
		}
		if p := n.Parent(); qualified(p) && p.FirstChild() != n || p.NumericKind() == psi.KindUSER_TYPE && p.ChildKind(psi.KindUSER_TYPE).Valid() {
			return true // a selector: its receiver says where it is
		}
		fq, decl := referencedDeclaration(a, f, n)
		if fq != qualify(old, n.Name()) || !decl.Valid() {
			return true
		}
		if df := decl.ContainingFile(); df == f.Snapshot || moves[df.Path()] != "" {
			return true
		}
		if !seen[fq] && !importsName(f, fq, n.Name()) {
			seen[fq] = true
			out = append(out, fq)
		}
		return true
	})
	slices.Sort(out)
	return out
}

// referencedDeclaration returns the qualified name an import of what the
// reference n names would have, and the declaration in the sources it
// names, if any: a class's for its constructor.
func referencedDeclaration(a *Analysis, f *File, n psi.NodeRef) (string, psi.NodeRef) {
	for m := n; m.Valid(); m = m.Parent() {
		fa, ok := a.Result.Fact(analysis.ElementID{Snapshot: f.Snapshot.ID(), Node: m.ID()})
		if ok && (fa.Symbol != 0 || fa.ImportableName != "") {
			fq := fa.ImportableName
			var s analysis.Symbol
			if fa.Symbol != 0 {
				s, _ = a.Result.Symbol(fa.Symbol)
			}
			switch {
			case fq != "":
			case s.Kind == "class" || s.Owner == "" && (s.Kind == "function" || s.Kind == "property"):
				fq = s.QualifiedName
			case s.Kind == "constructor":
				fq = strings.NewReplacer("/", ".", "$", ".").Replace(s.Owner)
			}
			if c, ok := a.Class(fq); ok && c.Declaration != nil {
				s = c
			}
			if s.Declaration != nil {
				if _, dn, ok := a.Node(*s.Declaration); ok {
					return fq, dn
				}
			}
			return fq, psi.NodeRef{}
		}
		if m.NumericKind() != psi.KindREFERENCE_EXPRESSION && m.NumericKind() != psi.KindUSER_TYPE && m.NumericKind() != psi.KindTYPE_REFERENCE {
			break
		}
	}
	return "", psi.NodeRef{}
}

// dedupeEdits drops the edits that repeat another, and those that overlap
// an earlier one.
func dedupeEdits(es []protocol.TextEdit) []protocol.TextEdit {
	var out []protocol.TextEdit
	for _, e := range es {
		ok := true
		for _, o := range out {
			if e == o || overlaps(e.Range, o.Range) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, e)
		}
	}
	return out
}

func overlaps(a, b protocol.Range) bool {
	before := func(x, y protocol.Position) bool {
		return x.Line < y.Line || x.Line == y.Line && x.Character < y.Character
	}
	// Insertions at one point don't overlap.
	return before(a.Start, b.End) && before(b.Start, a.End)
}
