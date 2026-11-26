package features

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/barteo/kotlin-lsp-go/internal/project"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// TestRunTargets finds main functions and tests.
func TestRunTargets(t *testing.T) {
	cases := []struct {
		name, path, src string
		want            []string // kind class[#method] framework
	}{
		{"top-level main", "/p/app.kt", "package a.b\n\nfun main() {}\n", []string{"main a.b.AppKt"}},
		{"main with args", "/p/Main.kt", "fun main(args: Array<String>) {}\nfun main(vararg a: String) {}\n", []string{"main MainKt"}},
		{"not main", "/p/x.kt", "fun main(n: Int) {}\nfun String.main() {}\nfun <T> main() {}\nclass C { fun main() {} }\n", nil},
		{"JvmName", "/p/x.kt", "@file:JvmName(\"Tool\")\npackage p\nfun main() {}\n", []string{"main p.Tool"}},
		{"file name", "/p/my-app.2.kt", "fun main() {}\n", []string{"main My_app_2Kt"}},
		{"objects", "/p/x.kt", "package p\nobject App { @JvmStatic fun main(args: Array<String>) {} }\n" +
			"class C { companion object { @JvmStatic fun main(args: Array<String>) {} } }\n" +
			"object NotStatic { fun main(args: Array<String>) {} }\n", []string{"main p.App", "main p.C"}},
		{"junit5", "/p/T.kt", "package p\nimport org.junit.jupiter.api.Test\nimport org.junit.jupiter.params.*\n\n" +
			"class FooTest {\n    /** Doc. */\n    @Test\n    fun one() {}\n    @ParameterizedTest fun two(x: Int) {}\n    fun helper() {}\n" +
			"    @Nested inner class Inner { @Test fun three() {} }\n}\n",
			[]string{"tests p.FooTest junit5", "test p.FooTest#one junit5", "test p.FooTest#two junit5", "tests p.FooTest$Inner junit5", "test p.FooTest$Inner#three junit5"}},
		{"junit4 qualified, testng alias, kotlin.test", "/p/T.kt", "import org.testng.annotations.Test as NG\nimport kotlin.test.*\n" +
			"class A { @org.junit.Test fun a() {} }\nclass B { @NG fun b() {} }\nclass C { @Test fun c() {} }\n",
			[]string{"tests A junit4", "test A#a junit4", "tests B testng", "test B#b testng", "tests C kotlin.test", "test C#c kotlin.test"}},
		{"not tests", "/p/T.kt", "import org.junit.Test\nabstract class A { @Test fun a() {} }\ninterface I { @Test fun i() {} }\n" +
			"object O { @Test fun o() {} }\nclass D { @Other fun d() {} }\n@Test fun top() {}\n", nil},
		{"script", "/p/x.kts", "fun main() {}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFile(protocol.DocumentURI("file://"+tc.path), tc.path, tc.src, protocol.UTF16)
			var got []string
			for _, r := range RunTargets(f) {
				s := r.Kind + " " + r.Class
				if r.Method != "" {
					s += "#" + r.Method
				}
				if r.Framework != "" {
					s += " " + r.Framework
				}
				got = append(got, s)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}

	src := "import org.junit.Test\n\nclass T {\n    /** Doc. */\n    @Test\n    fun a() {}\n}\n"
	f := NewFile("file:///p/T.kt", "/p/T.kt", src, protocol.UTF16)
	var got []string
	for _, l := range CodeLenses(f) {
		got = append(got, fmt.Sprintf("%d:%d %s", l.Range.Start.Line, l.Range.Start.Character, l.Command.Title))
	}
	if want := "2:0 ▶ Run tests,4:4 ▶ Run test"; strings.Join(got, ",") != want {
		t.Errorf("lenses %q, want %q", strings.Join(got, ","), want)
	}
}

// TestPlanRun plans runs of Gradle tests and of tests outside Gradle.
func TestPlanRun(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) string {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	test := write("app/src/test/kotlin/FooTest.kt", "class FooTest\n")
	write("gradlew", "")
	ws := &project.Workspace{Root: root, Source: "gradle"}
	m := &project.Module{ID: ":app:test", Roots: []string{filepath.Join(root, "app/src/test/kotlin")}}
	plan, err := PlanRun(RunArgs{Kind: RunTest, Class: "p.FooTest$Inner", Method: "one"}, RunEnv{Path: test, Workspace: ws, Module: m})
	if err != nil {
		t.Fatal(err)
	}
	if want := `./gradlew :app:cleanTest :app:test --tests 'p.FooTest$Inner.one'`; plan.Command != want || plan.Cwd != root || plan.Name != "FooTest$Inner.one" {
		t.Errorf("got %+v, want %s in %s", plan, want, root)
	}
	other := write("lib/sub/src/jvmTest/kotlin/BarTest.kt", "class BarTest\n")
	write("lib/sub/build.gradle.kts", "")
	plan, err = PlanRun(RunArgs{Kind: RunTests, Class: "BarTest"}, RunEnv{Path: other, Workspace: ws, Module: &project.Module{ID: "file:" + other}})
	if want := "./gradlew :lib:sub:cleanJvmTest :lib:sub:jvmTest --tests BarTest"; err != nil || plan.Command != want {
		t.Errorf("a file the model doesn't know: %+v, %v; want %s", plan, err, want)
	}
	mvnWS := &project.Workspace{Root: root, Source: "maven"}
	plan, err = PlanRun(RunArgs{Kind: RunTest, Class: "a.FooTest", Method: "one"}, RunEnv{Path: test, Workspace: mvnWS, Module: &project.Module{ID: "app:test", Dir: filepath.Join(root, "app")}})
	if want := "mvn -pl app -am test '-Dtest=a.FooTest#one' -Dsurefire.failIfNoSpecifiedTests=false -DfailIfNoTests=false"; err != nil || plan.Command != want {
		t.Errorf("maven: %+v, %v; want %s", plan, err, want)
	}
	for set, task := range map[string]string{"test": "test", "testJvm": "jvmTest", "debugUnitTest": "testDebugUnitTest", "integrationTest": "integrationTest"} {
		if got := testTask(set); got != task {
			t.Errorf("testTask(%s) = %s, want %s", set, got, task)
		}
	}

	// Outside Gradle: compile with kc, then a launcher on the class path.
	jar := write("lib/junit-platform-console-standalone-1.11.0.jar", "")
	write("lib/junit-4.13.2.jar", "")
	ws.Source = "kcls.json"
	m.ClassPath = []string{jar, filepath.Join(root, "lib/junit-4.13.2.jar")}
	dir := filepath.Join(root, "run")
	plan, err = PlanRun(RunArgs{Kind: RunTests, Class: "FooTest", Framework: "junit5"}, RunEnv{Path: test, Workspace: ws, Module: m, KC: "/bin/kc", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.Command, "/bin/kc @"+filepath.Join(dir, "kc.args")+" && ") || !strings.Contains(plan.Command, "java @"+filepath.Join(dir, "java.args")) {
		t.Errorf("command %s", plan.Command)
	}
	java, _ := os.ReadFile(filepath.Join(dir, "java.args"))
	if !strings.Contains(string(java), "-jar\n"+jar+"\nexecute\n--class-path\n") || !strings.HasSuffix(string(java), "\n--disable-banner\n--select-class=FooTest\n") {
		t.Errorf("java.args:\n%s", java)
	}
	plan, err = PlanRun(RunArgs{Kind: RunTest, Class: "FooTest", Method: "a", Framework: "junit4"}, RunEnv{Path: test, Workspace: ws, Module: m, KC: "/bin/kc", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	java, _ = os.ReadFile(filepath.Join(dir, "java.args"))
	if !strings.HasSuffix(string(java), "org.junit.runner.JUnitCore\nFooTest\n") {
		t.Errorf("java.args:\n%s", java)
	}
	m.ClassPath = nil
	if _, err := PlanRun(RunArgs{Kind: RunTests, Class: "FooTest"}, RunEnv{Path: test, Workspace: ws, Module: m, KC: "/bin/kc", Dir: dir}); err == nil || !strings.Contains(err.Error(), "launcher") {
		t.Errorf("no launcher: %v", err)
	}
	if _, err := PlanRun(RunArgs{Kind: RunMain, Class: "MainKt"}, RunEnv{Path: test, Workspace: ws, Module: m, Dir: dir}); err == nil || !strings.Contains(err.Error(), "kc isn't installed") {
		t.Errorf("no kc: %v", err)
	}
	if got := shellJoin([]string{"a", "b c", "it's", "x$y"}); got != `a 'b c' 'it'\''s' 'x$y'` {
		t.Errorf("shellJoin: %s", got)
	}
}

// TestRunMain compiles and runs a main function as a run lens does, with
// kc built from the kotlin-compiler-go module go.mod requires, and the JDK.
func TestRunMain(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds kc")
	}
	jdk := project.FindJDK()
	if jdk == "" {
		t.Skip("no JDK")
	}
	dir := t.TempDir()
	kc := filepath.Join(dir, "kc")
	if out, err := exec.Command("go", "build", "-o", kc, "github.com/barteo/kotlin-compiler-go/cmd/kc").CombinedOutput(); err != nil {
		t.Fatalf("building kc: %v\n%s", err, out)
	}
	root := filepath.Join(dir, "my project")
	src := filepath.Join(root, "src/main/kotlin/app/Main.kt")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.MkdirAll(filepath.Join(root, "src/main/resources"), 0o755)
	os.MkdirAll(filepath.Join(root, "src/main/java/app"), 0o755)
	os.WriteFile(src, []byte("package app\n\nfun main(args: Array<String>) {\n    val r = object {}.javaClass.getResource(\"/greeting.txt\")!!.readText().trim()\n    println(r + \" \" + Util.twice(21))\n}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "src/main/java/app/Util.java"), []byte("package app;\npublic class Util { public static int twice(int x) { return 2 * x; } }\n"), 0o644)
	os.WriteFile(filepath.Join(root, "src/main/resources/greeting.txt"), []byte("hello\n"), 0o644)
	m := &project.Module{ID: "root", ModuleName: "main", Roots: []string{filepath.Join(root, "src/main/kotlin"), filepath.Join(root, "src/main/java")},
		ClassPath: project.Folder(root).Modules[0].ClassPath, JDKHome: jdk}
	ws := &project.Workspace{Root: root, Source: "kcls.json", Modules: []*project.Module{m}}
	f := NewFile(protocol.DocumentURI("file://"+src), src, mustRead(t, src), protocol.UTF16)
	targets := RunTargets(f)
	if len(targets) != 1 {
		t.Fatalf("targets %v", targets)
	}
	plan, err := PlanRun(targets[0].RunArgs, RunEnv{Path: src, Workspace: ws, Module: m, KC: kc, Dir: filepath.Join(dir, "run")})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", plan.Command)
	cmd.Dir = plan.Cwd
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", plan.Command, err, out.String())
	}
	if got := out.String(); got != "hello 42\n" {
		t.Errorf("output %q", got)
	}
}

func mustRead(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
