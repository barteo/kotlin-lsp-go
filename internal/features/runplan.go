package features

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/project"
)

// RunEnv is what planning a run needs besides the RunArgs.
type RunEnv struct {
	// Path is the file the target is in.
	Path string
	// Workspace is the file's workspace, nil for a file in none, and
	// Module its module. Deps are the modules whose sources Module uses,
	// all of them.
	Workspace *project.Workspace
	Module    *project.Module
	Deps      []*project.Module
	// KC is the kc binary that compiles the sources; empty if there is
	// none.
	KC string
	// Dir is the directory for the classes and argument files. PlanRun
	// empties it.
	Dir string
}

// RunPlan is how to run a RunArgs: a POSIX shell command, run in Cwd.
type RunPlan struct {
	// Name names the run, as a terminal's title: "MainKt", "FooTest.bar".
	Name    string `json:"name"`
	Cwd     string `json:"cwd"`
	Command string `json:"command"`
}

// PlanRun returns how to run a main function or tests. Tests of a Gradle
// build run with its test task, filtered to the class or function.
// Anything else compiles with kc, the module's sources and those of the
// modules it depends on together, and runs on the JVM: a main function
// with java, tests with the JUnit Platform's console launcher, JUnit 4's
// JUnitCore or TestNG, whichever is on the module's class path. The
// argument files go in env.Dir, which keeps the class path off the
// command line.
func PlanRun(a RunArgs, env RunEnv) (*RunPlan, error) {
	m := env.Module
	if m == nil {
		return nil, errors.New("the file is in no module")
	}
	plan := &RunPlan{Name: simpleClassName(a.Class), Cwd: filepath.Dir(env.Path)}
	if a.Kind == RunTest {
		plan.Name += "." + a.Method
	}
	if env.Workspace != nil {
		plan.Cwd = env.Workspace.Root
	}
	switch a.Kind {
	case RunMain, RunTest, RunTests:
	default:
		return nil, fmt.Errorf("unknown run kind %q", a.Kind)
	}
	if a.Kind != RunMain && env.Workspace != nil {
		switch env.Workspace.Source {
		case "gradle":
			plan.Command = gradleTests(env.Workspace.Root, gradleModule(env.Workspace.Root, env.Path, m.ID), a)
			return plan, nil
		case "maven":
			plan.Command = mavenTests(env.Workspace.Root, m.Dir, a)
			return plan, nil
		}
	}
	if a.Kind == RunMain && m.Dir != "" {
		plan.Cwd = m.Dir
	}
	if m.NoJDK {
		return nil, errors.New("kcls runs JVM code only: run Android code with Gradle")
	}
	if env.KC == "" {
		return nil, errors.New("kc isn't installed: kcls compiles the code to run with kc, which it looks for next to itself and on PATH")
	}

	var kts, javas []string
	seen := map[string]bool{}
	for _, mod := range append([]*project.Module{m}, env.Deps...) {
		for _, p := range mod.Sources() {
			if seen[p] {
				continue
			}
			seen[p] = true
			switch filepath.Ext(p) {
			case ".kt":
				kts = append(kts, p)
			case ".java":
				javas = append(javas, p)
			}
		}
	}
	if !slices.Contains(kts, env.Path) && strings.HasSuffix(env.Path, ".kt") {
		kts = append(kts, env.Path)
	}
	jdk := m.JDKHome
	if jdk == "" {
		jdk = project.FindJDK()
	}
	tool := func(name string) string {
		if jdk != "" {
			if p := filepath.Join(jdk, "bin", name); exists(p) {
				return p
			}
		}
		return name
	}
	cp, friends := runClassPath(m, env.Deps)
	classes := filepath.Join(env.Dir, "classes")
	if err := os.RemoveAll(env.Dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(classes, 0o755); err != nil {
		return nil, err
	}

	var steps [][]string
	name := m.ModuleName
	if name == "" {
		name = "main"
	}
	kc := []string{"-d", classes, "-nowarn", "-module-name", name}
	if len(cp) > 0 {
		kc = append(kc, "-cp", strings.Join(cp, string(os.PathListSeparator)))
	}
	if len(friends) > 0 {
		kc = append(kc, "-friend-paths", strings.Join(friends, string(os.PathListSeparator)))
	}
	if m.JVMTarget != "" {
		kc = append(kc, "-jvm-target", m.JVMTarget)
	}
	if jdk != "" {
		kc = append(kc, "-jdk-home", jdk)
	}
	if m.MultiPlatform {
		kc = append(kc, "-Xmulti-platform")
	}
	for _, p := range m.Plugins {
		switch p {
		case "serialization", "parcelize", "compose", "metro":
			kc = append(kc, "-"+p)
		}
	}
	for _, o := range m.OptIns {
		kc = append(kc, "-opt-in", o)
	}
	kc = append(append(kc, kts...), javas...)
	file, err := writeArgFile(env.Dir, "kc.args", kc)
	if err != nil {
		return nil, err
	}
	steps = append(steps, []string{env.KC, "@" + file})
	if len(javas) > 0 {
		javac := append([]string{"-nowarn", "-d", classes, "-cp", strings.Join(append([]string{classes}, cp...), string(os.PathListSeparator))}, javas...)
		file, err := writeArgFile(env.Dir, "javac.args", javac)
		if err != nil {
			return nil, err
		}
		steps = append(steps, []string{tool("javac"), "@" + file})
	}

	runCP := append(append([]string{classes}, resourceDirs(append([]*project.Module{m}, env.Deps...))...), cp...)
	var java []string
	if a.Kind == RunMain {
		java = []string{"-cp", strings.Join(runCP, string(os.PathListSeparator)), a.Class}
	} else {
		java, err = testLauncher(a, runCP, cp)
		if err != nil {
			return nil, err
		}
	}
	file, err = writeArgFile(env.Dir, "java.args", java)
	if err != nil {
		return nil, err
	}
	steps = append(steps, []string{tool("java"), "@" + file})
	cmds := make([]string, len(steps))
	for i, s := range steps {
		cmds[i] = shellJoin(s)
	}
	plan.Command = strings.Join(cmds, " && ")
	return plan, nil
}

// gradleTests returns the command that runs tests with a Gradle build's
// test task of the module, cleaning its results first so that it runs
// again.
func gradleTests(root, id string, a RunArgs) string {
	proj, set := "", id
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		proj, set = id[:i], id[i+1:]
	}
	task := testTask(set)
	gradle := "gradle"
	if exists(filepath.Join(root, "gradlew")) {
		gradle = "./gradlew"
	}
	filter := a.Class
	if a.Kind == RunTest {
		filter += "." + a.Method
	}
	return shellJoin([]string{gradle, proj + ":clean" + upperFirst(task), proj + ":" + task, "--tests", filter})
}

