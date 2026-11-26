package features

import (
	"slices"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// FoldingRanges returns the regions a client may fold: the import list,
// multi-line comments and runs of line comments, braced bodies (classes,
// functions, lambdas, when) and multi-line string literals. A range ends
// on the line before its closing brace, so that the brace stays visible.
func FoldingRanges(f *File) []protocol.FoldingRange {
	out := []protocol.FoldingRange{}
	m := f.Mapper
	add := func(start, end int, kind string) {
		sl, el := m.Line(start), m.Line(end)
		if el > sl {
			out = append(out, protocol.FoldingRange{StartLine: uint32(sl), EndLine: uint32(el), Kind: kind})
		}
	}
	// braces folds from the opening brace's line to the line before the
	// closing one.
	braces := func(n psi.NodeRef) {
		var open, close psi.NodeRef
		for c := n.FirstChild(); c.Valid(); c = c.NextSibling() {
			switch c.NumericKind() {
			case psi.KindLBRACE:
				if !open.Valid() {
					open = c
				}
			case psi.KindRBRACE:
				close = c
			}
		}
		if !open.Valid() {
			return
		}
		end := n.Range().End
		if close.Valid() {
			end = m.LineStart(m.Line(close.Range().Start)) - 1
		}
		add(open.Range().Start, end, "")
	}
	commentRun := -1 // start of a run of line comments
	commentEnd := 0
	endRun := func() {
		if commentRun >= 0 {
			add(commentRun, commentEnd, protocol.FoldingComment)
		}
		commentRun = -1
	}
	psi.WalkNodes(f.Snapshot.RootNode(), func(n psi.NodeRef) bool {
		k := n.NumericKind()
		if k == psi.KindEOL_COMMENT {
			// A run continues while only whitespace with at most one line
			// break separates the comments.
			if commentRun >= 0 && m.Line(n.Range().Start) == m.Line(commentEnd)+1 {
				commentEnd = n.Range().End
			} else {
				endRun()
				commentRun, commentEnd = n.Range().Start, n.Range().End
			}
			return false
		}
		if n.IsLeaf() && k != psi.KindWHITE_SPACE {
			endRun()
		}
		switch k {
		case psi.KindIMPORT_LIST:
			if imps := importNodes(n); len(imps) > 1 {
				add(imps[0].Range().Start, imps[len(imps)-1].Range().End, protocol.FoldingImports)
			}
			return false
		case psi.KindBLOCK_COMMENT, psi.KindKDOC:
			add(n.Range().Start, n.Range().End, protocol.FoldingComment)
		case psi.KindCLASS_BODY, psi.KindFUNCTION_LITERAL, psi.KindWHEN:
			braces(n)
		case psi.KindBLOCK:
			if n.Parent().NumericKind() != psi.KindFUNCTION_LITERAL {
				braces(n)
			}
		case psi.KindSTRING_TEMPLATE:
			add(n.Range().Start, n.Range().End, "")
		}
		return true
	})
	endRun()
	slices.SortStableFunc(out, func(a, b protocol.FoldingRange) int { return int(a.StartLine) - int(b.StartLine) })
	return out
}

func importNodes(list psi.NodeRef) []psi.NodeRef {
	var out []psi.NodeRef
	for c := list.FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() == psi.KindIMPORT_DIRECTIVE {
			out = append(out, c)
		}
	}
	return out
}

// SelectionRanges returns, for each position, the chain of ever larger
// syntax nodes around it, for expand and shrink selection.
func SelectionRanges(f *File, positions []protocol.Position) ([]protocol.SelectionRange, error) {
	out := make([]protocol.SelectionRange, 0, len(positions))
	for _, p := range positions {
		off, err := f.Mapper.Offset(p)
		if err != nil {
			return nil, err
		}
		n := f.Snapshot.NodeAt(off)
		if n.IsTrivia() && n.NumericKind() == psi.KindWHITE_SPACE {
			// Between tokens, prefer the token the position ends.
			if off > 0 && off == n.Range().Start {
				if prev := f.Snapshot.NodeAt(off - 1); prev.Valid() && !prev.IsTrivia() {
					n = prev
				}
			}
		}
		var chain []psi.Range
		for ; n.Valid(); n = n.Parent() {
			r := n.Range()
			if r.Start == r.End || len(chain) > 0 && chain[len(chain)-1] == r {
				continue
			}
			chain = append(chain, r)
		}
		var sr *protocol.SelectionRange
		for i := len(chain) - 1; i >= 0; i-- {
			sr = &protocol.SelectionRange{Range: f.Mapper.Range(chain[i].Start, chain[i].End), Parent: sr}
		}
		if sr == nil {
			sr = &protocol.SelectionRange{Range: protocol.Range{Start: p, End: p}}
		}
		out = append(out, *sr)
	}
	return out, nil
}

// SyntaxDiagnostics returns the file's syntax errors.
func SyntaxDiagnostics(f *File) []protocol.Diagnostic {
	out := []protocol.Diagnostic{}
	for _, d := range f.Snapshot.Diagnostics() {
		out = append(out, protocol.Diagnostic{
			Range:    f.Mapper.Range(d.Range.Start, d.Range.End),
			Severity: protocol.SeverityError,
			Code:     d.Code,
			Source:   "kc",
			Message:  d.Message,
		})
	}
	return out
}
