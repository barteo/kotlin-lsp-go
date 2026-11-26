package features

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

// TestGolden dumps every syntax feature of each testdata/*.kt file and
// compares the dump with testdata/<name>.golden. Run with -update to
// rewrite the golden files after checking the change.
func TestGolden(t *testing.T) {
	files, err := filepath.Glob("testdata/*.kt")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got := dump(t, path, string(src))
			golden := strings.TrimSuffix(path, ".kt") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if got != string(want) {
				t.Errorf("dump differs from %s (run with -update to accept):\n%s", golden, diff(string(want), got))
			}
		})
	}
}

func dump(t *testing.T, path, src string) string {
	f := NewFile(protocol.DocumentURI("file:///"+path), path, src, protocol.UTF16)
	var b strings.Builder
	text := func(r protocol.Range) string {
		s, e, err := f.Mapper.OffsetRange(r)
		if err != nil {
			t.Fatalf("range %v: %v", r, err)
		}
		return src[s:e]
	}
	pos := func(p protocol.Position) string { return fmt.Sprintf("%d:%d", p.Line+1, p.Character+1) }
	rng := func(r protocol.Range) string { return pos(r.Start) + "-" + pos(r.End) }

	b.WriteString("== diagnostics\n")
	for _, d := range SyntaxDiagnostics(f) {
		fmt.Fprintf(&b, "%s %s: %s\n", rng(d.Range), d.Code, d.Message)
	}

	b.WriteString("== symbols\n")
	var walk func(ss []protocol.DocumentSymbol, depth int)
	walk = func(ss []protocol.DocumentSymbol, depth int) {
		for _, s := range ss {
			if !contains(s.Range, s.SelectionRange) {
				t.Errorf("%s: selection range %v outside range %v", s.Name, s.SelectionRange, s.Range)
			}
			tags := ""
			if len(s.Tags) > 0 {
				tags = " deprecated"
			}
			fmt.Fprintf(&b, "%s%s %s %q [%s]%s\n", strings.Repeat("  ", depth), kindName(s.Kind), s.Name, s.Detail, text(s.SelectionRange), tags)
			walk(s.Children, depth+1)
		}
	}
	walk(DocumentSymbols(f), 0)

	b.WriteString("== folding\n")
	for _, r := range FoldingRanges(f) {
		fmt.Fprintf(&b, "%d-%d %s\n", r.StartLine+1, r.EndLine+1, r.Kind)
	}

	b.WriteString("== tokens\n")
	toks, err := SemanticTokens(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	var line, char uint32
	for i := 0; i+4 < len(toks.Data); i += 5 {
		d := toks.Data[i : i+5]
		if d[0] > 0 {
			char = 0
		}
		line += d[0]
		char += d[1]
		start := protocol.Position{Line: line, Character: char}
		end := protocol.Position{Line: line, Character: char + d[2]}
		var mods []string
		for j, m := range TokenModifiers {
			if d[4]&(1<<j) != 0 {
				mods = append(mods, m)
			}
		}
		fmt.Fprintf(&b, "%s %s %q %s\n", pos(start), TokenTypes[d[3]], text(protocol.Range{Start: start, End: end}), strings.Join(mods, ","))
	}

	// Selection ranges at the first name of each declaration line.
	b.WriteString("== selection\n")
	if i := strings.Index(src, "x"); i >= 0 {
		srs, err := SelectionRanges(f, []protocol.Position{f.Mapper.Position(i)})
		if err != nil {
			t.Fatal(err)
		}
		for sr := &srs[0]; sr != nil; sr = sr.Parent {
			s := text(sr.Range)
			if r := []rune(s); len(r) > 40 {
				s = string(r[:40]) + "…"
			}
			fmt.Fprintf(&b, "%s %q\n", rng(sr.Range), s)
		}
	}
	return b.String()
}

func contains(outer, inner protocol.Range) bool {
	less := func(a, b protocol.Position) bool {
		return a.Line < b.Line || a.Line == b.Line && a.Character <= b.Character
	}
	return less(outer.Start, inner.Start) && less(inner.End, outer.End)
}

func kindName(k protocol.SymbolKind) string {
	switch k {
	case protocol.SymbolClass:
		return "class"
	case protocol.SymbolInterface:
		return "interface"
	case protocol.SymbolEnum:
		return "enum"
	case protocol.SymbolObject:
		return "object"
	case protocol.SymbolFunction:
		return "function"
	case protocol.SymbolMethod:
		return "method"
	case protocol.SymbolProperty:
		return "property"
	case protocol.SymbolConstant:
		return "constant"
	case protocol.SymbolConstructor:
		return "constructor"
	case protocol.SymbolEnumMember:
		return "enumMember"
	}
	return fmt.Sprint(int(k))
}

// diff returns the lines of want and got that differ, as a line diff
// would show the first differences.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := 0
	for i := 0; i < max(len(w), len(g)) && n < 20; i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d:\n- %s\n+ %s\n", i+1, wl, gl)
			n++
		}
	}
	return b.String()
}
