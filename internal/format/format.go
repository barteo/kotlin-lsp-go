// Package format formats Kotlin source as IntelliJ does with the Kotlin
// coding conventions, on psi's lossless tree.
//
// It changes whitespace only, and keeps the line breaks the code has,
// as IntelliJ keeps them by default: it indents each line, spaces the
// tokens of a line, limits blank lines, and ends the file with a line
// break. It never joins nor wraps lines.
package format

import (
	"sort"
	"strings"

	"github.com/barteo/kotlin-compiler-go/psi"
)

// Options configure the formatter, as .editorconfig does.
type Options struct {
	// IndentSize is the columns of one indentation level.
	IndentSize int
	// UseTabs indents with tabs of TabWidth columns, and spaces for the
	// rest.
	UseTabs  bool
	TabWidth int
	// MaxBlankLines is the most blank lines kept in a row, and
	// BlankLinesBeforeRBrace the most before a closing brace.
	MaxBlankLines          int
	BlankLinesBeforeRBrace int
	// InsertFinalNewline ends the file with one line break.
	InsertFinalNewline bool
}

// Default returns the options of the Kotlin coding conventions.
func Default() Options {
	return Options{IndentSize: 4, TabWidth: 4, MaxBlankLines: 2, BlankLinesBeforeRBrace: 0, InsertFinalNewline: true}
}

// Edit replaces the source's bytes [Start, End) with Text.
type Edit struct {
	Start, End int
	Text       string
}

// Format returns the edits that format s, in order. A file with syntax
// errors has its lines indented, and the spacing within them kept.
func Format(s *psi.Snapshot, o Options) []Edit {
	return newFormatter(s, o).format()
}

func newFormatter(s *psi.Snapshot, o Options) *formatter {
	if o.IndentSize <= 0 {
		o.IndentSize = 4
	}
	if o.TabWidth <= 0 {
		o.TabWidth = o.IndentSize
	}
	f := &formatter{s: s, src: s.Source(), o: o, pairs: map[psi.NodeID][]pair{}, index: map[psi.NodeID]int{}}
	f.broken = len(s.Diagnostics()) > 0
	f.nl = "\n"
	if strings.Contains(f.src, "\r\n") {
		f.nl = "\r\n"
	}
	f.tokens()
	f.lines()
	return f
}

type formatter struct {
	s      *psi.Snapshot
	src    string
	o      Options
	nl     string
	broken bool

	toks       []tok
	index      map[psi.NodeID]int // a token's node → its index
	lineStarts []int
	pairs      map[psi.NodeID][]pair
	levels     []int // the indentation levels of the line each token is on
}

type tok struct {
	n          psi.NodeRef
	start, end int
}

// pair is a bracket pair among a node's children: its closer is invalid
// if the code lacks it.
type pair struct{ open, close psi.NodeRef }

// tokens lists the tokens: the leaves but whitespace, with strings and
// KDoc whole.
func (f *formatter) tokens() {
	psi.WalkNodes(f.s.RootNode(), func(n psi.NodeRef) bool {
		switch n.NumericKind() {
		case psi.KindWHITE_SPACE:
			return false
		case psi.KindBAD_CHARACTER:
			if n.Text() == "\r" {
				return false // of a \r\n line break, which the lexer doesn't take for whitespace
			}
		case psi.KindSTRING_TEMPLATE, psi.KindKDOC:
		default:
			if !n.IsLeaf() {
				return true
			}
		}
		r := n.Range()
		if r.End > r.Start {
			f.index[n.ID()] = len(f.toks)
			f.toks = append(f.toks, tok{n: n, start: r.Start, end: r.End})
		}
		return false
	})
}

func (f *formatter) lines() {
	f.lineStarts = []int{0}
	for i := 0; i < len(f.src); i++ {
		if f.src[i] == '\n' {
			f.lineStarts = append(f.lineStarts, i+1)
		}
	}
}

// line returns the 0-based line of an offset.
func (f *formatter) line(off int) int {
	return sort.SearchInts(f.lineStarts, off+1) - 1
}

