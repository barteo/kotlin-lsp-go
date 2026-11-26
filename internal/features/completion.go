package features

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// Completer analyzes a file for a completion.
type Completer interface {
	// Complete analyzes f, which replaces the file at its path, with
	// c: the analysis's Result.Completion is what the code could name at
	// the placeholder.
	Complete(ctx context.Context, f *File, c *analysis.Complete) (*Analysis, error)
	// LibraryClasses returns the binary names of the top-level classes of
	// the libraries of the module of the file at path whose simple names
	// match.
	LibraryClasses(ctx context.Context, path string, match func(simple string) bool) []string
}

// CompletionOptions are what the client supports.
type CompletionOptions struct {
	// Snippets lets an item insert a snippet: name($0).
	Snippets bool
	// DeprecatedTag marks deprecated items with a tag, not the
	// deprecated property.
	DeprecatedTag bool
}

// completionLimit bounds the items a completion returns.
const completionLimit = 200

// indentUnit is what the inserted code indents a level with.
const indentUnit = "    "

// The places a completion can be at.
type place int

const (
	placeNone       place = iota
	placeExpression       // a statement or an expression: x
	placeMember           // after a receiver: a.x, a?.x
	placeReference        // a callable reference: ::x, a::x
	placeType             // a type: val v: X
	placeArgName          // a named argument's name: f(x = 1)
	placeClassBody        // a member's declaration: class C { x }
	placeTopLevel         // a top-level declaration: x
)

// site is where a completion is.
type site struct {
	f     *File
	place place
	// start and end are the identifier being typed, if any; off is the
	// caret.
	start, end, off int
	prefix          string
	// spliced is f with the placeholder in place of the identifier, and
	// ph the placeholder's leaf in it.
	spliced *File
	ph      psi.NodeRef
	// declStart is where a member's modifiers and keyword start, in a
	// class body: "override fun x".
	declStart int
	// callFollows is set when parentheses or a lambda follow the
	// identifier.
	callFollows bool
}

// Completion returns what the code could name at pos in f: the
// declarations in scope, the members of a receiver, the declarations an
// import would bring in scope, overrides in a class body, named
// arguments, keywords and live templates.
func Completion(ctx context.Context, c Completer, f *File, pos protocol.Position, opts CompletionOptions) (*protocol.CompletionList, error) {
	empty := &protocol.CompletionList{Items: []protocol.CompletionItem{}}
	off, err := f.Mapper.Offset(pos)
	if err != nil {
		return nil, err
	}
	s, ok := siteAt(f, off)
	if !ok {
		return empty, nil
	}
	cc := &completer{s: s, c: c, opts: opts, names: map[string]bool{}}
	switch s.place {
	case placeNone:
		return empty, nil
	case placeTopLevel:
		cc.keywords(topLevelKeywords)
		cc.templates(topLevelTemplates)
	case placeClassBody:
		if err := cc.overrides(ctx); err != nil {
			return nil, err
		}
		typed := f.Mapper.Text()[s.declStart:s.start]
		if !hasWord(typed, "fun") && !hasWord(typed, "val") && !hasWord(typed, "var") {
			cc.keywords(classBodyKeywords)
		}
	default:
		if err := cc.scope(ctx); err != nil {
			return nil, err
		}
		if s.place == placeExpression {
			cc.keywords(expressionKeywords)
			cc.templates(statementTemplates)
		}
	}
	return cc.list(), nil
}

// siteAt returns where a completion at off in f is, or false in a
// comment, a string or a number.
func siteAt(f *File, off int) (site, bool) {
	text := f.Mapper.Text()
	if off < 0 || off > len(text) {
		return site{}, false
	}
	if off > 0 && inCommentOrString(f.Snapshot.NodeAt(off-1)) {
		return site{}, false
	}
	start, end := off, off
	for start > 0 {
		r, n := utf8.DecodeLastRuneInString(text[:start])
		if !identRune(r) {
			break
		}
		start -= n
	}
	for end < len(text) {
		r, n := utf8.DecodeRuneInString(text[end:])
		if !identRune(r) {
			break
		}
		end += n
	}
	if start < end && unicode.IsDigit(rune(text[start])) {
		return site{}, false
	}
	s := site{f: f, start: start, end: end, off: off, prefix: text[start:off], declStart: start}
	rest := strings.TrimLeft(text[end:], " \t")
	s.callFollows = strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "{")
	s.spliced = NewFile(f.URI, f.Snapshot.Path(), text[:start]+analysis.Placeholder+closer(f.Snapshot, off)+text[end:], f.Mapper.Encoding())
	s.ph = s.spliced.Snapshot.NodeAt(start)
	if !s.ph.Valid() || s.ph.Text() != analysis.Placeholder {
		return site{}, false
	}
	s.place = placeOf(s.ph)
	if s.place == placeClassBody {
		s.declStart = modifiersStart(text, start)
	}
	return s, true
}