// mavenTests returns the command that runs tests with Maven's Surefire,
// in the module at dir and the modules it depends on, filtered to the
// class or function.
func mavenTests(root, dir string, a RunArgs) string {
	mvn := "mvn"
	if exists(filepath.Join(root, "mvnw")) {
		mvn = "./mvnw"
	}
	args := []string{mvn}
	if rel, err := filepath.Rel(root, dir); err == nil && dir != "" && rel != "." {
		args = append(args, "-pl", filepath.ToSlash(rel), "-am")
	}
	filter := a.Class
	if a.Kind == RunTest {
		filter += "#" + a.Method
	}
	return shellJoin(append(args, "test", "-Dtest="+filter, "-Dsurefire.failIfNoSpecifiedTests=false", "-DfailIfNoTests=false"))
}

// gradleModule returns the Gradle module of the file at path, as
// ":app:test": id if it is one, else as the file's directories say, for a
// file the build's model doesn't know yet: the project of the nearest
// build file, the source set of src/<set>/.
func gradleModule(root, path, id string) string {
	if strings.HasPrefix(id, ":") {
		return id
	}
	proj := ""
	for dir := filepath.Dir(path); within(dir, root); dir = filepath.Dir(dir) {
		if exists(filepath.Join(dir, "build.gradle.kts")) || exists(filepath.Join(dir, "build.gradle")) {
			if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
				proj = ":" + strings.ReplaceAll(filepath.ToSlash(rel), "/", ":")
			}
			break
		}
		if dir == root {
			break
		}
	}
	set := "test"
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := len(parts) - 2; i > 0; i-- {
		if parts[i-1] == "src" {
			set = parts[i]
			break
		}
	}
	return proj + ":" + set
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// testTask returns the Gradle task that runs the tests of a source set:
// test's test, KMP's jvmTest (compileTestKotlinJvm's testJvm), Android's
// testDebugUnitTest (debugUnitTest).
func testTask(set string) string {
	switch {
	case set == "test":
		return "test"
	case strings.HasPrefix(set, "test") && len(set) > 4 && unicode.IsUpper(rune(set[4])):
		return lowerFirst(set[4:]) + "Test"
	case strings.HasSuffix(set, "UnitTest"):
		return "test" + upperFirst(set)
	}
	return set
}

