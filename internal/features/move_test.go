package features

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// TestMoveFiles moves a file to another package directory.
func TestMoveFiles(t *testing.T) {
	lib := stdlibLibrary(t)
	root := t.TempDir()
	sources := map[string]string{
		"src/a/b/Util.kt":    "package a.b\n\nclass Util(val s: Sibling)\n\nfun helper() = Sibling()\n\nprivate fun hidden() {}\n",
		"src/a/b/Sibling.kt": "package a.b\n\nclass Sibling\n",
		"src/a/b/User.kt":    "package a.b\n\nfun use() {\n    Util(Sibling())\n    helper()\n}\n",
		"src/c/Client.kt":    "package c\n\nimport a.b.Util\n\nfun client(u: Util) = a.b.helper()\n",
	}
	var paths []string
	for p, src := range sources {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(src), 0o644)
		paths = append(paths, full)
	}
	sort.Strings(paths)
	var files []*File
	var snaps []*psi.Snapshot
	for _, p := range paths {
		f := NewFile(protocol.DocumentURI("file://"+p), p, sources[strings.TrimPrefix(p, root+"/")], protocol.UTF16)
		files = append(files, f)
		snaps = append(snaps, f.Snapshot)
	}
	res, err := lib.Analyze(context.Background(), snaps, analysis.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := NewAnalysis(res, "test", files, paths, nil)
	analyze := func(ctx context.Context, path string) (*Analysis, error) { return a, nil }
	from := filepath.Join(root, "src/a/b/Util.kt")
	e, err := MoveFiles(context.Background(), oneModule{a, files}, analyze, map[string]string{from: filepath.Join(root, "src/a/x/Util.kt")})
	if err != nil || e == nil {
		t.Fatalf("MoveFiles: %v, %v", e, err)
	}
	want := map[string]string{
		"src/a/b/Util.kt":    "package a.x\n\nimport a.b.Sibling\n\nclass Util(val s: Sibling)\n\nfun helper() = Sibling()\n\nprivate fun hidden() {}\n",
		"src/a/b/User.kt":    "package a.b\n\nimport a.x.Util\nimport a.x.helper\n\nfun use() {\n    Util(Sibling())\n    helper()\n}\n",
		"src/c/Client.kt":    "package c\n\nimport a.x.Util\n\nfun client(u: Util) = a.x.helper()\n",
		"src/a/b/Sibling.kt": sources["src/a/b/Sibling.kt"],
	}
	for _, f := range files {
		rel := strings.TrimPrefix(f.Snapshot.Path(), root+"/")
		if got := applyEdits(f, e.Changes[f.URI]); got != want[rel] {
			t.Errorf("%s:\n got %q\nwant %q", rel, got, want[rel])
		}
	}

	// A file whose directory isn't its package's moves as it is.
	if e, _ := MoveFiles(context.Background(), oneModule{a, files}, analyze, map[string]string{filepath.Join(root, "src/c/Client.kt"): filepath.Join(root, "src/d/Client.kt")}); e == nil {
		t.Errorf("moving c/Client.kt to d: no edits")
	}
	if got := packageOrNot(filepath.Join(root, "src/Flat.kt"), "a.b", filepath.Join(root, "src/x/Flat.kt")); got != "-" {
		t.Errorf("a.b in src/: %s", got)
	}
}

func packageOrNot(path, pkg, newPath string) string {
	p, ok := packageFor(path, pkg, newPath)
	if !ok {
		return "-"
	}
	return p
}
