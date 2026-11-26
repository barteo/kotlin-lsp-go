package features

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// oneModule is a Searcher over a single analysis.
type oneModule struct {
	a     *Analysis
	files []*File
}

func (m oneModule) Search(ctx context.Context, words []string, from string) ([]Scope, error) {
	sc := Scope{Analysis: m.a}
	for _, f := range m.files {
		for _, w := range words {
			if strings.Contains(f.Mapper.Text(), w) {
				sc.Files = append(sc.Files, f)
				break
			}
		}
	}
	return []Scope{sc}, nil
}

// TestWorkspaceGolden analyzes each testdata/workspace/<dir> as a module
// and dumps, at each /*|*/, the references, implementations, rename, and
// the type and call hierarchies, comparing the dump with
// testdata/workspace/<dir>.golden.
func TestWorkspaceGolden(t *testing.T) {
	jar, err := analysis.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	lib, err := analysis.OpenLibrary(analysis.Config{NoJDK: true, ClassPath: []string{jar}, ModuleName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Close()
	dirs, _ := filepath.Glob("testdata/workspace/*")
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		t.Run(filepath.Base(dir), func(t *testing.T) {
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
			res, err := lib.Analyze(context.Background(), snaps, analysis.Options{Warnings: true})
			if err != nil {
				t.Fatal(err)
			}
			a := NewAnalysis(res, "test", files, paths, nil)
			compareGolden(t, dir+".golden", dumpWorkspace(t, a, files))
		})
	}
}

func dumpWorkspace(t *testing.T, a *Analysis, files []*File) string {
	ctx := context.Background()
	s := oneModule{a, files}
	var b strings.Builder
	pos := func(p protocol.Position) string { return fmt.Sprintf("%d:%d", p.Line+1, p.Character+1) }
	byURI := map[protocol.DocumentURI]*File{}
	for _, f := range files {
		byURI[f.URI] = f
	}
	loc := func(uri protocol.DocumentURI, r protocol.Range) string {
		f := byURI[uri]
		if f == nil {
			return string(uri)
		}
		s, e, err := f.Mapper.OffsetRange(r)
		if err != nil {
			t.Fatalf("range %v: %v", r, err)
		}
		return fmt.Sprintf("%s:%s [%s]", filepath.Base(f.Snapshot.Path()), pos(r.Start), f.Mapper.Text()[s:e])
	}
	locs := func(label string, ls []protocol.Location) {
		var out []string
		for _, l := range ls {
			out = append(out, loc(l.URI, l.Range))
		}
		if len(out) > 0 {
			fmt.Fprintf(&b, "%s:\n  %s\n", label, strings.Join(out, "\n  "))
		}
	}
	item := func(it protocol.HierarchyItem) string {
		return fmt.Sprintf("%s (%d, %s) at %s", it.Name, it.Kind, it.Detail, loc(it.URI, it.SelectionRange))
	}
	check := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range files {
		src := f.Mapper.Text()
		fmt.Fprintf(&b, "==== %s\n", filepath.Base(f.Snapshot.Path()))
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
			refs, err := References(ctx, s, a, f, p, true)
			check(err)
			locs("references", refs)
			impls, err := Implementations(ctx, s, a, f, p)
			check(err)
			locs("implementations", impls)
			if pr, err := PrepareRename(a, f, p); err != nil {
				fmt.Fprintf(&b, "prepare rename: %v\n", err)
			} else if pr != nil {
				fmt.Fprintf(&b, "prepare rename: %s %q\n", loc(f.URI, pr.Range), pr.Placeholder)
				edit, err := Rename(ctx, s, a, f, p, "renamed")
				check(err)
				var out []string
				for uri, edits := range edit.Changes {
					for _, e := range edits {
						out = append(out, loc(uri, e.Range))
					}
				}
				slices.Sort(out)
				fmt.Fprintf(&b, "rename:\n  %s\n", strings.Join(out, "\n  "))
			}
			for _, it := range PrepareTypeHierarchy(ctx, a, f, p) {
				fmt.Fprintf(&b, "type hierarchy: %s\n", item(it))
				k, ok := ItemKey(it)
				if !ok {
					t.Fatalf("item %v: no key", it)
				}
				for _, sup := range Supertypes(ctx, a, k) {
					fmt.Fprintf(&b, "  supertype: %s\n", item(sup))
				}
				subs, err := Subtypes(ctx, s, k)
				check(err)
				for _, sub := range subs {
					fmt.Fprintf(&b, "  subtype: %s\n", item(sub))
				}
			}
			for _, it := range PrepareCallHierarchy(ctx, a, f, p) {
				fmt.Fprintf(&b, "call hierarchy: %s\n", item(it))
				k, _ := ItemKey(it)
				in, err := IncomingCalls(ctx, s, a, k)
				check(err)
				for _, c := range in {
					var rs []string
					for _, r := range c.FromRanges {
						rs = append(rs, pos(r.Start))
					}
					fmt.Fprintf(&b, "  incoming: %s from %s\n", item(c.From), strings.Join(rs, " "))
				}
				for _, c := range OutgoingCalls(ctx, a, k) {
					var rs []string
					for _, r := range c.FromRanges {
						rs = append(rs, pos(r.Start))
					}
					fmt.Fprintf(&b, "  outgoing: %s from %s\n", item(c.To), strings.Join(rs, " "))
				}
			}
		}
	}
	return b.String()
}
