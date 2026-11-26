package features

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// applyEdits returns text with edits, which don't overlap, made.
func applyEdits(f *File, edits []protocol.TextEdit) string {
	type edit struct {
		s, e int
		text string
	}
	var es []edit
	for _, e := range edits {
		s, en, _ := f.Mapper.OffsetRange(e.Range)
		es = append(es, edit{s, en, e.NewText})
	}
	sort.Slice(es, func(i, j int) bool { return es[i].s > es[j].s })
	text := f.Mapper.Text()
	for _, e := range es {
		text = text[:e.s] + e.text + text[e.e:]
	}
	return text
}

// TestRefactorings extracts and inlines variables: « » marks a
// selection, ‸ a caret.
func TestRefactorings(t *testing.T) {
	lib := stdlibLibrary(t)
	cases := []struct {
		name, src, title, want string
	}{
		{"extract selection", "fun f(a: Int) {\n    println(«a * 2» + 1)\n}\n", "Extract variable",
			"fun f(a: Int) {\n    val value = a * 2\n    println(value + 1)\n}\n"},
		{"extract call at caret", "fun f(s: String) {\n    val n = s.trim‸().length\n}\n", "Extract variable",
			"fun f(s: String) {\n    val trim = s.trim()\n    val n = trim.length\n}\n"},
		{"extract statement", "fun f() {\n    «listOf(1, 2)»\n}\n", "Extract variable",
			"fun f() {\n    val list = listOf(1, 2)\n}\n"},
		{"extract name clash", "fun f(list: List<Int>) {\n    println(«listOf(1)»)\n}\n", "Extract variable",
			"fun f(list: List<Int>) {\n    val list2 = listOf(1)\n    println(list2)\n}\n"},
		{"extract around a variable", "fun f(a: Int) {\n    println(‸a * 2)\n}\n", "Extract variable",
			"fun f(a: Int) {\n    val value = a * 2\n    println(value)\n}\n"},
		{"extract in branch", "fun f(b: Boolean) {\n    if (b) println(«1 + 1»)\n}\n", "Extract variable", ""},
		{"extract right of &&", "fun f(b: Boolean, s: String) {\n    println(b && «s.isEmpty()»)\n}\n", "Extract variable", ""},
		{"inline", "fun f(a: Int) {\n    val sum = a + 1\n    println(su‸m * 2)\n    println(\"$sum\")\n}\n", "Inline variable",
			"fun f(a: Int) {\n    println((a + 1) * 2)\n    println(\"${a + 1}\")\n}\n"},
		{"inline at declaration", "fun f(s: String) {\n    val ‸t = s.trim()\n    println(t)\n}\n", "Inline variable",
			"fun f(s: String) {\n    println(s.trim())\n}\n"},
		{"inline var", "fun f() {\n    var x = 1\n    x = 2\n    println(‸x)\n}\n", "Inline variable", ""},
		{"inline a call used twice", "fun f(s: String) {\n    val t = s.trim()\n    println(‸t + t)\n}\n", "Inline variable", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := c.src
			start, end := strings.Index(src, "«"), strings.Index(src, "»")
			if start >= 0 {
				src = src[:start] + src[start+len("«"):end] + src[end+len("»"):]
				end -= len("«")
			} else {
				start = strings.Index(src, "‸")
				src = src[:start] + src[start+len("‸"):]
				end = start
			}
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "a.kt"), []byte(src), 0o644)
			a, files := analyzeDir(t, lib, dir)
			f := files[0]
			r := protocol.Range{Start: f.Mapper.Position(start), End: f.Mapper.Position(end)}
			var got string
			var titles []string
			for _, act := range CodeActions(context.Background(), a, nil, f, r, nil, []string{"refactor"}) {
				titles = append(titles, act.Title)
				if strings.HasPrefix(act.Title, c.title) {
					got = applyEdits(f, act.Edit.Changes[f.URI])
				}
			}
			if got != c.want {
				t.Errorf("%s (of %q):\n got %q\nwant %q", c.title, titles, got, c.want)
			}
		})
	}
}
