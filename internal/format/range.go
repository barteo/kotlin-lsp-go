package format

import (
	"strings"

	"github.com/barteo/kotlin-compiler-go/psi"
)

// Range returns the edits of Format that touch [start, end].
func Range(s *psi.Snapshot, o Options, start, end int) []Edit {
	var out []Edit
	for _, e := range Format(s, o) {
		if e.End >= start && e.Start <= end {
			out = append(out, e)
		}
	}
	return out
}

// Placeholder is the name OnType writes on a blank line to learn its
// indentation.
const Placeholder = "kc__indent"

// OnType returns the edits that format what typing ch before off ended:
// the line of a closing brace, or, for a line break, the line it ended
// and the indentation of the line it began.
func OnType(s *psi.Snapshot, o Options, off int, ch string) []Edit {
	src := s.Source()
	off = min(max(off, 0), len(src))
	ls := strings.LastIndexByte(src[:off], '\n') + 1
	le := len(src)
	if i := strings.IndexByte(src[off:], '\n'); i >= 0 {
		le = off + i
	}
	switch ch {
	case "}", ";", ")":
		return Range(s, o, ls, le)
	case "\n":
	default:
		return nil
	}
	var out []Edit
	if ls > 0 {
		// The line ended: its own spacing and indentation, not the line
		// break after it.
		pls := strings.LastIndexByte(src[:ls-1], '\n') + 1
		indented := firstNonSpace(src, pls)
		for _, e := range Format(s, o) {
			within := e.Start >= pls && e.End < ls
			if within || e.End == indented && indented < ls-1 {
				out = append(out, e)
			}
		}
	}
	// The line begun: indented as a token on it would be.
	first := firstNonSpace(src, ls)
	if first > le {
		first = le
	}
	text, at := src, first
	if first == le {
		text = src[:first] + Placeholder + src[first:]
	}
	ps := s
	if text != src {
		ps = psi.Parse(s.Path(), text)
	}
	if indent, ok := indentOf(ps, o, at); ok && src[ls:first] != indent {
		out = append(out, Edit{Start: ls, End: first, Text: indent})
	}
	return out
}

func firstNonSpace(src string, off int) int {
	for off < len(src) && (src[off] == ' ' || src[off] == '\t') {
		off++
	}
	return off
}

// indentOf returns the indentation Format gives the line of the token
// that starts at off, if it starts the line.
func indentOf(s *psi.Snapshot, o Options, off int) (string, bool) {
	f := newFormatter(s, o)
	f.format()
	i := f.tokenAt(off)
	if i >= len(f.toks) || f.toks[i].start != off || i == 0 && off != 0 {
		return "", false
	}
	if i > 0 && !strings.Contains(f.src[f.toks[i-1].end:off], "\n") {
		return "", false
	}
	return f.unit(f.levels[i]), true
}
