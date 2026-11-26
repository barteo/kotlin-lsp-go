package features

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// Analysis is a module's semantic analysis: kc's facts about the files
// it analyzed, the module's own and those of the modules it depends on.
type Analysis struct {
	Result *analysis.Result
	// Module is the ID of the module analyzed.
	Module string
	// Libraries finds library declarations in sources jars; nil if the
	// module has none.
	Libraries Libraries

	files map[string]*File // by path
	byID  map[psi.SnapshotID]*File
	own   map[string]bool

	classesOnce sync.Once
	classes     map[string]uint32 // a class's qualified name → symbol
}

// Libraries finds the declarations of library symbols in sources.
type Libraries interface {
	// Find returns where a library declaration is in its sources, or
	// false.
	Find(ctx context.Context, d LibraryDecl) (LibraryTarget, bool)
}

// LibraryTarget is a library declaration in its sources: Node in a Kotlin
// file, or the byte range of the name in a Java file and its Javadoc.
type LibraryTarget struct {
	File       *File
	Node       psi.NodeRef
	Start, End int
	Doc        string
}

// LibraryDecl names a library declaration.
type LibraryDecl struct {
	// Kind is the symbol's kind: "class", "function", "constructor" or
	// "property".
	Kind string
	// QualifiedName is the declaration's fully qualified name, and Owner
	// the JVM binary name of a member's class.
	QualifiedName, Owner string
	// Class is a class's JVM binary name, a/b/C$D.
	Class string
	// Parameters is a function's number of parameters, or -1.
	Parameters int
}

// NewAnalysis returns the analysis res of files, of which own are the
// module's own.
func NewAnalysis(res *analysis.Result, module string, files []*File, own []string, libs Libraries) *Analysis {
	a := &Analysis{Result: res, Module: module, Libraries: libs, files: map[string]*File{}, byID: map[psi.SnapshotID]*File{}, own: map[string]bool{}}
	for _, f := range files {
		a.files[f.Snapshot.Path()] = f
		a.byID[f.Snapshot.ID()] = f
	}
	for _, p := range own {
		a.own[p] = true
	}
	return a
}

// File returns the analyzed file at path, or nil.
func (a *Analysis) File(path string) *File { return a.files[path] }

// Files returns the analyzed files, by path.
func (a *Analysis) Files() []*File {
	out := make([]*File, 0, len(a.files))
	for _, p := range slices.Sorted(maps.Keys(a.files)) {
		out = append(out, a.files[p])
	}
	return out
}

// FileOf returns the analyzed file of a snapshot, or nil.
func (a *Analysis) FileOf(id psi.SnapshotID) *File { return a.byID[id] }

// Own reports whether path is one of the module's own files.
func (a *Analysis) Own(path string) bool { return a.own[path] }

// Node returns the file and node an element ID names.
func (a *Analysis) Node(id analysis.ElementID) (*File, psi.NodeRef, bool) {
	f := a.byID[id.Snapshot]
	if f == nil {
		return nil, psi.NodeRef{}, false
	}
	n := f.Snapshot.Node(id.Node)
	return f, n, n.Valid()
}

// Class returns the symbol of the class in the sources whose qualified
// name is name.
func (a *Analysis) Class(name string) (analysis.Symbol, bool) {
	a.classesOnce.Do(func() {
		a.classes = map[string]uint32{}
		for id := uint32(1); ; id++ {
			s, ok := a.Result.Symbol(id)
			if !ok {
				break
			}
			if s.Kind == "class" && s.QualifiedName != "" {
				if _, dup := a.classes[s.QualifiedName]; !dup {
					a.classes[s.QualifiedName] = id
				}
			}
		}
	})
	id, ok := a.classes[name]
	if !ok {
		return analysis.Symbol{}, false
	}
	return a.Result.Symbol(id)
}

// Diagnostics returns f's syntax errors and, if a analyzed f, the
// checker's errors and warnings in it.
func Diagnostics(a *Analysis, f *File) []protocol.Diagnostic {
	out := SyntaxDiagnostics(f)
	if a == nil {
		return out
	}
	af := a.File(f.Snapshot.Path())
	if af == nil {
		return out
	}
	for _, d := range a.Result.Diagnostics() {
		if d.Path != f.Snapshot.Path() {
			continue
		}
		start := af.offset(d.Line, d.Column)
		end := diagnosticEnd(af.Snapshot, start, strings.HasPrefix(d.Message, "unresolved reference"))
		sev := protocol.SeverityError
		switch {
		case d.Severity == "warning":
			sev = protocol.SeverityWarning
		case strings.HasPrefix(d.Code, "unsupported:"):
			// kc's gap, not the code's error: kotlinc compiles it.
			sev = protocol.SeverityInformation
		}
		pd := protocol.Diagnostic{Range: af.Mapper.Range(start, end), Severity: sev, Source: "kc", Message: d.Message}
		switch {
		case d.Name != "":
			pd.Code = d.Name
		case strings.HasPrefix(d.Code, "unsupported:"):
			pd.Code = d.Code
		}
		if d.Name == "DEPRECATION" || strings.HasPrefix(d.Name, "DEPRECATION_") {
			pd.Tags = append(pd.Tags, protocol.TagDeprecated)
		}
		if strings.HasPrefix(d.Name, "UNUSED_") || strings.HasPrefix(d.Name, "REDUNDANT_") {
			pd.Tags = append(pd.Tags, protocol.TagUnnecessary)
		}
		out = append(out, pd)
	}
	return append(out, unusedImportDiagnostics(f)...)
}

// offset returns the byte offset of a 1-based line and byte column,
// clamped to the line.
func (f *File) offset(line, col int) int {
	if line < 1 {
		return 0
	}
	start := f.Mapper.LineStart(line - 1)
	if start < 0 {
		return len(f.Mapper.Text())
	}
	off := start + max(col-1, 0)
	text := f.Mapper.Text()
	if nl := strings.IndexByte(text[start:], '\n'); nl >= 0 && off > start+nl {
		off = start + nl
	}
	return min(off, len(text))
}

// diagnosticEnd returns where a diagnostic that starts at start ends: at
// the end of the token there if token is set, else at the end of the
// widest expression or reference that starts there on the same line.
func diagnosticEnd(s *psi.Snapshot, start int, token bool) int {
	src := s.Source()
	lineEnd := len(src)
	if nl := strings.IndexByte(src[start:], '\n'); nl >= 0 {
		lineEnd = start + nl
	}
	leaf := s.NodeAt(start)
	if !leaf.Valid() || leaf.IsTrivia() || leaf.Range().Start != start {
		// Between tokens: one character, at least.
		if start < lineEnd {
			return start + 1
		}
		return start
	}
	end := leaf.Range().End
	if token {
		return min(end, lineEnd)
	}
	for n := leaf.Parent(); n.Valid() && n.Range().Start == start; n = n.Parent() {
		r := n.Range()
		if r.End > lineEnd {
			break
		}
		switch n.NumericKind() {
		case psi.KindFILE, psi.KindBLOCK:
			return min(end, lineEnd)
		}
		if isDeclaration(n) {
			break
		}
		end = r.End
	}
	return min(max(end, start), lineEnd)
}

func isDeclaration(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindFUN, psi.KindPROPERTY, psi.KindTYPEALIAS,
		psi.KindPRIMARY_CONSTRUCTOR, psi.KindSECONDARY_CONSTRUCTOR, psi.KindVALUE_PARAMETER:
		return true
	}
	return false
}