// placeOf classifies the placeholder's leaf.
func placeOf(ph psi.NodeRef) place {
	for a := ph.Parent(); a.Valid(); a = a.Parent() {
		switch a.NumericKind() {
		case psi.KindIMPORT_DIRECTIVE, psi.KindPACKAGE_DIRECTIVE:
			return placeNone
		}
	}
	p := ph.Parent()
	switch p.NumericKind() {
	case psi.KindERROR_ELEMENT:
		switch p.Parent().NumericKind() {
		case psi.KindCLASS_BODY:
			return placeClassBody
		case psi.KindFILE:
			return placeTopLevel
		}
		return placeNone
	case psi.KindREFERENCE_EXPRESSION:
		gp := p.Parent()
		switch gp.NumericKind() {
		case psi.KindUSER_TYPE:
			if gp.FirstChild() != p {
				return placeNone // a nested class: Map.X
			}
			return placeType
		case psi.KindDOT_QUALIFIED_EXPRESSION, psi.KindSAFE_ACCESS_EXPRESSION:
			if gp.FirstChild() != p {
				return placeMember
			}
		case psi.KindCALLABLE_REFERENCE_EXPRESSION:
			return placeReference
		case psi.KindVALUE_ARGUMENT_NAME:
			return placeArgName
		}
		return placeExpression
	case psi.KindFUN, psi.KindPROPERTY:
		if p.NameIdentifier() == ph && p.Parent().NumericKind() == psi.KindCLASS_BODY && hasModifier(p, psi.KindOVERRIDE_KEYWORD) {
			return placeClassBody
		}
	}
	return placeNone
}

func inCommentOrString(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindEOL_COMMENT, psi.KindBLOCK_COMMENT, psi.KindSHEBANG_COMMENT, psi.KindREGULAR_STRING_PART,
		psi.KindESCAPE_SEQUENCE, psi.KindOPEN_QUOTE, psi.KindCHARACTER_LITERAL:
		return true
	}
	for ; n.Valid(); n = n.Parent() {
		if n.NumericKind() == psi.KindKDOC {
			return true
		}
	}
	return false
}

func identRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// memberWords are the words a member's declaration can start with, before
// its name.
var memberWords = map[string]bool{
	"override": true, "fun": true, "val": true, "var": true, "public": true, "protected": true, "internal": true,
	"open": true, "final": true, "suspend": true, "operator": true, "infix": true, "inline": true, "tailrec": true,
	"lateinit": true, "abstract": true, "external": true,
}

// modifiersStart returns where the modifiers and keyword of the member
// declaration whose name starts at start start, on its line.
func modifiersStart(text string, start int) int {
	i := start
	for {
		j := i
		for j > 0 && (text[j-1] == ' ' || text[j-1] == '\t') {
			j--
		}
		k := j
		for k > 0 && identRune(rune(text[k-1])) {
			k--
		}
		if k == j || !memberWords[text[k:j]] {
			return i
		}
		i = k
	}
}

func hasWord(s, w string) bool { return slices.Contains(strings.Fields(s), w) }

// lineIndent returns the indentation of the line off is on.
func lineIndent(text string, off int) string {
	i := strings.LastIndexByte(text[:off], '\n') + 1
	j := i
	for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
		j++
	}
	return text[i:j]
}

// completer gathers a completion's items.
type completer struct {
	s     site
	c     Completer
	opts  CompletionOptions
	a     *Analysis
	items []scored
	// names are the simple names of the items in scope.
	names map[string]bool
}

type scored struct {
	item  protocol.CompletionItem
	group int
	score int
	// rank orders the items of a group that score the same: a member
	// before an extension, a local before a top-level declaration.
	rank int
}

// The groups items sort in.
const (
	groupArgName = iota
	groupExpected
	groupLocal
	groupMember
	groupScope
	groupKeyword
	groupImport
)

// match reports whether a name is worth listing for the prefix typed: its
// first letter must be the prefix's, ignoring case, and the rest must
// match as MatchScore has them.
func (cc *completer) match(name string) (int, bool) {
	p := cc.s.prefix
	if p == "" {
		return 0, true
	}
	r1, _ := utf8.DecodeRuneInString(p)
	r2, _ := utf8.DecodeRuneInString(name)
	if unicode.ToLower(r1) != unicode.ToLower(r2) {
		return 0, false
	}
	return MatchScore(p, name)
}

func (cc *completer) add(it protocol.CompletionItem, group int, name string) {
	cc.addRanked(it, group, 0, name)
}

