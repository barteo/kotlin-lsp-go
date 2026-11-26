package features

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// TestCodeActionsGolden lists, for each file of each
// testdata/actions/<dir>, its diagnostics and the code actions for each,
// the intentions at each /*|*/, and organize imports, with the code each
// action's edit makes of the line, comparing the dump with
// testdata/actions/<dir>.golden.
func TestCodeActionsGolden(t *testing.T) {
	lib := stdlibLibrary(t)
	dirs, _ := filepath.Glob("testdata/actions/*")
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			a, files := analyzeDir(t, lib, dir)
			c := &testCompleter{lib: lib, files: files, jar: stdlibJar(t)}
			var b strings.Builder
			for _, f := range files {
				fmt.Fprintf(&b, "==== %s\n", filepath.Base(f.Snapshot.Path()))
				diags := Diagnostics(a, f)
				for _, d := range diags {
					fmt.Fprintf(&b, "== %d:%d %s %s\n", d.Range.Start.Line+1, d.Range.Start.Character+1, d.Code, d.Message)
					acts := CodeActions(context.Background(), a, c, f, d.Range, []protocol.Diagnostic{d}, nil)
					dumpActions(&b, f, acts, func(act protocol.CodeAction) bool { return len(act.Diagnostics) > 0 })
				}
				fmt.Fprintf(&b, "== intentions\n")
				seen := map[string]bool{}
				text := f.Mapper.Text()
				for off := range len(text) {
					if !isWordStart(text, off) {
						continue
					}
					pos := f.Mapper.Position(off)
					r := protocol.Range{Start: pos, End: pos}
					acts := CodeActions(context.Background(), a, c, f, r, nil, []string{protocol.RefactorRewrite})
					for _, act := range acts {
						var d strings.Builder
						dumpActions(&d, f, []protocol.CodeAction{act}, func(protocol.CodeAction) bool { return true })
						if !seen[d.String()] {
							seen[d.String()] = true
							b.WriteString(d.String())
						}
					}
				}
				fmt.Fprintf(&b, "== source\n")
				acts := CodeActions(context.Background(), a, c, f, protocol.Range{}, nil, []string{"source"})
				dumpActions(&b, f, acts, func(protocol.CodeAction) bool { return true })
			}
			compareGolden(t, dir+".golden", b.String())
		})
	}
}

func dumpActions(b *strings.Builder, f *File, acts []protocol.CodeAction, keep func(protocol.CodeAction) bool) {
	for _, act := range acts {
		if !keep(act) {
			continue
		}
		pref := ""
		if act.IsPreferred {
			pref = " (preferred)"
		}
		fmt.Fprintf(b, "  %s [%s]%s\n", act.Title, act.Kind, pref)
		if act.Edit == nil {
			continue
		}
		for _, e := range act.Edit.Changes[f.URI] {
			s, en, _ := f.Mapper.OffsetRange(e.Range)
			fmt.Fprintf(b, "    %d:%d-%d:%d %q -> %q\n", e.Range.Start.Line+1, e.Range.Start.Character+1, e.Range.End.Line+1, e.Range.End.Character+1,
				f.Mapper.Text()[s:en], e.NewText)
		}
	}
}

// isWordStart reports whether a word starts at off: the cursor positions
// the test asks for intentions at.
func isWordStart(text string, off int) bool {
	w := func(b byte) bool {
		return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
	}
	return w(text[off]) && (off == 0 || !w(text[off-1]))
}
