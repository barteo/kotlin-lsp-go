package format

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/psi"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// Apply returns src with the edits made.
func apply(src string, edits []Edit) string {
	var b strings.Builder
	last := 0
	for _, e := range edits {
		b.WriteString(src[last:e.Start])
		b.WriteString(e.Text)
		last = e.End
	}
	b.WriteString(src[last:])
	return b.String()
}

func formatString(path, src string, o Options) string {
	return apply(src, Format(psi.Parse(path, src), o))
}

// TestGolden formats each testdata/*.kt, comparing the result with the
// .golden file next to it.
func TestGolden(t *testing.T) {
	paths, _ := filepath.Glob("testdata/*.kt")
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			got := formatString(p, string(src), Default())
			golden := strings.TrimSuffix(p, ".kt") + ".golden"
			if *update {
				os.WriteFile(golden, []byte(got), 0o644)
				return
			}
			want, _ := os.ReadFile(golden)
			if got != string(want) {
				t.Errorf("%s differs from %s:\n%s", p, golden, diffLines(string(want), got))
			}
			if again := formatString(p, got, Default()); again != got {
				t.Errorf("formatting %s again changes it:\n%s", p, diffLines(got, again))
			}
		})
	}
}

// TestWhitespaceOnly formats every Kotlin file of this repository and of
// the kotlin-compiler-go module go.mod requires, and checks that only
// whitespace changed, and that formatting again changes nothing.
func TestWhitespaceOnly(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/barteo/kotlin-compiler-go").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var files []string
	walk := func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".kt") || strings.HasSuffix(p, ".kts") {
			files = append(files, p)
		}
		return nil
	}
	filepath.WalkDir("../..", walk)
	filepath.WalkDir(strings.TrimSpace(string(out)), walk)
	if len(files) < 100 {
		t.Fatalf("only %d Kotlin files", len(files))
	}
	sort.Strings(files)
	unstable := 0
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		got := formatString(p, string(src), Default())
		if a, b := tokenText(p, string(src)), tokenText(p, got); a != b {
			t.Errorf("%s: formatting changed more than whitespace:\n%s", p, diffLines(a, b))
			continue
		}
		if again := formatString(p, got, Default()); again != got {
			unstable++
			if unstable <= 5 {
				t.Errorf("%s: formatting again changes it:\n%s", p, diffLines(got, again))
			}
		}
	}
}

// tokenText returns the kinds and text of a file's tokens, a line each.
func tokenText(path, src string) string {
	var b strings.Builder
	psi.WalkNodes(psi.Parse(path, src).RootNode(), func(n psi.NodeRef) bool {
		if n.IsLeaf() && n.NumericKind() != psi.KindWHITE_SPACE {
			text := n.Text()
			if k := n.NumericKind(); k == psi.KindKDOC || k == psi.KindBLOCK_COMMENT {
				// Their lines' indentation may change.
				var ls []string
				for _, l := range strings.Split(text, "\n") {
					ls = append(ls, strings.TrimSpace(l))
				}
				text = strings.Join(ls, "\n")
			}
			b.WriteString(n.Kind() + " " + text + "\n")
		}
		return true
	})
	return b.String()
}

// diffLines returns the first lines where a and b differ.
func diffLines(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out strings.Builder
	n := 0
	for i := 0; i < max(len(al), len(bl)) && n < 8; i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			n++
			fmt.Fprintf(&out, "  %d- %s\n  %d+ %s\n", i+1, x, i+1, y)
		}
	}
	return out.String()
}

// TestOnType types a closing brace and line breaks.
func TestOnType(t *testing.T) {
	cases := []struct {
		src, ch, want string // | is the caret, after ch
	}{
		{"fun f() {\n    if (x) {\n        y()\n        }|\n}\n", "}", "fun f() {\n    if (x) {\n        y()\n    }\n}\n"},
		{"fun f() {\n|\n}\n", "\n", "fun f() {\n    \n}\n"},
		{"fun f() {\n    foo(\n|\n}\n", "\n", "fun f() {\n    foo(\n        \n}\n"},
		{"fun f() {\n    val x=1\n|}\n", "\n", "fun f() {\n    val x = 1\n}\n"},
		{"fun f() {\n    items\n|.map { it }\n}\n", "\n", "fun f() {\n    items\n        .map { it }\n}\n"},
	}
	for _, c := range cases {
		off := strings.Index(c.src, "|")
		src := c.src[:off] + c.src[off+1:]
		got := apply(src, OnType(psi.Parse("a.kt", src), Default(), off, c.ch))
		if got != c.want {
			t.Errorf("typing %q in %q:\n got %q\nwant %q", c.ch, c.src, got, c.want)
		}
	}
}

// TestEditorConfig reads the settings of nested .editorconfig files.
func TestEditorConfig(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(root, ".editorconfig"), []byte("root = true\n[*]\nindent_size = 2\n[*.{kt,kts}]\ninsert_final_newline = false\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a", ".editorconfig"), []byte("# nested\n[b/**.kt]\nindent_style = tab\ntab_width = 8\nindent_size = tab\nij_kotlin_keep_blank_lines_in_code = 1\n"), 0o644)
	got := EditorConfig(filepath.Join(root, "a", "b", "c", "X.kt"), Default())
	want := Options{IndentSize: 8, UseTabs: true, TabWidth: 8, MaxBlankLines: 1, InsertFinalNewline: false}
	if got != want {
		t.Errorf("a/b/c/X.kt: %+v, want %+v", got, want)
	}
	got = EditorConfig(filepath.Join(root, "Y.kt"), Default())
	want = Options{IndentSize: 2, TabWidth: 2, MaxBlankLines: 2, InsertFinalNewline: false}
	if got != want {
		t.Errorf("Y.kt: %+v, want %+v", got, want)
	}
	if got := formatString("a.kt", "fun f() {\nx()\n}", got); got != "fun f() {\n  x()\n}" {
		t.Errorf("formatted with Y.kt's settings: %q", got)
	}
}