func (cc *completer) addRanked(it protocol.CompletionItem, group, rank int, name string) {
	score, ok := cc.match(name)
	if !ok {
		return
	}
	if it.TextEdit == nil {
		text := it.Label
		if it.InsertTextFormat == 0 {
			it.InsertTextFormat = protocol.PlainTextFormat
		}
		it.TextEdit = &protocol.TextEdit{Range: cc.s.f.Mapper.Range(cc.s.start, cc.s.end), NewText: text}
	}
	cc.items = append(cc.items, scored{it, group, score, rank})
}

// list returns the best items, sorted.
func (cc *completer) list() *protocol.CompletionList {
	// A strong match (exact, a prefix, camel humps) comes before a weak
	// one (a substring, letters anywhere), whatever its group.
	weak := func(s scored) bool { return cc.s.prefix != "" && s.score < 500 }
	slices.SortStableFunc(cc.items, func(a, b scored) int {
		if wa, wb := weak(a), weak(b); wa != wb {
			if wa {
				return 1
			}
			return -1
		}
		if a.group != b.group {
			return a.group - b.group
		}
		if a.score != b.score {
			return b.score - a.score
		}
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return strings.Compare(a.item.Label, b.item.Label)
	})
	l := &protocol.CompletionList{Items: []protocol.CompletionItem{}, IsIncomplete: cc.s.prefix == ""}
	if len(cc.items) > completionLimit {
		cc.items = cc.items[:completionLimit]
		l.IsIncomplete = true
	}
	for i, it := range cc.items {
		it.item.SortText = fmt.Sprintf("%04d", i)
		l.Items = append(l.Items, it.item)
	}
	return l
}

// The keywords each place offers.
var (
	expressionKeywords = []string{"as", "break", "continue", "do", "else", "false", "for", "fun", "if", "in", "is", "null",
		"object", "return", "super", "this", "throw", "true", "try", "val", "var", "when", "while"}
	classBodyKeywords = []string{"abstract", "annotation", "class", "companion", "const", "constructor", "data", "enum",
		"fun", "infix", "init", "inline", "inner", "interface", "internal", "lateinit", "object", "open", "operator",
		"override", "private", "protected", "public", "sealed", "suspend", "tailrec", "val", "var", "value"}
	topLevelKeywords = []string{"abstract", "annotation", "class", "const", "data", "enum", "fun", "import", "infix",
		"inline", "interface", "internal", "object", "open", "operator", "package", "private", "public", "sealed",
		"suspend", "tailrec", "typealias", "val", "var", "value"}
)

func (cc *completer) keywords(kws []string) {
	for _, kw := range kws {
		cc.add(protocol.CompletionItem{Label: kw, Kind: protocol.CompletionKeyword}, groupKeyword, kw)
	}
}

// template is a live template: a snippet a short name expands to.
type template struct {
	name, detail, body string
}

// The live templates, as IntelliJ's Kotlin ones; a body indents with
// tabs.
var (
	topLevelTemplates = []template{
		{"main", "main() function", "fun main() {\n\t$0\n}"},
		{"maina", "main(args) function", "fun main(args: Array<String>) {\n\t$0\n}"},
		{"fun0", "function without parameters", "fun ${1:name}() {\n\t$0\n}"},
		{"fun1", "function with one parameter", "fun ${1:name}(${2:x}: ${3:Any}) {\n\t$0\n}"},
		{"fun2", "function with two parameters", "fun ${1:name}(${2:x}: ${3:Any}, ${4:y}: ${5:Any}) {\n\t$0\n}"},
		{"exfun", "extension function", "fun ${1:Any}.${2:name}(${3}) {\n\t$0\n}"},
		{"exval", "extension read-only property", "val ${1:Any}.${2:name}: ${3:Any}\n\tget() = $0"},
		{"exvar", "extension variable property", "var ${1:Any}.${2:name}: ${3:Any}\n\tget() = $4\n\tset(value) {\n\t\t$0\n\t}"},
		{"singleton", "singleton object", "object ${1:Name} {\n\t$0\n}"},
		{"interface", "interface", "interface ${1:Name} {\n\t$0\n}"},
	}
	statementTemplates = []template{
		{"ifn", "if null", "if (${1:x} == null) {\n\t$0\n}"},
		{"inn", "if not null", "if (${1:x} != null) {\n\t$0\n}"},
		{"iter", "for loop over an iterable", "for (${1:item} in ${2:items}) {\n\t$0\n}"},
		{"sout", "print to standard output", "println($0)"},
		{"serr", "print to standard error", "System.err.println($0)"},
		{"soutv", "print a value", "println(\"${1:x} = \\${${1:x}\\}\")"},
		{"todo", "TODO()", "TODO(\"${1:Not yet implemented}\")"},
		{"closure", "lambda", "{ ${1:x} -> $0 }"},
	}
)

