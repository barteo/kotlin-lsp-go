// Package features computes kcls's answers to LSP requests from a parsed
// file. The functions here need only the psi tree: document symbols,
// folding ranges, selection ranges, syntax diagnostics and lexical
// semantic tokens. They never wait for semantic analysis.
//
// Each takes a File: a snapshot and a Mapper over the same text, which
// converts psi's byte offsets to positions in the client's encoding.
package features

import (
	"strings"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// File is a parsed document.
type File struct {
	URI      protocol.DocumentURI
	Snapshot *psi.Snapshot
	Mapper   *protocol.Mapper
}

// NewFile parses text as the file at path, mapping positions in enc.
func NewFile(uri protocol.DocumentURI, path, text, enc string) *File {
	return &File{URI: uri, Snapshot: psi.Parse(path, text), Mapper: protocol.NewMapper(text, enc)}
}

// Range returns the LSP range of a node.
func (f *File) Range(n psi.NodeRef) protocol.Range {
	r := n.Range()
	return f.Mapper.Range(r.Start, r.End)
}

// compact returns the node's text with trivia dropped and whitespace
// normalized, as a one-line signature shows it: "(x: Int, y: String)".
func compact(n psi.NodeRef) string {
	var b strings.Builder
	prev := byte(0)
	psi.WalkNodes(n, func(c psi.NodeRef) bool {
		if !c.IsLeaf() {
			return true
		}
		if c.IsTrivia() || c.Text() == "" {
			return false
		}
		t := c.Text()
		switch c.NumericKind() {
		case psi.KindCOMMA:
			b.WriteString(", ")
			prev = ' '
			return false
		case psi.KindCOLON:
			b.WriteString(": ")
			prev = ' '
			return false
		case psi.KindEQ, psi.KindARROW:
			if prev != ' ' && prev != 0 {
				b.WriteByte(' ')
			}
			b.WriteString(t + " ")
			prev = ' '
			return false
		}
		if wordByte(prev) && wordByte(t[0]) {
			b.WriteByte(' ')
		}
		b.WriteString(t)
		prev = t[len(t)-1]
		return false
	})
	s := strings.TrimSpace(b.String())
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

func wordByte(c byte) bool {
	return c == '_' || c == '`' || c >= 0x80 || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// hasModifier reports whether a declaration's modifier list has the
// keyword k.
func hasModifier(decl psi.NodeRef, k psi.Kind) bool {
	return decl.ChildKind(psi.KindMODIFIER_LIST).ChildKind(k).Valid()
}

// annotated reports whether a declaration has an annotation whose type's
// last name is name.
func annotated(decl psi.NodeRef, name string) bool {
	for c := decl.ChildKind(psi.KindMODIFIER_LIST).FirstChild(); c.Valid(); c = c.NextSibling() {
		if c.NumericKind() != psi.KindANNOTATION_ENTRY {
			continue
		}
		t := c.ChildKind(psi.KindCONSTRUCTOR_CALLEE).ChildKind(psi.KindTYPE_REFERENCE).ChildKind(psi.KindUSER_TYPE)
		if t.ChildKind(psi.KindREFERENCE_EXPRESSION).Name() == name {
			return true
		}
	}
	return false
}