func (f *formatter) format() []Edit {
	var edits []Edit
	f.levels = make([]int, len(f.toks))
	cur := 0
	for i, t := range f.toks {
		gs := 0
		if i > 0 {
			gs = f.toks[i-1].end
		}
		orig := f.src[gs:t.start]
		if strings.TrimSpace(orig) != "" {
			f.levels[i] = cur
			continue // not whitespace: leave it
		}
		want := orig
		nls := strings.Count(orig, "\n")
		switch {
		case i == 0:
			want = ""
			cur = 0
		case nls > 0:
			cur = f.indent(i)
			want = strings.Repeat(f.nl, f.lineBreaks(i, nls)) + f.unit(cur)
		case !f.broken:
			want = f.space(f.toks[i-1].n, t.n, orig)
		}
		f.levels[i] = cur
		if want != orig {
			edits = append(edits, Edit{Start: gs, End: t.start, Text: want})
		}
		if nls > 0 || i == 0 {
			edits = append(edits, f.reindentComment(t, f.unit(cur))...)
		}
	}
	if n := len(f.toks); n > 0 {
		end := f.toks[n-1].end
		orig := f.src[end:]
		if strings.TrimSpace(orig) == "" && !strings.HasSuffix(f.src[:end], "\n") {
			want := ""
			if f.o.InsertFinalNewline || strings.Contains(orig, "\n") {
				want = f.nl
			}
			if want != orig {
				edits = append(edits, Edit{Start: end, End: len(f.src), Text: want})
			}
		}
	}
	return edits
}

// lineBreaks returns how many line breaks the gap before token i keeps
// of the nls it has.
func (f *formatter) lineBreaks(i, nls int) int {
	most := f.o.MaxBlankLines + 1
	b := f.toks[i].n
	if b.NumericKind() == psi.KindRBRACE {
		most = f.o.BlankLinesBeforeRBrace + 1
	}
	n := min(nls, max(most, 1))
	// A blank line after the package directive and the imports.
	a := f.toks[i-1].n
	for p := a.Parent(); p.Valid(); p = p.Parent() {
		k := p.NumericKind()
		if k == psi.KindPACKAGE_DIRECTIVE || k == psi.KindIMPORT_LIST {
			if p.Range().End == f.toks[i-1].end && !inside(b, p) {
				n = max(n, 2)
			}
			break
		}
		if k == psi.KindFILE {
			break
		}
	}
	return n
}

func inside(n, p psi.NodeRef) bool {
	for ; n.Valid(); n = n.Parent() {
		if n == p {
			return true
		}
	}
	return false
}

// indent returns the indentation levels of token i, which starts a
// line. A closing bracket is indented no more than the line it opens on.
func (f *formatter) indent(i int) int {
	t := f.toks[i]
	n := f.levelsOf(t.n, t.start)
	if open := opener(t.n); open.Valid() {
		if j, ok := f.index[open.ID()]; ok && j < i {
			n = min(n, f.levels[j])
		}
	}
	return n
}

// unit returns the indentation of levels levels.
func (f *formatter) unit(levels int) string {
	cols := levels * f.o.IndentSize
	if !f.o.UseTabs {
		return strings.Repeat(" ", cols)
	}
	return strings.Repeat("\t", cols/f.o.TabWidth) + strings.Repeat(" ", cols%f.o.TabWidth)
}

// opener returns the bracket a closing bracket closes, or an invalid node
// if n isn't one.
func opener(n psi.NodeRef) psi.NodeRef {
	var want psi.Kind
	switch n.NumericKind() {
	case psi.KindRPAR:
		want = psi.KindLPAR
	case psi.KindRBRACKET:
		want = psi.KindLBRACKET
	case psi.KindRBRACE:
		want = psi.KindLBRACE
	case psi.KindGT:
		if !typeList(n.Parent()) {
			return psi.NodeRef{}
		}
		want = psi.KindLT
	default:
		return psi.NodeRef{}
	}
	for c := n.PreviousSibling(); c.Valid(); c = c.PreviousSibling() {
		if c.NumericKind() == want {
			return c
		}
	}
	return psi.NodeRef{}
}

func typeList(n psi.NodeRef) bool {
	k := n.NumericKind()
	return k == psi.KindTYPE_ARGUMENT_LIST || k == psi.KindTYPE_PARAMETER_LIST
}