func (cc *completer) templates(ts []template) {
	if !cc.opts.Snippets {
		return
	}
	indent := lineIndent(cc.s.f.Mapper.Text(), cc.s.start)
	for _, t := range ts {
		body := strings.ReplaceAll(t.body, "\t", indentUnit)
		body = strings.ReplaceAll(body, "\n", "\n"+indent)
		cc.add(protocol.CompletionItem{
			Label: t.name, Kind: protocol.CompletionSnippet, Detail: t.detail,
			InsertTextFormat: protocol.SnippetFormat, InsertTextMode: protocol.InsertAsIs,
			TextEdit: &protocol.TextEdit{Range: cc.s.f.Mapper.Range(cc.s.start, cc.s.end), NewText: body},
		}, groupKeyword, t.name)
	}
}

// scope adds what the code could name at a statement, an expression, a
// receiver's member, a callable reference, a type or a named argument.
func (cc *completer) scope(ctx context.Context) error {
	s := cc.s
	req := &analysis.Complete{Path: s.f.Snapshot.Path(), Importable: s.prefix != "" && s.place != placeType}
	req.Match = func(name string) bool {
		_, ok := cc.match(name)
		return ok
	}
	if s.place == placeArgName {
		req.Match = func(string) bool { return false }
	}
	a, err := cc.c.Complete(ctx, s.spliced, req)
	if err != nil {
		return err
	}
	if a == nil {
		return nil
	}
	cc.a = a
	if s.place == placeExpression || s.place == placeArgName {
		cc.argNames()
	}
	if s.place == placeArgName {
		return nil
	}
	comp := a.Result.Completion()
	if comp == nil || s.place == placeMember && !comp.Found {
		return nil
	}
	annotation := false
	for n := s.ph; n.Valid() && s.place == placeType; n = n.Parent() {
		if n.NumericKind() == psi.KindANNOTATION_ENTRY {
			annotation = true
		}
	}
	for _, ci := range comp.Items {
		if ci.Import == "" {
			cc.names[ci.Name] = true
		}
		if s.place == placeType && ci.Kind != "class" || annotation && ci.ClassKind != "annotation" {
			continue
		}
		cc.addItem(ci, comp.Expected)
	}
	if s.prefix != "" && (s.place == placeExpression || s.place == placeType) {
		cc.importableClasses(ctx)
	}
	return nil
}

// addItem adds an item of the analysis's completion.
func (cc *completer) addItem(ci analysis.CompletionItem, expected *analysis.Type) {
	a, s := cc.a, cc.s
	sym, _ := a.Result.Symbol(ci.Symbol)
	it := protocol.CompletionItem{Label: ci.Name}
	group := groupScope
	switch {
	case ci.Import != "":
		group = groupImport
	case ci.Level == 0:
		group = groupLocal
	case ci.Level == 1:
		group = groupMember
	}
	if expected != nil && ci.Type != nil && ci.Type.Name == expected.Name && ci.Kind != "class" && group != groupImport {
		group = groupExpected
	}
	pkg := ""
	if ci.Import != "" {
		pkg = ci.Import[:max(strings.LastIndexByte(ci.Import, '.'), 0)]
	} else if sym.Owner == "" && ci.Kind != "local" && ci.Kind != "parameter" {
		pkg = container(sym)
	}
	insert := quoteName(ci.Name)
	format := protocol.PlainTextFormat
	call := func(ps []analysis.Type) {
		if s.callFollows || s.place == placeReference || s.place == placeType {
			return
		}
		switch {
		case !cc.opts.Snippets:
			if len(ps) == 0 {
				insert += "()"
			}
		case len(ps) == 0:
			insert += "()"
		case len(ps) == 1 && functionType(ps[0]):
			insert += " { $0 }"
			format = protocol.SnippetFormat
		default:
			insert += "($0)"
			format = protocol.SnippetFormat
		}
	}
	switch ci.Kind {
	case "function":
		it.Kind = protocol.CompletionFunction
		if sym.Owner != "" && !ci.Extension {
			it.Kind = protocol.CompletionMethod
		}
		if ci.Call != nil {
			ret := ""
			if ci.Call.DeclaredReturnType != nil {
				ret = RenderType(*ci.Call.DeclaredReturnType, false)
			}
			it.LabelDetails = &protocol.CompletionItemLabelDetails{Detail: params(ci.Call), Description: ret}
			it.Detail = callSignature(ci.Call)
			call(ci.Call.Parameters)
		}
	case "property", "local", "parameter":
		it.Kind = protocol.CompletionVariable
		if ci.Kind == "property" {
			it.Kind = protocol.CompletionProperty
		}
		kw := "val "
		if ci.Mutable {
			kw = "var "
		}
		if ci.Type != nil {
			t := RenderType(*ci.Type, false)
			it.LabelDetails = &protocol.CompletionItemLabelDetails{Description: t}
			it.Detail = kw + ci.Name + ": " + t
		}
	case "class":
		it.Kind = classCompletionKind(ci.ClassKind)
		it.Detail = ci.ClassKind + " " + sym.QualifiedName
		if s.place == placeExpression && len(ci.Constructors) > 0 {
			ps := ci.Constructors[0].Parameters
			for _, c := range ci.Constructors {
				if len(c.Parameters) > len(ps) {
					ps = c.Parameters
				}
			}
			if len(ps) > 0 {
				ps = ps[:1]
			}
			call(ps)
			if len(ci.Constructors) == 1 {
				it.LabelDetails = &protocol.CompletionItemLabelDetails{Detail: params(&ci.Constructors[0])}
			}
		}
	}
	if pkg != "" {
		if it.LabelDetails == nil {
			it.LabelDetails = &protocol.CompletionItemLabelDetails{}
		}
		if ci.Kind == "class" || ci.Import != "" || ci.Extension {
			it.LabelDetails.Description = pkg
		}
	}
	if ci.Deprecated {
		cc.deprecate(&it)
	}
	if ci.Import != "" {
		if e, ok := importEdit(s.f, ci.Import); ok {
			it.AdditionalTextEdits = []protocol.TextEdit{e}
		}
	}
	it.InsertTextFormat = format
	it.TextEdit = &protocol.TextEdit{Range: s.f.Mapper.Range(s.start, s.end), NewText: insert}
	cc.document(&it, sym, ci.Call)
	rank := 2 * ci.Level
	if ci.Extension {
		rank++
	}
	cc.addRanked(it, group, rank, ci.Name)
}

