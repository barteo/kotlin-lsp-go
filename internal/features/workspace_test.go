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

// countingSearcher records the words of each search.
type countingSearcher struct {
	Searcher
	searches [][]string
}

func (c *countingSearcher) Search(ctx context.Context, words []string, from string) ([]Scope, error) {
	c.searches = append(c.searches, words)
	return c.Searcher.Search(ctx, words, from)
}

// TestReferencesScope checks that the references, rename and
// implementations of a declaration only its file can refer to, or of a
// member nothing can override, don't search the workspace, and that
// those of an open member still find its overrides' references.
func TestReferencesScope(t *testing.T) {
	lib := stdlibLibrary(t)
	dir := t.TempDir()
	srcs := map[string]string{
		"A.kt": `package p

class FooViewModel(handle: Map<String, String>) {
    private val url: String by lazy { checkNotNull(handle[URL_ARG]) }

    private companion object {
        const val URL_ARG = "url"
    }
}

class Plain private constructor(val shown: Int) {
    private fun secret() = 1
    fun useSecret() = secret()
    fun finalMember() = 2
}

object Single {
    fun member() = 3
}

open class Base {
    open fun greet() = "base"
}

private fun hidden() = 4
fun useHidden() = hidden()
`,
		"B.kt": `package p

class Derived : Base() {
    override fun greet() = "derived"
}

fun calls(b: Base, d: Derived, p: Plain) {
    b.greet()
    d.greet()
    p.finalMember()
    p.shown
    Single.member()
    hidden()
}
`,
	}
	for name, src := range srcs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, files := analyzeDir(t, lib, dir)
	byName := map[string]*File{}
	for _, f := range files {
		byName[filepath.Base(f.Snapshot.Path())] = f
	}
	ctx := context.Background()
	// at returns the position of the n-th (from 0) occurrence of sub in
	// the file name.
	at := func(name, sub string, n int) (*File, protocol.Position) {
		f := byName[name]
		text, off := f.Mapper.Text(), -1
		for i := 0; i <= n; i++ {
			j := strings.Index(text[off+1:], sub)
			if j < 0 {
				t.Fatalf("no %q #%d in %s", sub, n, name)
			}
			off += 1 + j
		}
		return f, f.Mapper.Position(off)
	}
	where := func(locs []protocol.Location) []string {
		var out []string
		for _, l := range locs {
			out = append(out, fmt.Sprintf("%s:%d", filepath.Base(string(l.URI)), l.Range.Start.Line+1))
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		file     string
		sub      string
		n        int
		refs     []string   // where the references are, with the declaration
		searches [][]string // the words searched, in order
	}{
		{"private companion const", "A.kt", "URL_ARG", 1, []string{"A.kt:4", "A.kt:7"}, nil},
		{"private companion const at use", "A.kt", "URL_ARG", 0, []string{"A.kt:4", "A.kt:7"}, nil},
		{"private member", "A.kt", "secret", 0, []string{"A.kt:12", "A.kt:13"}, nil},
		{"private top-level", "A.kt", "hidden", 0, []string{"A.kt:25", "A.kt:26"}, nil},
		{"private class's member", "A.kt", "url", 0, []string{"A.kt:4"}, nil},
		{"final class's member", "A.kt", "finalMember", 0, []string{"A.kt:14", "B.kt:10"}, [][]string{{"finalMember"}}},
		{"private constructor's property", "A.kt", "shown", 0, []string{"A.kt:11", "B.kt:11"}, [][]string{{"shown"}}},
		{"object's member", "A.kt", "member", 0, []string{"A.kt:18", "B.kt:12"}, [][]string{{"member"}}},
		{"open member", "A.kt", "greet", 0, []string{"A.kt:22", "B.kt:8", "B.kt:9"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &countingSearcher{Searcher: oneModule{a, files}}
			f, p := at(tc.file, tc.sub, tc.n)
			locs, err := References(ctx, s, a, f, p, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := where(locs); !slices.Equal(got, tc.refs) {
				t.Errorf("references: got %v, want %v", got, tc.refs)
			}
			if tc.name == "open member" {
				if !slices.ContainsFunc(s.searches, func(w []string) bool { return slices.Equal(w, []string{"Base"}) }) {
					t.Errorf("open member's subclasses not searched: %v", s.searches)
				}
				return
			}
			if !slices.EqualFunc(s.searches, tc.searches, slices.Equal) {
				t.Errorf("searches: got %v, want %v", s.searches, tc.searches)
			}
		})
	}
	t.Run("rename and implementations", func(t *testing.T) {
		s := &countingSearcher{Searcher: oneModule{a, files}}
		f, p := at("A.kt", "URL_ARG", 1)
		edit, err := Rename(ctx, s, a, f, p, "ARG")
		if err != nil {
			t.Fatal(err)
		}
		if n := len(edit.Changes[f.URI]); n != 2 || len(edit.Changes) != 1 {
			t.Errorf("rename edits: %+v", edit.Changes)
		}
		for _, sub := range []string{"finalMember", "Plain", "member", "Single"} {
			f, p := at("A.kt", sub, 0)
			if impls, err := Implementations(ctx, s, a, f, p); err != nil || len(impls) != 0 {
				t.Errorf("implementations of %s: %v %v", sub, impls, err)
			}
		}
		if len(s.searches) != 0 {
			t.Errorf("searches: %v", s.searches)
		}
		f, p = at("A.kt", "greet", 0)
		impls, err := Implementations(ctx, s, a, f, p)
		if err != nil || !slices.Equal(where(impls), []string{"B.kt:4"}) {
			t.Errorf("implementations of greet: %v %v", where(impls), err)
		}
	})
}