// pairsOf returns the bracket pairs among n's children.
func (f *formatter) pairsOf(n psi.NodeRef) []pair {
	if ps, ok := f.pairs[n.ID()]; ok {
		return ps
	}
	var ps []pair
	var open []psi.NodeRef
	tl := typeList(n)
	for c := n.FirstChild(); c.Valid(); c = c.NextSibling() {
		switch k := c.NumericKind(); {
		case k == psi.KindLPAR || k == psi.KindLBRACKET || k == psi.KindLBRACE || k == psi.KindLT && tl:
			open = append(open, c)
		case k == psi.KindRPAR || k == psi.KindRBRACKET || k == psi.KindRBRACE || k == psi.KindGT && tl:
			if len(open) > 0 {
				ps = append(ps, pair{open[len(open)-1], c})
				open = open[:len(open)-1]
			}
		}
	}
	for _, o := range open {
		ps = append(ps, pair{open: o})
	}
	f.pairs[n.ID()] = ps
	return ps
}

// levelsOf returns the indentation levels of the token n at off, which
// starts a line: one for each line on which an enclosing bracket opens or
// a construct continues on the next line, as `a +` or `val x =`.
func (f *formatter) levelsOf(n psi.NodeRef, off int) int {
	keys := map[int]bool{}
	child := n
	for p := n.Parent(); p.Valid(); child, p = p, p.Parent() {
		for _, pr := range f.pairsOf(p) {
			if pr.open.Range().End <= off && (!pr.close.Valid() || off < pr.close.Range().Start) {
				keys[f.line(pr.open.Range().Start)] = true
			}
		}
		f.continuation(p, child, off, keys)
	}
	return len(keys)
}

// continuation adds to keys the line a construct p that continues on the
// lines after it continues from, if the token at off is in what
// continues. child is p's child the token is in.
func (f *formatter) continuation(p, child psi.NodeRef, off int, keys map[int]bool) {
	switch p.NumericKind() {
	case psi.KindDOT_QUALIFIED_EXPRESSION, psi.KindSAFE_ACCESS_EXPRESSION:
		op := p.ChildKind(psi.KindDOT)
		if !op.Valid() {
			op = p.ChildKind(psi.KindSAFE_ACCESS)
		}
		if op.Valid() && off >= op.Range().Start && f.breakAround(op) {
			keys[f.line(chainStart(p, qualified).Range().Start)] = true
		}
		return
	case psi.KindBINARY_EXPRESSION, psi.KindBINARY_WITH_TYPE, psi.KindIS_EXPRESSION:
		if op := p.ChildKind(psi.KindOPERATION_REFERENCE); op.Valid() && off >= op.Range().Start && f.breakAround(op) {
			keys[f.line(chainStart(p, binary).Range().Start)] = true
		}
		return
	case psi.KindWHEN_ENTRY:
		if arrow := p.ChildKind(psi.KindARROW); arrow.Valid() && off >= arrow.Range().Start && f.breakAround(arrow) {
			keys[f.line(arrow.Range().Start)] = true
		}
		if isCondition(child) && child != firstChild(p) {
			keys[f.line(p.Range().Start)] = true
		}
		return
	case psi.KindIF, psi.KindFOR, psi.KindWHILE, psi.KindDO_WHILE:
		switch child.NumericKind() {
		case psi.KindTHEN, psi.KindELSE, psi.KindBODY:
			if f.breakBefore(child.Range().Start) {
				keys[f.line(f.prevEnd(child.Range().Start))] = true
			}
		}
		return
	case psi.KindSUPER_TYPE_LIST, psi.KindTYPE_CONSTRAINT_LIST:
		if child != firstChild(p) {
			keys[f.line(p.Range().Start)] = true
		}
		return
	case psi.KindPROPERTY_DELEGATE:
		if by := p.ChildKind(psi.KindBY_KEYWORD); by.Valid() && off >= by.Range().Start && f.breakAround(by) {
			keys[f.line(by.Range().Start)] = true
		}
		return
	}
	// Declarations: an initializer or body after =, accessors, supertypes
	// and constraints on the lines after the header.
	if eq := p.ChildKind(psi.KindEQ); eq.Valid() && off >= eq.Range().Start && f.breakAround(eq) {
		keys[f.line(eq.Range().Start)] = true
	}
	switch p.NumericKind() {
	case psi.KindPROPERTY:
		switch child.NumericKind() {
		case psi.KindPROPERTY_ACCESSOR, psi.KindBACKING_FIELD, psi.KindPROPERTY_DELEGATE:
			if f.breakBefore(child.Range().Start) {
				keys[f.line(p.Range().Start)] = true
			}
		}
	case psi.KindCLASS, psi.KindOBJECT_DECLARATION, psi.KindFUN:
		if colon := p.ChildKind(psi.KindCOLON); colon.Valid() && p.NumericKind() != psi.KindFUN && off >= colon.Range().Start &&
			child.NumericKind() != psi.KindCLASS_BODY && f.breakAround(colon) {
			keys[f.line(colon.Range().Start)] = true
		}
		switch child.NumericKind() {
		case psi.KindWHERE_KEYWORD, psi.KindTYPE_CONSTRAINT_LIST:
			keys[f.line(p.Range().Start)] = true
		}
	}
}