func (cc *completer) deprecate(it *protocol.CompletionItem) {
	if cc.opts.DeprecatedTag {
		it.Tags = []int{1}
	} else {
		it.Deprecated = true
	}
}

// document sets the documentation of a source declaration's item, and
// the data with which completionItem/resolve finds a library one's.
func (cc *completer) document(it *protocol.CompletionItem, sym analysis.Symbol, call *analysis.Call) {
	if sym.Declaration != nil {
		if _, dn, ok := cc.a.Node(*sym.Declaration); ok {
			if doc := kdoc(dn); doc != "" {
				it.Documentation = &protocol.MarkupContent{Kind: "markdown", Value: doc}
			}
		}
		return
	}
	var d LibraryDecl
	switch {
	case sym.Kind == "class":
		d = LibraryDecl{Kind: "class", Class: sym.Name, QualifiedName: sym.QualifiedName, Parameters: -1}
	case sym.QualifiedName != "" && (sym.Kind == "function" || sym.Kind == "property"):
		d = LibraryDecl{Kind: sym.Kind, QualifiedName: sym.QualifiedName, Owner: sym.Owner, Parameters: -1}
		if call != nil {
			d.Parameters = len(call.Parameters)
		}
	default:
		return
	}
	it.Data = completionData(cc.s.f.URI, d)
}

// CompletionData is what completionItem/resolve needs of an item: the
// document completed in, and the library declaration to document.
type CompletionData struct {
	URI  protocol.DocumentURI `json:"uri"`
	Decl LibraryDecl          `json:"decl"`
}

func completionData(uri protocol.DocumentURI, d LibraryDecl) json.RawMessage {
	b, _ := json.Marshal(CompletionData{URI: uri, Decl: d})
	return b
}

// ResolveCompletion documents an item from its library declaration's
// sources, found with a, the analysis of the document's module.
func ResolveCompletion(ctx context.Context, a *Analysis, it protocol.CompletionItem) protocol.CompletionItem {
	var d CompletionData
	if a == nil || a.Libraries == nil || len(it.Data) == 0 || json.Unmarshal(it.Data, &d) != nil {
		return it
	}
	lt, ok := a.Libraries.Find(ctx, d.Decl)
	if !ok {
		return it
	}
	doc := ""
	switch {
	case lt.Node.Valid():
		doc = kdoc(lt.Node)
		if sig := declSignature(lt.Node, analysis.Symbol{}); sig != "" && d.Decl.Kind == "class" {
			it.Detail = sig
		}
	case lt.Doc != "":
		doc = javadoc(lt.Doc)
	}
	if doc != "" {
		it.Documentation = &protocol.MarkupContent{Kind: "markdown", Value: doc}
	}
	return it
}

func classCompletionKind(kind string) protocol.CompletionItemKind {
	switch kind {
	case "interface", "annotation":
		return protocol.CompletionInterface
	case "enum":
		return protocol.CompletionEnum
	case "object":
		return protocol.CompletionModule
	}
	return protocol.CompletionClass
}