// testLauncher returns the java arguments that run the tests of a with
// a launcher on the class path cp.
func testLauncher(a RunArgs, runCP, cp []string) ([]string, error) {
	jar := func(prefix string) (string, string) {
		for _, e := range cp {
			base := filepath.Base(e)
			if v, ok := strings.CutPrefix(base, prefix); ok && strings.HasSuffix(v, ".jar") && v != "" && v[0] >= '0' && v[0] <= '9' {
				return e, strings.TrimSuffix(v, ".jar")
			}
		}
		return "", ""
	}
	joined := strings.Join(runCP, string(os.PathListSeparator))
	platform := func(launcher []string, version string) []string {
		args := launcher
		if subcommand(version) {
			args = append(args, "execute")
		}
		if launcher[0] == "-jar" {
			args = append(args, "--class-path", joined)
		}
		args = append(args, "--disable-banner")
		if a.Kind == RunTest {
			return append(args, "--select-method="+a.Class+"#"+a.Method)
		}
		return append(args, "--select-class="+a.Class)
	}
	standalone := func() []string {
		if j, v := jar("junit-platform-console-standalone-"); j != "" {
			return platform([]string{"-jar", j}, v)
		}
		if j, v := jar("junit-platform-console-"); j != "" {
			return platform([]string{"-cp", joined, "org.junit.platform.console.ConsoleLauncher"}, v)
		}
		return nil
	}
	junit4 := func() []string {
		if j, v := jar("junit-"); j != "" && strings.HasPrefix(v, "4.") {
			return []string{"-cp", joined, "org.junit.runner.JUnitCore", a.Class}
		}
		return nil
	}
	testng := func() []string {
		if j, _ := jar("testng-"); j != "" {
			args := []string{"-cp", joined, "org.testng.TestNG", "-testclass", a.Class}
			if a.Kind == RunTest {
				args = append(args, "-methods", a.Class+"."+a.Method)
			}
			return args
		}
		return nil
	}
	var order []func() []string
	switch a.Framework {
	case "junit5":
		order = []func() []string{standalone}
	case "junit4":
		order = []func() []string{junit4, standalone}
	case "testng":
		order = []func() []string{testng}
	default:
		order = []func() []string{standalone, junit4, testng}
	}
	for _, f := range order {
		if args := f(); args != nil {
			return args, nil
		}
	}
	return nil, errors.New("kcls runs tests outside a Gradle build with a launcher on the module's class path: junit-platform-console-standalone, JUnit 4 or TestNG, and there is none")
}

// subcommand reports whether the JUnit Platform console launcher of a
// version takes the execute subcommand: 1.10 and later.
func subcommand(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	if major != 1 {
		return major > 1
	}
	if len(parts) < 2 {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	return err == nil && minor >= 10
}

// runClassPath returns the class path of m, less the outputs of the
// modules it is compiled with, and its friend paths.
func runClassPath(m *project.Module, deps []*project.Module) (cp, friends []string) {
	var builds []string
	for _, d := range deps {
		if d.Output != "" {
			builds = append(builds, d.Output)
		}
	}
	for _, e := range m.ClassPath {
		if slices.ContainsFunc(builds, func(b string) bool { return e == b || strings.HasPrefix(e, b+string(filepath.Separator)) }) || !exists(e) {
			continue
		}
		cp = append(cp, e)
		if slices.Contains(m.FriendPaths, e) {
			friends = append(friends, e)
		}
	}
	return cp, friends
}

// resourceDirs returns the resources directories next to the modules'
// source roots: src/main/resources for src/main/kotlin.
func resourceDirs(ms []*project.Module) []string {
	var out []string
	for _, m := range ms {
		for _, r := range m.Roots {
			switch filepath.Base(r) {
			case "kotlin", "java":
				if d := filepath.Join(filepath.Dir(r), "resources"); exists(d) && !slices.Contains(out, d) {
					out = append(out, d)
				}
			}
		}
	}
	return out
}

// writeArgFile writes args to the argument file name in dir, one to a
// line, quoted where they need it, and returns its path.
func writeArgFile(dir, name string, args []string) (string, error) {
	var b strings.Builder
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t\r\n\"'\\#") {
			a = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(a) + `"`
		}
		b.WriteString(a)
		b.WriteByte('\n')
	}
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

// shellJoin returns args as a POSIX shell command.
func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r < utf8.RuneSelf && (r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '@' || r == '=' || r == '+' || r == ',' || r == '%' ||
			'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// simpleClassName returns a JVM class name less its package: Outer$Inner
// for p.Outer$Inner.
func simpleClassName(name string) string {
	return name[strings.LastIndexByte(name, '.')+1:]
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
