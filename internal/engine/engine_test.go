package engine

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
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
