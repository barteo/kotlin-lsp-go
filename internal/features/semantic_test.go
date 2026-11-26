package features

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/internal/classpath"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// caret marks a position in a semantic test source: the position right
// after it.
const caret = "/*|*/"

// TestSemanticGolden analyzes each testdata/semantic/<dir> as a module,
// against the standard library without the JDK, and dumps the
// diagnostics of each file and, at each /*|*/ in it, the hover,
// definition, type definition and highlights, comparing the dump with
// testdata/semantic/<dir>.golden.
func TestSemanticGolden(t *testing.T) {
	jar, err := classpath.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	lib, err := analysis.OpenLibrary(analysis.Config{NoJDK: true, ClassPath: []string{jar}, ModuleName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dirs, _ := filepath.Glob("testdata/semantic/*")
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			paths, _ := filepath.Glob(filepath.Join(dir, "*.kt"))
			var files []*File
			var own []string
			for _, p := range paths {
				src, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, NewFile(protocol.DocumentURI("file:///"+filepath.ToSlash(p)), p, string(src), protocol.UTF16))
				own = append(own, p)
			}
			var snaps []*psi.Snapshot
			for _, f := range files {
				snaps = append(snaps, f.Snapshot)
			}
			res, err := lib.Analyze(context.Background(), snaps, analysis.Options{Warnings: true})
			if err != nil {
				t.Fatal(err)
			}
			a := NewAnalysis(res, "test", files, own, nil)
			got := dumpSemantic(t, a, files)
			compareGolden(t, dir+".golden", got)
		})
	}
}

func compareGolden(t *testing.T, golden, got string) {
	t.Helper()
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
}

func dumpSemantic(t *testing.T, a *Analysis, files []*File) string {
	ctx := context.Background()
	var b strings.Builder
	pos := func(p protocol.Position) string { return fmt.Sprintf("%d:%d", p.Line+1, p.Character+1) }
	byURI := map[protocol.DocumentURI]*File{}
	for _, f := range files {
		byURI[f.URI] = f
	}
	text := func(f *File, r protocol.Range) string {
		s, e, err := f.Mapper.OffsetRange(r)
		if err != nil {
			t.Fatalf("range %v: %v", r, err)
		}
		return f.Mapper.Text()[s:e]
	}
	link := func(l protocol.LocationLink) string {
		f := byURI[l.TargetURI]
		if f == nil {
			return string(l.TargetURI)
		}
		if !contains(l.TargetRange, l.TargetSelectionRange) {
			t.Errorf("link %v: selection outside range", l)
		}
		return fmt.Sprintf("%s:%s [%s]", filepath.Base(f.Snapshot.Path()), pos(l.TargetSelectionRange.Start), text(f, l.TargetSelectionRange))
	}
	for _, f := range files {
		src := f.Mapper.Text()
		fmt.Fprintf(&b, "==== %s\n", filepath.Base(f.Snapshot.Path()))
		b.WriteString("== diagnostics\n")
		for _, d := range Diagnostics(a, f) {
			sev := [...]string{"", "error", "warning", "info", "hint"}[d.Severity]
			fmt.Fprintf(&b, "%s-%s %s %s [%s]: %s\n", pos(d.Range.Start), pos(d.Range.End), sev, d.Code, text(f, d.Range), d.Message)
		}
		for off := 0; ; {
			i := strings.Index(src[off:], caret)
			if i < 0 {
				break
			}
			off += i + len(caret)
			p := f.Mapper.Position(off)
			word := src[off:]
			if j := strings.IndexFunc(word, func(r rune) bool { return !isWord(string(r)) }); j >= 0 {
				word = word[:j]
			}
			fmt.Fprintf(&b, "== at %s %s\n", pos(p), word)
			if h := Hover(ctx, a, f, p); h != nil {
				fmt.Fprintf(&b, "hover [%s]:\n", text(f, *h.Range))
				for _, l := range strings.Split(h.Contents.Value, "\n") {
					b.WriteString("  " + l + "\n")
				}
			}
			for _, l := range Definition(ctx, a, f, p) {
				fmt.Fprintf(&b, "definition: %s\n", link(l))
			}
			for _, l := range TypeDefinition(ctx, a, f, p) {
				fmt.Fprintf(&b, "type definition: %s\n", link(l))
			}
			var hs []string
			for _, h := range DocumentHighlights(a, f, p) {
				hs = append(hs, fmt.Sprintf("%s%s", pos(h.Range.Start), [...]string{"", "", "", "(w)"}[h.Kind]))
			}
			if len(hs) > 0 {
				fmt.Fprintf(&b, "highlights: %s\n", strings.Join(hs, " "))
			}
		}
	}
	return b.String()
}
