package engine

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-lsp-go/internal/features"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// diskFiles reads files from disk, parsing each once per modification.
type diskFiles struct {
	mu    sync.Mutex
	files map[string]*features.File
	stamp map[string]time.Time
}

func (d *diskFiles) File(path string) (*features.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.files == nil {
		d.files, d.stamp = map[string]*features.File{}, map[string]time.Time{}
	}
	if f := d.files[path]; f != nil && d.stamp[path].Equal(fi.ModTime()) {
		return f, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := features.NewFile(protocol.DocumentURI("file://"+path), path, string(b), protocol.UTF16)
	d.files[path], d.stamp[path] = f, fi.ModTime()
	return f, nil
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func needStdlib(t *testing.T) string {
	t.Helper()
	jar, err := analysis.FindStdlib()
	if err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	return jar
}

func newEngine(t *testing.T, root string) *Engine {
	t.Helper()
	e := New(Options{Files: &diskFiles{}, CacheDir: t.TempDir(), NoImport: true, Logf: t.Logf, SourcesTTL: time.Hour})
	t.Cleanup(e.Close)
	e.AddRoot(root)
	return e
}

// pos returns the position of the nth occurrence of sub in f.
func pos(t *testing.T, f *features.File, sub string) protocol.Position {
	t.Helper()
	i := strings.Index(f.Mapper.Text(), sub)
	if i < 0 {
		t.Fatalf("%q not in %s", sub, f.Snapshot.Path())
	}
	return f.Mapper.Position(i)
}

func TestModules(t *testing.T) {
	needStdlib(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "app", "roots": ["app"], "dependsOn": ["lib"], "noJdk": true}
]}`,
		"lib/L.kt": "package lib\n\nclass Greeter(val name: String) { fun greet() = \"hi \" + name }\n\ninternal fun secret() = 1\n",
		"app/A.kt": "package app\n\nimport lib.Greeter\n\nfun main() { println(Greeter(\"x\").greet()) }\n",
		"app/B.kt": "package app\n\nfun broken(): Int = \"s\"\n",
	})
	e := newEngine(t, root)
	ctx := context.Background()
	a, err := e.Analyze(ctx, filepath.Join(root, "app/A.kt"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Module != "app" {
		t.Fatalf("module %s", a.Module)
	}
	af := a.File(filepath.Join(root, "app/A.kt"))
	if af == nil || a.File(filepath.Join(root, "lib/L.kt")) == nil {
		t.Fatal("app's analysis lacks A.kt or lib's L.kt")
	}
	if a.Own(filepath.Join(root, "lib/L.kt")) || !a.Own(filepath.Join(root, "app/A.kt")) {
		t.Error("Own: lib's file is app's own, or A.kt isn't")
	}
	links := features.Definition(ctx, a, af, pos(t, af, "greet()"))
	if len(links) != 1 || !strings.HasSuffix(string(links[0].TargetURI), "lib/L.kt") {
		t.Errorf("definition of greet: %+v", links)
	}
	// B.kt's error is app's; lib's analysis has none.
	bf := a.File(filepath.Join(root, "app/B.kt"))
	if d := features.Diagnostics(a, bf); len(d) != 1 {
		t.Errorf("B.kt diagnostics: %+v", d)
	}
	la, err := e.Analyze(ctx, filepath.Join(root, "lib/L.kt"))
	if err != nil {
		t.Fatal(err)
	}
	if la.Module != "lib" || la.File(filepath.Join(root, "app/A.kt")) != nil {
		t.Errorf("lib's analysis: module %s, has app's files", la.Module)
	}

	// The same files: the same analysis, without analyzing again.
	before, _ := e.Stats(filepath.Join(root, "app/A.kt"))
	if a2, err := e.Analyze(ctx, filepath.Join(root, "app/B.kt")); err != nil || a2 != a {
		t.Errorf("second analysis of unchanged files: %v, same %v", err, a2 == a)
	}
	after, _ := e.Stats(filepath.Join(root, "app/A.kt"))
	if after.Analyses != before.Analyses {
		t.Errorf("analyses %d → %d without a change", before.Analyses, after.Analyses)
	}

	// An edit on disk is seen: B.kt fixed.
	time.Sleep(10 * time.Millisecond)
	write(t, root, map[string]string{"app/B.kt": "package app\n\nfun broken(): Int = 1\n"})
	a3, err := e.Analyze(ctx, filepath.Join(root, "app/A.kt"))
	if err != nil {
		t.Fatal(err)
	}
	if d := features.Diagnostics(a3, a3.File(filepath.Join(root, "app/B.kt"))); len(d) != 0 {
		t.Errorf("B.kt after the fix: %+v", d)
	}

	// A new file, after a creation event.
	write(t, root, map[string]string{"app/C.kt": "package app\n\nfun c() = Greeter(\"y\")\n"})
	e.Changed(filepath.Join(root, "app/C.kt"), true, false)
	a4, err := e.Analyze(ctx, filepath.Join(root, "app/A.kt"))
	if err != nil {
		t.Fatal(err)
	}
	cf := a4.File(filepath.Join(root, "app/C.kt"))
	if cf == nil {
		t.Fatal("new C.kt not analyzed")
	}
	if d := features.Diagnostics(a4, cf); len(d) != 1 || !strings.Contains(d[0].Message, "Greeter") {
		t.Errorf("C.kt (no import of Greeter): %+v", d)
	}

	// A file outside every module is a module of its own.
	write(t, filepath.Dir(root), map[string]string{"loose.kt": "fun loose() = listOf(1)\n"})
	loose := filepath.Join(filepath.Dir(root), "loose.kt")
	defer os.Remove(loose)
	if la, err := e.Analyze(ctx, loose); err != nil || !strings.HasPrefix(la.Module, "file:") {
		t.Errorf("loose file: %v %+v", err, la)
	}
}

// TestSharedRun checks that concurrent requests for the same files share
// one analysis.
func TestSharedRun(t *testing.T) {
	needStdlib(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, map[string]string{"a.kt": "fun a() = listOf(1).map { it * 2 }\n", "b.kt": "fun b() = a()\n"})
	e := newEngine(t, root)
	var wg sync.WaitGroup
	results := make([]*features.Analysis, 8)
	for i := range results {
		wg.Go(func() {
			a, err := e.Analyze(context.Background(), filepath.Join(root, []string{"a.kt", "b.kt"}[i%2]))
			if err != nil {
				t.Error(err)
			}
			results[i] = a
		})
	}
	wg.Wait()
	for _, r := range results[1:] {
		if r != results[0] {
			t.Fatal("requests got different analyses")
		}
	}
	if s, _ := e.Stats(filepath.Join(root, "a.kt")); s.Analyses != 1 {
		t.Errorf("%d analyses, want 1", s.Analyses)
	}
	// A cancelled request returns at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	write(t, root, map[string]string{"b.kt": "fun b() = a() + 1\n"})
	time.Sleep(5 * time.Millisecond)
	if _, err := e.Analyze(ctx, filepath.Join(root, "a.kt")); err == nil {
		t.Error("cancelled request: no error")
	}
}

func makeJar(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lib-sources.jar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range files {
		w, _ := z.Create(name)
		w.Write([]byte(text))
	}
	z.Close()
	f.Close()
	return path
}

func TestLibrarySources(t *testing.T) {
	jar := makeJar(t, map[string]string{
		"commonMain/kotlin/text/Strings.kt": "/* license */\n@file:JvmName(\"StringsKt\")\npackage demo.text\n\n/** Shouts. */\nfun String.shout(): String = uppercase()\nfun pad(n: Int) = \"\"\nfun pad(n: Int, c: Char) = \"\"\n",
		"jvmMain/kotlin/Box.kt":             "package demo\n\nclass Box(val size: Int) {\n    class Inner { fun open() {} }\n    fun fill(x: Int) {}\n}\n",
		"java.base/demo/j/Thing.java":       "package demo.j;\n\n/**\n * A {@code Thing}.\n */\n@Deprecated\npublic class Thing {\n    public int getCount() { return 0; }\n}\n",
	})
	l := newLibrarySources(filepath.Join(t.TempDir(), "sources"), t.Logf)
	set := l.forJars([]string{jar}, &diskFiles{})
	ctx := context.Background()
	for _, c := range []struct {
		d    features.LibraryDecl
		want string // the text of the found name
	}{
		{features.LibraryDecl{Kind: "function", QualifiedName: "demo.text.shout", Parameters: 0}, "shout"},
		{features.LibraryDecl{Kind: "function", QualifiedName: "demo.text.pad", Parameters: 2}, "pad(n: Int, c"},
		{features.LibraryDecl{Kind: "class", Class: "demo/Box"}, "Box"},
		{features.LibraryDecl{Kind: "class", Class: "demo/Box$Inner"}, "Inner"},
		{features.LibraryDecl{Kind: "function", QualifiedName: "demo.Box.Inner.open", Owner: "demo/Box$Inner", Parameters: 0}, "open"},
		{features.LibraryDecl{Kind: "property", QualifiedName: "demo.Box.size", Owner: "demo/Box"}, "size"},
		{features.LibraryDecl{Kind: "constructor", QualifiedName: "demo.Box.<init>", Owner: "demo/Box"}, "Box"},
		{features.LibraryDecl{Kind: "class", Class: "demo/j/Thing"}, "Thing"},
		{features.LibraryDecl{Kind: "property", QualifiedName: "demo.j.Thing.count", Owner: "demo/j/Thing"}, "getCount"},
	} {
		lt, ok := set.Find(ctx, c.d)
		if !ok {
			t.Errorf("%+v: not found", c.d)
			continue
		}
		text := lt.File.Mapper.Text()
		start := lt.Start
		if lt.Node.Valid() {
			start = lt.Node.NameIdentifier().Range().Start
		}
		if !strings.HasPrefix(text[start:], c.want) {
			t.Errorf("%+v: found %q", c.d, text[start:min(start+20, len(text))])
		}
		if !e2(lt.File.Snapshot.Path(), l.dir) {
			t.Errorf("%+v: file %s not extracted into %s", c.d, lt.File.Snapshot.Path(), l.dir)
		}
	}
	if lt, _ := set.Find(ctx, features.LibraryDecl{Kind: "class", Class: "demo/j/Thing"}); !strings.Contains(lt.Doc, "{@code Thing}") {
		t.Errorf("Thing's Javadoc: %q", lt.Doc)
	}
	if _, ok := set.Find(ctx, features.LibraryDecl{Kind: "class", Class: "demo/Missing"}); ok {
		t.Error("found a missing class")
	}
}

func e2(path, dir string) bool { return within(path, dir) }

// TestStdlibSources finds println in the standard library's sources jar
// in Gradle's cache, if there is one.
func TestStdlibSources(t *testing.T) {
	home, _ := os.UserHomeDir()
	jars, _ := filepath.Glob(filepath.Join(home, ".gradle/caches/modules-2/files-2.1/org.jetbrains.kotlin/kotlin-stdlib/*/*/kotlin-stdlib-*-sources.jar"))
	if len(jars) == 0 {
		t.Skip("no kotlin-stdlib sources jar in Gradle's cache")
	}
	l := newLibrarySources(filepath.Join(t.TempDir(), "sources"), t.Logf)
	set := l.forJars(jars[len(jars)-1:], &diskFiles{})
	lt, ok := set.Find(context.Background(), features.LibraryDecl{Kind: "function", QualifiedName: "kotlin.io.println", Parameters: 1})
	if !ok || !lt.Node.Valid() || lt.Node.Name() != "println" {
		t.Fatalf("println: %+v %v", lt, ok)
	}
	if !strings.HasSuffix(lt.File.Snapshot.Path(), "Console.kt") {
		t.Errorf("println in %s", lt.File.Snapshot.Path())
	}
}

// TestRoots analyzes a workspace folder with the sources of another's
// module whose classes its class path has, and follows that folder's
// model changes.
func TestRoots(t *testing.T) {
	needStdlib(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(base, "lib"), filepath.Join(base, "app")
	write(t, lib, map[string]string{
		"kcls.json": `{"modules": [{"name": "lib", "roots": ["src"], "output": "out", "noJdk": true}]}`,
		"src/L.kt":  "package lib\n\nclass Greeter { fun greet() = 1 }\n",
	})
	write(t, app, map[string]string{
		"kcls.json": `{"modules": [{"name": "app", "roots": ["src"], "classPath": ["../lib/out"], "noJdk": true}]}`,
		"src/A.kt":  "package app\n\nimport lib.Greeter\n\nfun main() { Greeter().greet() }\n",
	})
	os.MkdirAll(filepath.Join(lib, "out"), 0o755)
	e := newEngine(t, app)
	ctx := context.Background()
	path := filepath.Join(app, "src/A.kt")
	a, err := e.Analyze(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if d := features.Diagnostics(a, a.File(path)); len(d) == 0 {
		t.Fatal("Greeter resolves without lib's folder")
	}
	e.AddRoot(lib)
	a, err = e.Analyze(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	af := a.File(path)
	if d := features.Diagnostics(a, af); len(d) != 0 {
		t.Errorf("with lib's folder: %+v", d)
	}
	links := features.Definition(ctx, a, af, pos(t, af, "greet()"))
	if len(links) != 1 || !strings.HasSuffix(string(links[0].TargetURI), "lib/src/L.kt") {
		t.Errorf("definition of greet: %+v", links)
	}
	// References to lib's Greeter are searched in app too.
	scopes, err := e.Search(ctx, []string{"Greeter"}, filepath.Join(lib, "src/L.kt"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range scopes {
		for _, p := range s.Files {
			found = found || p.Snapshot.Path() == path
		}
	}
	if !found {
		t.Errorf("search from lib doesn't reach app: %+v", scopes)
	}
	e.RemoveRoot(lib)
	a, err = e.Analyze(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if d := features.Diagnostics(a, a.File(path)); len(d) == 0 {
		t.Error("Greeter still resolves after lib's folder is removed")
	}
}

// TestIncremental checks that an edit after the module's first analysis
// updates its session incrementally, with the diagnostics an analysis of
// all the files has, and that a declaration edit reaches the files that
// use the declaration.
func TestIncremental(t *testing.T) {
	needStdlib(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	files := map[string]string{
		"kcls.json": `{"modules": [{"name": "m", "roots": ["src"], "noJdk": true}]}`,
		"src/A.kt":  "package m\n\nfun twice(n: Int): Int {\n    return n * 2\n}\n",
		"src/B.kt":  "package m\n\nfun useTwice(): Int = twice(1)\n",
		"src/C.kt":  "package m\n\nfun alone(): Int {\n    val unused = 1\n    return 3\n}\n",
	}
	// Files a declaration edit of A.kt doesn't affect, so that it doesn't
	// affect most of the module's, which an update checks whole.
	for i := range 4 {
		files[fmt.Sprintf("src/F%d.kt", i)] = fmt.Sprintf("package m\n\nfun filler%d(): Int = %d\n", i, i)
	}
	write(t, root, files)
	e := newEngine(t, root)
	ctx := context.Background()
	a := filepath.Join(root, "src/A.kt")
	if _, err := e.Analyze(ctx, a); err != nil {
		t.Fatal(err)
	}
	// diagnostics returns each file's diagnostics, as text.
	diagnostics := func(an *features.Analysis) map[string]string {
		out := map[string]string{}
		for _, f := range an.Files() {
			var lines []string
			for _, d := range features.Diagnostics(an, f) {
				lines = append(lines, fmt.Sprintf("%d:%d %s", d.Range.Start.Line, d.Range.Start.Character, d.Message))
			}
			out[filepath.Base(f.Snapshot.Path())] = strings.Join(lines, "; ")
		}
		return out
	}
	// whole returns the diagnostics of an analysis of all the files.
	whole := func() map[string]string {
		incremental = false
		defer func() { incremental = true }()
		w := newEngine(t, root)
		an, err := w.Analyze(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return diagnostics(an)
	}
	steps := []struct {
		name, file, text string
		want             string // a diagnostic of B.kt, if any
	}{
		{"body edit", "src/C.kt", "package m\n\nfun alone(): Int {\n    val unused = 1\n    return undefinedName\n}\n", ""},
		{"declaration edit", "src/A.kt", "package m\n\nfun twice(n: Int, k: Int): Int {\n    return n * k\n}\n", "no function 'twice' takes 1 arguments"},
		{"declaration fixed", "src/A.kt", files["src/A.kt"], ""},
	}
	for i, st := range steps {
		time.Sleep(10 * time.Millisecond)
		write(t, root, map[string]string{st.file: st.text})
		an, err := e.Analyze(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		stats, _ := e.Stats(a)
		if stats.Incremental != i+1 {
			t.Errorf("%s: %d incremental updates, want %d", st.name, stats.Incremental, i+1)
		}
		got, want := diagnostics(an), whole()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: diagnostics\n got  %v\n want %v", st.name, got, want)
		}
		if b := got["B.kt"]; (st.want == "") != (b == "") || !strings.Contains(b, st.want) {
			t.Errorf("%s: B.kt's diagnostics %q, want %q", st.name, b, st.want)
		}
	}
}

// openFiles is diskFiles with some files the client opened.
type openFiles struct {
	diskFiles
	open map[string]bool
}

func (o *openFiles) OpenText(path string) (string, bool) {
	if !o.open[path] {
		return "", false
	}
	b, err := os.ReadFile(path)
	return string(b), err == nil
}

// TestSearchReleases checks that a search keeps the analyses of only the
// KeepSearched first modules it analyzed and of those with an open
// document, and that a released module is analyzed again on demand.
func TestSearchReleases(t *testing.T) {
	needStdlib(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	files := map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "a", "roots": ["a"], "dependsOn": ["lib"], "noJdk": true},
  {"name": "b", "roots": ["b"], "dependsOn": ["lib"], "noJdk": true},
  {"name": "c", "roots": ["c"], "dependsOn": ["lib"], "noJdk": true}
]}`,
		"lib/L.kt": "package lib\n\nclass Greeter { fun greet() = 1 }\n",
	}
	for _, m := range []string{"a", "b", "c"} {
		files[m+"/U.kt"] = "package " + m + "\n\nimport lib.Greeter\n\nfun use() = Greeter().greet()\n"
	}
	write(t, root, files)
	fs := &openFiles{open: map[string]bool{filepath.Join(root, "c/U.kt"): true}}
	e := New(Options{Files: fs, CacheDir: t.TempDir(), NoImport: true, Logf: t.Logf, SourcesTTL: time.Hour, KeepSearched: 1})
	t.Cleanup(e.Close)
	e.AddRoot(root)
	ctx := context.Background()
	mod := func(name string) *module {
		m, err := e.module(ctx, filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	search := func() {
		t.Helper()
		scopes, err := e.Search(ctx, []string{"Greeter"}, filepath.Join(root, "lib/L.kt"))
		if err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, s := range scopes {
			for _, f := range s.Files {
				if d := features.Diagnostics(s.Analysis, f); len(d) != 0 {
					t.Errorf("%s: %+v", f.Snapshot.Path(), d)
				}
				found = append(found, filepath.Base(filepath.Dir(f.Snapshot.Path())))
			}
		}
		slices.Sort(found)
		if !slices.Equal(found, []string{"a", "b", "c", "lib"}) {
			t.Errorf("found in %v", found)
		}
	}
	search()
	for name, want := range map[string]bool{"lib/L.kt": true, "a/U.kt": false, "b/U.kt": false, "c/U.kt": true} {
		if got := mod(name).hasLast(); got != want {
			t.Errorf("after a search, %s's module keeps its analysis: %v, want %v", name, got, want)
		}
	}
	// A released module is analyzed again, with a new session.
	a, err := e.Analyze(ctx, filepath.Join(root, "a/U.kt"))
	if err != nil {
		t.Fatal(err)
	}
	if d := features.Diagnostics(a, a.File(filepath.Join(root, "a/U.kt"))); len(d) != 0 {
		t.Errorf("a after release: %+v", d)
	}
	search()
}

// releasingFiles is openFiles that forgets the files it read when
// released, as the server's document store does.
type releasingFiles struct {
	openFiles
}

func (r *releasingFiles) Release(keep map[*features.File]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for p, f := range r.files {
		if !r.open[p] && !keep[f] {
			delete(r.files, p)
			delete(r.stamp, p)
		}
	}
}

// TestSearchReleasesFiles checks that the files of the modules a search
// released are forgotten, and that the modules that keep their analysis
// keep their files, so that a request on them reuses the analysis.
func TestSearchReleasesFiles(t *testing.T) {
	needStdlib(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	files := map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "a", "roots": ["a"], "dependsOn": ["lib"], "noJdk": true},
  {"name": "b", "roots": ["b"], "dependsOn": ["lib"], "noJdk": true},
  {"name": "c", "roots": ["c"], "dependsOn": ["lib"], "noJdk": true}
]}`,
		"lib/L.kt": "package lib\n\nclass Greeter { fun greet() = 1 }\n",
	}
	for _, m := range []string{"a", "b", "c"} {
		files[m+"/U.kt"] = "package " + m + "\n\nimport lib.Greeter\n\nfun use() = Greeter().greet()\n"
	}
	write(t, root, files)
	path := func(name string) string { return filepath.Join(root, name) }
	fs := &releasingFiles{openFiles{open: map[string]bool{path("c/U.kt"): true}}}
	e := New(Options{Files: fs, CacheDir: t.TempDir(), NoImport: true, Logf: t.Logf, SourcesTTL: time.Hour, KeepSearched: 1})
	t.Cleanup(e.Close)
	e.AddRoot(root)
	ctx := context.Background()
	if _, err := e.Search(ctx, []string{"Greeter"}, path("lib/L.kt")); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]*features.File {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		out := map[string]*features.File{}
		for p, f := range fs.files {
			rel, _ := filepath.Rel(root, p)
			out[rel] = f
		}
		return out
	}
	before := read()
	e.releaseFiles()
	after := read()
	for _, name := range []string{"a/U.kt", "b/U.kt", "lib/L.kt", "c/U.kt"} {
		if before[name] == nil {
			t.Fatalf("%s wasn't read by the search", name)
		}
	}
	for name, want := range map[string]bool{"lib/L.kt": true, "c/U.kt": true, "a/U.kt": false, "b/U.kt": false} {
		if got := after[name] != nil; got != want {
			t.Errorf("after releasing, %s is kept: %v, want %v", name, got, want)
		} else if got && after[name] != before[name] {
			t.Errorf("after releasing, %s was parsed again", name)
		}
	}
	// The modules that keep their analysis reuse it.
	for _, name := range []string{"lib/L.kt", "c/U.kt"} {
		st, err := e.Stats(path(name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Analyze(ctx, path(name)); err != nil {
			t.Fatal(err)
		}
		st2, _ := e.Stats(path(name))
		if st2.Analyses != st.Analyses {
			t.Errorf("%s analyzed again after releasing files: %d analyses, then %d", name, st.Analyses, st2.Analyses)
		}
	}
	// A released module is analyzed again from its files read again.
	a, err := e.Analyze(ctx, path("a/U.kt"))
	if err != nil {
		t.Fatal(err)
	}
	if d := features.Diagnostics(a, a.File(path("a/U.kt"))); len(d) != 0 {
		t.Errorf("a after releasing: %+v", d)
	}
}

// TestWorkspaceSymbolsAfterRelease checks that workspace symbols survive
// the Files forgetting their parses and follow a file's change.
func TestWorkspaceSymbolsAfterRelease(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, root, map[string]string{"A.kt": "class Alpha\n"})
	fs := &releasingFiles{}
	e := New(Options{Files: fs, CacheDir: t.TempDir(), NoImport: true, Logf: t.Logf, SourcesTTL: time.Hour})
	t.Cleanup(e.Close)
	e.AddRoot(root)
	ctx := context.Background()
	names := func(q string) []string {
		t.Helper()
		syms, err := e.WorkspaceSymbols(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, s := range syms {
			out = append(out, s.Name)
		}
		return out
	}
	if got := names("Alpha"); !slices.Equal(got, []string{"Alpha"}) {
		t.Fatalf("symbols: %v", got)
	}
	fs.Release(nil)
	if got := names("Alpha"); !slices.Equal(got, []string{"Alpha"}) {
		t.Errorf("symbols after release: %v", got)
	}
	fs.mu.Lock()
	n := len(fs.files)
	fs.mu.Unlock()
	if n != 0 {
		t.Errorf("an unchanged file was parsed again for its symbols")
	}
	write(t, root, map[string]string{"A.kt": "class Beta\n"})
	if got := names("Beta"); !slices.Equal(got, []string{"Beta"}) {
		t.Errorf("symbols after a change: %v", got)
	}
}