// functionType reports whether t is a function type, which a lambda can
// be passed as.
func functionType(t analysis.Type) bool {
	return strings.HasPrefix(t.Name, "kotlin.Function") || strings.HasPrefix(t.Name, "kotlin.coroutines.SuspendFunction") || t.SAM
}

// quoteName quotes a name Kotlin can't write as an identifier: a hard
// keyword or one with other characters.
func quoteName(name string) string {
	if hardKeywords[name] || !isWord(name) {
		return "`" + name + "`"
	}
	return name
}

// importEdit returns the edit that imports fq into f, in order among its
// imports, or false if f imports it already.
func importEdit(f *File, fq string) (protocol.TextEdit, bool) {
	root := f.Snapshot.RootNode()
	pkg := fq[:max(strings.LastIndexByte(fq, '.'), 0)]
	var first, last psi.NodeRef // the first import after fq, the last one
	for c := root.ChildKind(psi.KindIMPORT_LIST).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() != psi.KindIMPORT_DIRECTIVE {
			continue
		}
		p := c.Path()
		if c.Alias() == "" && (p == fq && !c.IsAllUnder() || p == pkg && c.IsAllUnder()) {
			return protocol.TextEdit{}, false
		}
		if p > fq && !first.Valid() {
			first = c
		}
		last = c
	}
	insert := func(at int, text string) (protocol.TextEdit, bool) {
		return protocol.TextEdit{Range: f.Mapper.Range(at, at), NewText: text}, true
	}
	switch pd := root.ChildKind(psi.KindPACKAGE_DIRECTIVE); {
	case first.Valid():
		return insert(first.Range().Start, "import "+fq+"\n")
	case last.Valid():
		return insert(last.Range().End, "\nimport "+fq)
	case pd.Valid() && pd.Range().End > pd.Range().Start:
		return insert(pd.Range().End, "\n\nimport "+fq)
	}
	return insert(0, "import "+fq+"\n\n")
}

// argNames adds the names of the parameters of the call whose argument
// the placeholder is that no argument names yet: x = .
func (cc *completer) argNames() {
	ref := cc.s.ph.Parent()
	arg := ref.Parent()
	if arg.NumericKind() == psi.KindVALUE_ARGUMENT_NAME {
		arg = arg.Parent()
	} else if arg.NumericKind() != psi.KindVALUE_ARGUMENT || arg.FirstChild() != ref {
		return
	}
	list := arg.Parent()
	call := list.Parent()
	if list.NumericKind() != psi.KindVALUE_ARGUMENT_LIST {
		return
	}
	sp := cc.s.spliced
	fa, ok := cc.a.Result.Fact(analysis.ElementID{Snapshot: sp.Snapshot.ID(), Node: call.ID()})
	if !ok || fa.Call == nil {
		return
	}
	named := map[string]bool{}
	for c := list.FirstChild(); c.Valid(); c = c.NextSibling() {
		if n := c.ChildKind(psi.KindVALUE_ARGUMENT_NAME); n.Valid() && c != arg {
			named[n.ChildKind(psi.KindREFERENCE_EXPRESSION).Name()] = true
		}
	}
	nameOnly := cc.s.place == placeArgName
	for i, name := range fa.Call.ParameterNames {
		if name == "" || named[name] || i >= len(fa.Call.Parameters) {
			continue
		}
		text := quoteName(name)
		if !nameOnly {
			text += " = "
		}
		cc.add(protocol.CompletionItem{
			Label: name + " =", Kind: protocol.CompletionVariable, FilterText: name,
			Detail:   name + ": " + RenderType(fa.Call.Parameters[i], false),
			TextEdit: &protocol.TextEdit{Range: cc.s.f.Mapper.Range(cc.s.start, cc.s.end), NewText: text},
		}, groupArgName, name)
	}
}

