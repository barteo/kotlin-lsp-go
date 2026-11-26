package features

import (
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

// analyzeDir analyzes the .kt files of dir as one module against the
// standard library.
func analyzeDir(t *testing.T, lib *analysis.Library, dir string) (*Analysis, []*File) {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(dir, "*.kt"))
	var files []*File
	var snaps []*psi.Snapshot
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f := NewFile(protocol.DocumentURI("file:///"+filepath.ToSlash(p)), p, string(src), protocol.UTF16)
		files = append(files, f)
		snaps = append(snaps, f.Snapshot)
	}
	res, err := lib.Analyze(context.Background(), snaps, analysis.Options{Warnings: true, ExtraWarnings: true})
	if err != nil {
		t.Fatal(err)
	}
	return NewAnalysis(res, "test", files, paths, nil), files
}

func stdlibLibrary(t *testing.T) *analysis.Library {
	t.Helper()
	jar, err := analysis.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	lib, err := analysis.OpenLibrary(analysis.Config{NoJDK: true, ClassPath: []string{jar}, ModuleName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lib.Close() })
	return lib
}

// TestInlayHintsGolden dumps the inlay hints of each file of each
// testdata/inlay/<dir>, with the default options and with all parameter
// names, comparing the dump with testdata/inlay/<dir>.golden.
func TestInlayHintsGolden(t *testing.T) {
	lib := stdlibLibrary(t)
	dirs, _ := filepath.Glob("testdata/inlay/*")
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
			a, files := analyzeDir(t, lib, dir)
			var b strings.Builder
			for _, f := range files {
				all := protocol.Range{End: f.Mapper.Position(len(f.Mapper.Text()))}
				for _, o := range []HintOptions{DefaultHints(), {ParameterNames: "all"}} {
					fmt.Fprintf(&b, "==== %s %+v\n", filepath.Base(f.Snapshot.Path()), o)
					text := f.Mapper.Text()
					for _, h := range InlayHints(a, f, all, o) {
						off, _ := f.Mapper.Offset(h.Position)
						ls := f.Mapper.LineStart(int(h.Position.Line))
						fmt.Fprintf(&b, "%d:%d %q  %s⟨%s⟩%s\n", h.Position.Line+1, h.Position.Character+1, h.Label,
							strings.TrimLeft(text[ls:off], " "), h.Label, text[off:off+strings.IndexByte(text[off:]+"\n", '\n')])
					}
				}
			}
			compareGolden(t, dir+".golden", b.String())
		})
	}
}

func stdlibJar(t *testing.T) string {
	t.Helper()
	jar, err := analysis.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	return jar
}