func isCondition(n psi.NodeRef) bool {
	switch n.NumericKind() {
	case psi.KindWHEN_CONDITION_EXPRESSION, psi.KindWHEN_CONDITION_IS_PATTERN, psi.KindWHEN_CONDITION_IN_RANGE:
		return true
	}
	return false
}

func firstChild(p psi.NodeRef) psi.NodeRef {
	for c := p.FirstChild(); c.Valid(); c = c.NextSibling() {
		if !c.IsTrivia() {
			return c
		}
	}
	return psi.NodeRef{}
}

func qualified(n psi.NodeRef) bool {
	k := n.NumericKind()
	return k == psi.KindDOT_QUALIFIED_EXPRESSION || k == psi.KindSAFE_ACCESS_EXPRESSION
}

func binary(n psi.NodeRef) bool {
	k := n.NumericKind()
	return k == psi.KindBINARY_EXPRESSION || k == psi.KindBINARY_WITH_TYPE || k == psi.KindIS_EXPRESSION
}

// chainStart returns the outermost of the nodes of a kind that n is the
// first operand of, or within: a call chain, or a chain of operators.
func chainStart(n psi.NodeRef, same func(psi.NodeRef) bool) psi.NodeRef {
	for p := n.Parent(); p.Valid() && same(p); p = p.Parent() {
		n = p
	}
	return n
}

// tokenAt returns the index of the token that starts at off, or the
// first after it.
func (f *formatter) tokenAt(off int) int {
	return sort.Search(len(f.toks), func(i int) bool { return f.toks[i].start >= off })
}

// breakBefore reports whether a line break comes before the token at off.
func (f *formatter) breakBefore(off int) bool {
	i := f.tokenAt(off)
	if i == 0 || i >= len(f.toks) {
		return false
	}
	return strings.Contains(f.src[f.toks[i-1].end:f.toks[i].start], "\n")
}

// prevEnd returns where the token before the one at off ends.
func (f *formatter) prevEnd(off int) int {
	if i := f.tokenAt(off); i > 0 {
		return f.toks[i-1].end
	}
	return 0
}

// breakAround reports whether a line break comes before or after n.
func (f *formatter) breakAround(n psi.NodeRef) bool {
	return f.breakBefore(n.Range().Start) || f.breakBefore(n.Range().End)
}

// reindentComment returns the edits that align the lines of a KDoc that
// start with * with its first line, at indent.
func (f *formatter) reindentComment(t tok, indent string) []Edit {
	if t.n.NumericKind() != psi.KindKDOC {
		return nil
	}
	text := f.src[t.start:t.end]
	var edits []Edit
	for i := strings.IndexByte(text, '\n'); i >= 0; {
		ls := i + 1
		j := ls
		for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
			j++
		}
		if j < len(text) && text[j] == '*' {
			if want := indent + " "; text[ls:j] != want {
				edits = append(edits, Edit{Start: t.start + ls, End: t.start + j, Text: want})
			}
		}
		next := strings.IndexByte(text[ls:], '\n')
		if next < 0 {
			break
		}
		i = ls + next
	}
	return edits
}
