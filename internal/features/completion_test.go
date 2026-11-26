package features

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// testCompleter analyzes a module's files with one of them replaced.
type testCompleter struct {
	lib   *analysis.Library
	files []*File
	jar   string
}

func (c *testCompleter) Complete(ctx context.Context, f *File, req *analysis.Complete) (*Analysis, error) {
	var files []*File
	var snaps []*psi.Snapshot
	var own []string
	for _, g := range c.files {
		if g.Snapshot.Path() == f.Snapshot.Path() {
			g = f
		}
		files = append(files, g)
		snaps = append(snaps, g.Snapshot)
		own = append(own, g.Snapshot.Path())
	}
	res, err := c.lib.Analyze(ctx, snaps, analysis.Options{Complete: req})
	if err != nil {
		return nil, err
	}
	return NewAnalysis(res, "test", files, own, nil), nil
}

func (c *testCompleter) LibraryClasses(ctx context.Context, path string, match func(string) bool) []string {
	z, err := zip.OpenReader(c.jar)
	if err != nil {
		return nil
	}
	defer z.Close()
	var out []string
	for _, f := range z.File {
		bin, ok := strings.CutSuffix(f.Name, ".class")
		base := bin[strings.LastIndexByte(bin, '/')+1:]
		if ok && !strings.ContainsRune(base, '$') && !strings.HasSuffix(base, "Kt") && match(base) {
			out = append(out, bin)
		}
	}
	return out
}

// TestCompletionGolden completes at each /*|*/ of each file of each
// testdata/completion/<dir>, a module, the markers removed, and dumps
// the first items, comparing the dump with testdata/completion/<dir>.golden.
func TestCompletionGolden(t *testing.T) {
	jar, err := analysis.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	lib, err := analysis.OpenLibrary(analysis.Config{NoJDK: true, ClassPath: []string{jar}, ModuleName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dirs, _ := filepath.Glob("testdata/completion/*")
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			paths, _ := filepath.Glob(filepath.Join(dir, "*.kt"))
			c := &testCompleter{lib: lib, jar: jar}
			carets := map[string][]int{}
			for _, p := range paths {
				src, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				text := string(src)
				for {
					i := strings.Index(text, caret)
					if i < 0 {
						break
					}
					carets[p] = append(carets[p], i)
					text = text[:i] + text[i+len(caret):]
				}
				c.files = append(c.files, NewFile(protocol.DocumentURI("file:///"+filepath.ToSlash(p)), p, text, protocol.UTF16))
			}
			var b strings.Builder
			for _, f := range c.files {
				for _, off := range carets[f.Snapshot.Path()] {
					pos := f.Mapper.Position(off)
					line := f.Mapper.Text()[f.Mapper.LineStart(int(pos.Line)):off]
					fmt.Fprintf(&b, "== %s:%d:%d %q\n", filepath.Base(f.Snapshot.Path()), pos.Line+1, pos.Character+1, strings.TrimSpace(line))
					l, err := Completion(context.Background(), c, f, pos, CompletionOptions{Snippets: true})
					if err != nil {
						t.Fatal(err)
					}
					dumpCompletion(&b, l)
					h, err := SignatureHelp(context.Background(), c, f, pos, false)
					if err != nil {
						t.Fatal(err)
					}
					dumpSignatures(&b, h)
				}
			}
			compareGolden(t, dir+".golden", b.String())
		})
	}
}

// completionShown is how many items a dump shows.
const completionShown = 12

func dumpCompletion(b *strings.Builder, l *protocol.CompletionList) {
	fmt.Fprintf(b, "%d items, incomplete %v\n", len(l.Items), l.IsIncomplete)
	for i, it := range l.Items {
		if i == completionShown {
			fmt.Fprintf(b, "  …\n")
			break
		}
		fmt.Fprintf(b, "  %s [%d]", it.Label, it.Kind)
		if d := it.LabelDetails; d != nil {
			fmt.Fprintf(b, " %q %q", d.Detail, d.Description)
		}
		if it.TextEdit != nil && it.TextEdit.NewText != it.Label {
			fmt.Fprintf(b, " -> %q", it.TextEdit.NewText)
		}
		for _, e := range it.AdditionalTextEdits {
			fmt.Fprintf(b, " +%q", e.NewText)
		}
		if it.Documentation != nil {
			fmt.Fprintf(b, " doc %q", it.Documentation.Value)
		}
		b.WriteString("\n")
	}
}

func dumpSignatures(b *strings.Builder, h *protocol.SignatureHelp) {
	if h == nil {
		return
	}
	fmt.Fprintf(b, "signatures, active %d, parameter %d:\n", h.ActiveSignature, h.ActiveParameter)
	for _, s := range h.Signatures {
		fmt.Fprintf(b, "  %s", s.Label)
		if s.ActiveParameter != nil {
			fmt.Fprintf(b, " [%v]", s.Parameters[*s.ActiveParameter].Label)
		}
		if s.Documentation != nil {
			fmt.Fprintf(b, " doc %q", s.Documentation.Value)
		}
		b.WriteString("\n")
	}
}