// importableClasses adds the classes not in scope that an import would
// bring in: the sources' and the libraries'.
func (cc *completer) importableClasses(ctx context.Context) {
	s := cc.s
	own := s.f.Snapshot.RootNode().ChildKind(psi.KindPACKAGE_DIRECTIVE).Path()
	seen := map[string]bool{}
	add := func(fq, kind string, decl psi.NodeRef) {
		if seen[fq] {
			return
		}
		seen[fq] = true
		i := strings.LastIndexByte(fq, '.')
		if i < 0 || fq[:i] == own || defaultImports[fq[:i]] || cc.names[fq[i+1:]] {
			return
		}
		name := fq[i+1:]
		it := protocol.CompletionItem{Label: name, Kind: classCompletionKind(kind), Detail: fq,
			LabelDetails: &protocol.CompletionItemLabelDetails{Description: fq[:i]}}
		if e, ok := importEdit(s.f, fq); ok {
			it.AdditionalTextEdits = []protocol.TextEdit{e}
		}
		if doc := kdoc(decl); decl.Valid() && doc != "" {
			it.Documentation = &protocol.MarkupContent{Kind: "markdown", Value: doc}
		} else if !decl.Valid() {
			bin := strings.ReplaceAll(fq, ".", "/") // a top-level class's
			it.Data = completionData(s.f.URI, LibraryDecl{Kind: "class", Class: bin, QualifiedName: fq, Parameters: -1})
		}
		cc.add(it, groupImport, name)
	}
	for _, f := range cc.a.Files() {
		if f.Snapshot.Path() == s.f.Snapshot.Path() {
			continue
		}
		root := f.Snapshot.RootNode()
		pkg := root.ChildKind(psi.KindPACKAGE_DIRECTIVE).Path()
		for c := root.FirstChild(); c.Valid(); c = c.NextSibling() {
			switch c.NumericKind() {
			case psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindTYPEALIAS:
			default:
				continue
			}
			name := c.Name()
			if _, ok := cc.match(name); !ok || hasModifier(c, psi.KindPRIVATE_KEYWORD) {
				continue
			}
			kind := "class"
			switch {
			case c.NumericKind() == psi.KindOBJECT_DECLARATION:
				kind = "object"
			case c.ChildKind(psi.KindINTERFACE_KEYWORD).Valid():
				kind = "interface"
			case hasModifier(c, psi.KindENUM_KEYWORD):
				kind = "enum"
			}
			add(qualify(pkg, name), kind, c)
		}
	}
	for _, bin := range cc.c.LibraryClasses(ctx, s.f.Snapshot.Path(), func(name string) bool {
		_, ok := cc.match(name)
		return ok
	}) {
		fq := strings.ReplaceAll(bin, "/", ".")
		if internalPackage(fq) {
			continue
		}
		add(fq, "class", psi.NodeRef{})
	}
}

