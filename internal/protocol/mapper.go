package protocol

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// Mapper converts between byte offsets in a text and LSP positions in one
// position encoding. Lines end at "\n"; a "\r" before it belongs to the
// line break, not to the line. A Mapper is immutable.
type Mapper struct {
	text       string
	enc        string
	lineStarts []int
	ascii      bool
}

// NewMapper returns a mapper over text for the encoding enc (UTF8, UTF16
// or UTF32; anything else is UTF16).
func NewMapper(text, enc string) *Mapper {
	m := &Mapper{text: text, enc: enc, lineStarts: []int{0}, ascii: true}
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\n':
			m.lineStarts = append(m.lineStarts, i+1)
		case c >= utf8.RuneSelf:
			m.ascii = false
		}
	}
	return m
}

// Text returns the mapped text.
func (m *Mapper) Text() string { return m.text }

// Encoding returns the position encoding.
func (m *Mapper) Encoding() string { return m.enc }

// LineCount returns the number of lines; a text ending in "\n" has an
// empty last line.
func (m *Mapper) LineCount() int { return len(m.lineStarts) }

// lineEnd returns the offset where line l's content ends, before its
// line break.
func (m *Mapper) lineEnd(l int) int {
	if l+1 < len(m.lineStarts) {
		e := m.lineStarts[l+1] - 1
		if e > m.lineStarts[l] && m.text[e-1] == '\r' {
			e--
		}
		return e
	}
	return len(m.text)
}

// Line returns the zero-based line of a byte offset, which is clamped to
// the text.
func (m *Mapper) Line(off int) int {
	off = min(max(off, 0), len(m.text))
	return sort.Search(len(m.lineStarts), func(i int) bool { return m.lineStarts[i] > off }) - 1
}

// units counts s in the encoding's code units.
func (m *Mapper) units(s string) int {
	if m.ascii || m.enc == UTF8 {
		return len(s)
	}
	n := 0
	for _, r := range s {
		switch {
		case m.enc == UTF32:
			n++
		case r >= 0x10000:
			n += 2
		default:
			n++
		}
	}
	return n
}

// Position returns the position of a byte offset, which is clamped to the
// text. An offset inside a line break is at the end of its line.
func (m *Mapper) Position(off int) Position {
	off = min(max(off, 0), len(m.text))
	l := m.Line(off)
	off = min(off, m.lineEnd(l))
	return Position{Line: uint32(l), Character: uint32(m.units(m.text[m.lineStarts[l]:off]))}
}

// Range returns the range of the byte offsets [start, end).
func (m *Mapper) Range(start, end int) Range {
	return Range{Start: m.Position(start), End: m.Position(end)}
}

// Offset returns the byte offset of a position. As the protocol asks, a
// character past the end of its line is the end of the line; a line past
// the last is the end of the text. A position inside a character (the
// middle of a UTF-16 surrogate pair or of a UTF-8 sequence) is an error.
func (m *Mapper) Offset(p Position) (int, error) {
	l := int(p.Line)
	if l >= len(m.lineStarts) {
		return len(m.text), nil
	}
	start, end := m.lineStarts[l], m.lineEnd(l)
	want := int(p.Character)
	if m.ascii || m.enc == UTF8 {
		off := min(start+want, end)
		if !m.ascii && off < end && !utf8.RuneStart(m.text[off]) {
			return 0, fmt.Errorf("position %d:%d is inside a character", p.Line, p.Character)
		}
		return off, nil
	}
	n := 0
	for i, r := range m.text[start:end] {
		if n == want {
			return start + i, nil
		}
		w := 1
		if m.enc != UTF32 && r >= 0x10000 {
			w = 2
		}
		if n+w > want {
			return 0, fmt.Errorf("position %d:%d is inside a character", p.Line, p.Character)
		}
		n += w
	}
	return end, nil
}

// OffsetRange returns the byte offsets of a range.
func (m *Mapper) OffsetRange(r Range) (start, end int, err error) {
	if start, err = m.Offset(r.Start); err != nil {
		return 0, 0, err
	}
	if end, err = m.Offset(r.End); err != nil {
		return 0, 0, err
	}
	if end < start {
		return 0, 0, fmt.Errorf("range end %d:%d before start %d:%d", r.End.Line, r.End.Character, r.Start.Line, r.Start.Character)
	}
	return start, end, nil
}

// LineStart returns the byte offset where line l starts, clamped.
func (m *Mapper) LineStart(l int) int {
	if l < 0 {
		return 0
	}
	if l >= len(m.lineStarts) {
		return len(m.text)
	}
	return m.lineStarts[l]
}