func qualify(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// defaultImports are the packages every Kotlin file on the JVM imports.
var defaultImports = map[string]bool{
	"kotlin": true, "kotlin.annotation": true, "kotlin.collections": true, "kotlin.comparisons": true, "kotlin.io": true,
	"kotlin.ranges": true, "kotlin.sequences": true, "kotlin.text": true, "kotlin.jvm": true, "java.lang": true,
}

// internalPackage reports whether a class is in a package not meant to
// be imported.
func internalPackage(fq string) bool {
	for _, p := range []string{"sun.", "com.sun.", "jdk.", "kotlin.jvm.internal.", "kotlin.internal."} {
		if strings.HasPrefix(fq, p) {
			return true
		}
	}
	return strings.Contains(fq, ".internal.") || strings.Contains(fq, ".impl.")
}

// overridePrefix is what overrides splices in a class body to ask for
// the members of the class.
const overridePrefix = "fun kc__members() { this."

// overrides adds the members of a class's supertypes that a declaration
// in its body could override.
func (cc *completer) overrides(ctx context.Context) error {
	s := cc.s
	text := s.f.Mapper.Text()
	typed := text[s.declStart:s.start]
	a, items, err := superMembers(ctx, cc.c, s.f, s.declStart, s.end, "", "", func(name string) bool {
		_, ok := cc.match(name)
		return ok
	})
	if err != nil || a == nil {
		return err
	}
	cc.a = a
	indent := lineIndent(text, s.declStart)
	wantFun, wantProp := !hasWord(typed, "val") && !hasWord(typed, "var"), !hasWord(typed, "fun")
	for _, ci := range items {
		if ci.Kind == "function" && !wantFun || ci.Kind == "property" && !wantProp {
			continue
		}
		sym, _ := a.Result.Symbol(ci.Symbol)
		label, code := overrideCode(ci, indent)
		it := protocol.CompletionItem{
			Label: label, Kind: protocol.CompletionMethod, FilterText: typed + ci.Name,
			InsertTextFormat: protocol.PlainTextFormat, InsertTextMode: protocol.InsertAsIs,
			TextEdit: &protocol.TextEdit{Range: s.f.Mapper.Range(s.declStart, s.end), NewText: code},
		}
		if ci.Kind == "property" {
			it.Kind = protocol.CompletionProperty
		}
		if owner := sym.Owner; owner != "" {
			it.LabelDetails = &protocol.CompletionItemLabelDetails{Description: owner[strings.LastIndexAny(owner, "/$")+1:]}
		}
		cc.document(&it, sym, ci.Call)
		score, ok := cc.match(ci.Name)
		if !ok {
			continue
		}
		cc.items = append(cc.items, scored{it, groupMember, score, 0})
	}
	return nil
}

// superMembers returns the members of a class's supertypes that its body
// could override, open or abstract, whose names match: what the code
// could name after this in a function that replaces text[at:end] of f,
// written there between before and after, in the class's body.
func superMembers(ctx context.Context, c Completer, f *File, at, end int, before, after string, match func(string) bool) (*Analysis, []analysis.CompletionItem, error) {
	text := f.Mapper.Text()
	spliced := NewFile(f.URI, f.Snapshot.Path(), text[:at]+before+overridePrefix+analysis.Placeholder+" }"+after+text[end:], f.Mapper.Encoding())
	body := spliced.Snapshot.NodeAt(at + len(before) + len(overridePrefix))
	for body.Valid() && body.NumericKind() != psi.KindCLASS_BODY {
		body = body.Parent()
	}
	if !body.Valid() {
		return nil, nil, nil
	}
	clsRange := body.Parent().Range()
	a, err := c.Complete(ctx, spliced, &analysis.Complete{Path: f.Snapshot.Path(), Match: match})
	if err != nil || a == nil {
		return nil, nil, err
	}
	comp := a.Result.Completion()
	if comp == nil || !comp.Found {
		return a, nil, nil
	}
	var out []analysis.CompletionItem
	for _, ci := range comp.Items {
		if ci.Level != 1 || ci.Extension || ci.Modality == "final" || ci.Modality == "" ||
			ci.Kind == "function" && ci.Call == nil || ci.Kind != "function" && ci.Kind != "property" {
			continue
		}
		if sym, _ := a.Result.Symbol(ci.Symbol); sym.Declaration != nil {
			if df, dn, ok := a.Node(*sym.Declaration); ok && df.Snapshot.ID() == spliced.Snapshot.ID() &&
				dn.Range().Start >= clsRange.Start && dn.Range().End <= clsRange.End {
				continue // the class's own
			}
		}
		out = append(out, ci)
	}
	return a, out, nil
}

// overrideCode returns the label and code of the override of a member,
// whose body is TODO() for an abstract member or a call of super's.
func overrideCode(ci analysis.CompletionItem, indent string) (string, string) {
	in := indent + indentUnit
	if ci.Kind == "property" {
		kw := "val"
		if ci.Mutable {
			kw = "var"
		}
		typ := ""
		if ci.Type != nil {
			typ = ": " + codeType(*ci.Type)
		}
		head := "override " + kw + " " + quoteName(ci.Name) + typ
		get, set := "TODO(\"Not yet implemented\")", "TODO(\"Not yet implemented\")"
		if ci.Modality != "abstract" {
			get, set = "super."+quoteName(ci.Name), "super."+quoteName(ci.Name)+" = value"
		}
		code := head + "\n" + in + "get() = " + get
		if ci.Mutable {
			code += "\n" + in + "set(value) {\n" + in + indentUnit + set + "\n" + in + "}"
		}
		return head, code
	}
	c := ci.Call
	var ps, args []string
	for i, t := range c.Parameters {
		name := fmt.Sprintf("p%d", i)
		if i < len(c.ParameterNames) && c.ParameterNames[i] != "" {
			name = quoteName(c.ParameterNames[i])
		}
		p, arg := name+": "+codeType(t), name
		if i < len(c.Varargs) && c.Varargs[i] {
			p, arg = "vararg "+p, "*"+name
		}
		ps, args = append(ps, p), append(args, arg)
	}
	head := "override fun "
	if c.Suspend {
		head = "override suspend fun "
	}
	if len(c.TypeParameterNames) > 0 {
		head += "<" + strings.Join(c.TypeParameterNames, ", ") + "> "
	}
	head += quoteName(ci.Name) + "(" + strings.Join(ps, ", ") + ")"
	unit := ci.Type == nil || ci.Type.Name == "kotlin.Unit"
	if !unit {
		head += ": " + codeType(*ci.Type)
	}
	body := "TODO(\"Not yet implemented\")"
	if ci.Modality != "abstract" {
		body = "super." + quoteName(ci.Name) + "(" + strings.Join(args, ", ") + ")"
		if !unit {
			body = "return " + body
		}
	}
	return head, head + " {\n" + in + body + "\n" + indent + "}"
}

// codeType renders a type as code declares it: a platform type as
// nullable is, flexible types not being writable.
func codeType(t analysis.Type) string {
	return RenderType(declarable(t), false)
}

func declarable(t analysis.Type) analysis.Type {
	if t.Platform {
		t.Platform = false
	}
	if len(t.Arguments) > 0 {
		args := make([]analysis.Type, len(t.Arguments))
		for i, a := range t.Arguments {
			args[i] = declarable(a)
		}
		t.Arguments = args
	}
	return t
}
