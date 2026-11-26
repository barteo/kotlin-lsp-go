package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

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

func TestFolder(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"a.kt":                 "",
		"src/b/B.kt":           "",
		"src/b/J.java":         "",
		"build.gradle.kts":     "",
		"build/gen/C.kt":       "",
		".hidden/D.kt":         "",
		"node_modules/x/E.kt":  "",
		"script.main.kts":      "",
		"src/b/notes.txt":      "",
		"nested/out/Ignore.kt": "",
	})
	w := Folder(root)
	if len(w.Modules) != 1 {
		t.Fatalf("modules: %+v", w.Modules)
	}
	m := w.Modules[0]
	got := m.Sources()
	want := []string{filepath.Join(root, "a.kt"), filepath.Join(root, "src/b/B.kt"), filepath.Join(root, "src/b/J.java")}
	if !slices.Equal(got, want) {
		t.Errorf("sources %v, want %v", got, want)
	}
	if !m.Has(filepath.Join(root, "x/New.kt")) || m.Has(filepath.Join(filepath.Dir(root), "Other.kt")) {
		t.Error("Has: wrong containment")
	}
	if w.Module(filepath.Join(root, "a.kt")) != m {
		t.Error("Module: a.kt not in the folder module")
	}
}

func TestLoadConfig(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"kcls.json": `{
  "modules": [
    {"name": "lib", "roots": ["lib/src"], "classPath": ["libs/a.jar"], "stdlib": false, "jvmTarget": "17"},
    {"name": "app", "roots": ["app/src"], "dependsOn": ["lib"], "plugins": ["serialization"], "explicitApi": "strict"}
  ],
  "sourceJars": {"libs/a.jar": "libs/a-sources.jar"}
}`,
		"lib/src/L.kt": "",
		"app/src/A.kt": "",
	})
	if Detect(root) != ConfigFile {
		t.Fatalf("Detect: %s", Detect(root))
	}
	w, stale, err := Load(root, t.TempDir())
	if err != nil || stale {
		t.Fatal(err, stale)
	}
	if len(w.Modules) != 2 {
		t.Fatalf("modules: %+v", w.Modules)
	}
	lib, app := w.ByID("lib"), w.ByID("app")
	if lib == nil || app == nil {
		t.Fatalf("modules: %+v", w.Modules)
	}
	if !slices.Equal(lib.ClassPath, []string{filepath.Join(root, "libs/a.jar")}) {
		t.Errorf("lib class path %v", lib.ClassPath)
	}
	if lib.JVMTarget != "17" || app.ExplicitAPI != "strict" || !slices.Equal(app.Plugins, []string{"serialization"}) {
		t.Errorf("settings: %+v %+v", lib, app)
	}
	if !slices.Equal(app.DependsOn, []string{"lib"}) || app.ModuleName != "app" {
		t.Errorf("app: %+v", app)
	}
	if w.Module(filepath.Join(root, "app/src/A.kt")) != app {
		t.Error("A.kt not in app")
	}
	if w.SourceJars[filepath.Join(root, "libs/a.jar")] != filepath.Join(root, "libs/a-sources.jar") {
		t.Errorf("source jars %v", w.SourceJars)
	}

	write(t, root, map[string]string{"kcls.json": `{"modules": [{"name": "x", "roots": ["s"], "dependsOn": ["y"]}]}`})
	if _, err := LoadConfig(root); err == nil || !strings.Contains(err.Error(), "unknown module y") {
		t.Errorf("unknown dependency: %v", err)
	}
	write(t, root, map[string]string{"kcls.json": `{"modules": [{"name": "x"}]}`})
	if _, err := LoadConfig(root); err == nil {
		t.Error("module without roots: no error")
	}
}

func TestSourceSetName(t *testing.T) {
	for task, want := range map[string]string{
		"compileKotlin":              "main",
		"compileTestKotlin":          "test",
		"compileDebugKotlin":         "debug",
		"compileDebugUnitTestKotlin": "debugUnitTest",
		"compileKotlinJvm":           "jvm",
		"compileTestKotlinJvm":       "testJvm",
	} {
		if got := sourceSetName(task); got != want {
			t.Errorf("%s: %s, want %s", task, got, want)
		}
	}
}

func TestFromGradle(t *testing.T) {
	root := "/w"
	gm := &gradleModel{
		Modules: []gradleModule{
			{Project: ":app", ProjectDir: "/w/app", Task: "compileTestKotlin",
				Sources:   []string{"/w/app/src/test/kotlin/p/T.kt"},
				ClassPath: []string{"/w/app/build/classes/kotlin/main", "/w/lib/build/libs/lib.jar", "/c/junit.jar"}},
			{Project: ":app", ProjectDir: "/w/app", Task: "compileKotlin", ModuleName: "app",
				Sources:   []string{"/w/app/src/main/kotlin/p/A.kt", "/w/app/src/main/java/p/J.java", "/w/app/build/generated/ksp/main/kotlin/G.kt"},
				ClassPath: []string{"/w/lib/build/libs/lib.jar", "/c/kotlin-stdlib.jar"}, Output: "/w/app/build/classes/kotlin/main",
				Plugins: []string{"serialization"}, JVMTarget: "17"},
			{Project: ":lib", ProjectDir: "/w/lib", Task: "compileKotlin",
				Sources: []string{"/w/lib/src/main/kotlin/L.kt"}, Output: "/w/lib/build/classes/kotlin/main"},
			{Project: ":android", ProjectDir: "/w/android", Task: "compileDebugKotlin", Sources: []string{"/w/android/src/main/kotlin/D.kt"}},
			{Project: ":android", ProjectDir: "/w/android", Task: "compileReleaseKotlin", Sources: []string{"/w/android/src/main/kotlin/D.kt"}},
			{Project: ":empty", Task: "compileKotlin"},
		},
	}
	w := fromGradle(root, gm)
	var ids []string
	for _, m := range w.Modules {
		ids = append(ids, m.ID)
	}
	if want := []string{":app:main", ":lib:main", ":android:debug", ":app:test"}; !slices.Equal(ids, want) {
		t.Fatalf("modules %v, want %v", ids, want)
	}
	app := w.ByID(":app:main")
	if want := []string{"/w/app/src/main/java", "/w/app/src/main/kotlin"}; !slices.Equal(app.Roots, want) {
		t.Errorf("roots %v, want %v", app.Roots, want)
	}
	if want := []string{"/w/app/build/generated/ksp/main/kotlin/G.kt"}; !slices.Equal(app.Files, want) {
		t.Errorf("files %v, want %v", app.Files, want)
	}
	if !slices.Equal(app.DependsOn, []string{":lib:main"}) {
		t.Errorf("app depends on %v", app.DependsOn)
	}
	test := w.ByID(":app:test")
	if !slices.Contains(test.DependsOn, ":app:main") || !slices.Contains(test.DependsOn, ":lib:main") {
		t.Errorf("test depends on %v", test.DependsOn)
	}
	if test.ModuleName != "test" || app.ModuleName != "app" {
		t.Errorf("module names %q %q", app.ModuleName, test.ModuleName)
	}
	if w.Module("/w/app/src/main/kotlin/p/New.kt") != app {
		t.Error("a new file in app's root is not app's")
	}
}

func TestCache(t *testing.T) {
	root, cache := t.TempDir(), t.TempDir()
	write(t, root, map[string]string{"settings.gradle.kts": "rootProject.name = \"x\"\n", "src/main/kotlin/A.kt": ""})
	if w, stale, err := Load(root, cache); err != nil || !stale || w.Source != "folder" {
		t.Fatalf("Load without a cached model: %v %v %v", w, stale, err)
	}
	dir := workspaceCache(cache, root)
	os.MkdirAll(dir, 0o755)
	saveCache(dir, BuildKey(root), &Workspace{Root: root, Source: "gradle", Modules: []*Module{{ID: ":main"}}})
	w, stale, err := Load(root, cache)
	if err != nil || stale || w.Source != "gradle" {
		t.Fatalf("Load with a current model: %+v %v %v", w, stale, err)
	}
	// An edit of a source doesn't change the key; one of a build file does.
	write(t, root, map[string]string{"src/main/kotlin/A.kt": "fun a() {}"})
	if _, current := Cached(cache, root); !current {
		t.Error("source edit made the model stale")
	}
	write(t, root, map[string]string{"sub/build.gradle.kts": "plugins {}"})
	if w, current := Cached(cache, root); w == nil || current {
		t.Error("build file edit: model still current")
	}
	if err := ClearCache(cache, root); err != nil {
		t.Fatal(err)
	}
	if w, _ := Cached(cache, root); w != nil {
		t.Error("model left after ClearCache")
	}
}

// TestImportGradle imports a small Gradle build with a library and an
// application module. It runs Gradle, so it needs KCLS_GRADLE_TEST=1 and
// either gradle on PATH or KCLS_GRADLE naming a gradle binary, and the
// Kotlin Gradle plugin (KCLS_KGP, default 2.4.20) in Gradle's cache or
// reachable online.
func TestImportGradle(t *testing.T) {
	if os.Getenv("KCLS_GRADLE_TEST") != "1" {
		t.Skip("KCLS_GRADLE_TEST=1 runs Gradle")
	}
	gradle := os.Getenv("KCLS_GRADLE")
	if gradle == "" {
		var err error
		if gradle, err = exec.LookPath("gradle"); err != nil {
			t.Skip("no gradle on PATH and no KCLS_GRADLE")
		}
	}
	kgp := os.Getenv("KCLS_KGP")
	if kgp == "" {
		kgp = "2.4.20"
	}
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	write(t, root, map[string]string{
		"settings.gradle.kts": `pluginManagement { repositories { mavenCentral(); gradlePluginPortal() } }
dependencyResolutionManagement { repositories { mavenCentral() } }
rootProject.name = "kcls-import"
include("lib", "app")
`,
		"build.gradle.kts":     "plugins { kotlin(\"jvm\") version \"" + kgp + "\" apply false }\n",
		"lib/build.gradle.kts": "plugins { kotlin(\"jvm\") }\nkotlin { explicitApi() }\n",
		"app/build.gradle.kts": "plugins { kotlin(\"jvm\") }\ndependencies { implementation(project(\":lib\")) }\n" +
			"kotlin { compilerOptions { optIn.add(\"kotlin.RequiresOptIn\") } }\n",
		"lib/src/main/kotlin/L.kt":  "package lib\npublic fun lib(): Int = 1\n",
		"app/src/main/kotlin/A.kt":  "fun main() = println(lib.lib())\n",
		"app/src/test/kotlin/AT.kt": "fun t() = main()\n",
	})
	// The wrapper ImportGradle looks for.
	script := "#!/bin/sh\nexec " + gradle + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(root, "gradlew"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cache := t.TempDir()
	w, err := ImportGradle(ctx, root, GradleOptions{CacheDir: cache, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	app, lib, test := w.ByID(":app:main"), w.ByID(":lib:main"), w.ByID(":app:test")
	if app == nil || lib == nil || test == nil {
		t.Fatalf("modules: %+v", w.Modules)
	}
	if !slices.Contains(app.DependsOn, ":lib:main") {
		t.Errorf("app depends on %v", app.DependsOn)
	}
	if lib.ExplicitAPI != "strict" {
		t.Errorf("lib explicit API %q", lib.ExplicitAPI)
	}
	if !slices.Contains(app.OptIns, "kotlin.RequiresOptIn") {
		t.Errorf("app opt-ins %v", app.OptIns)
	}
	if want := filepath.Join(root, "app/src/main/kotlin"); !slices.Contains(app.Roots, want) {
		t.Errorf("app roots %v", app.Roots)
	}
	stdlib := ""
	for _, e := range app.ClassPath {
		if strings.Contains(filepath.Base(e), "kotlin-stdlib") {
			stdlib = e
		}
	}
	if stdlib == "" {
		t.Errorf("no stdlib on app's class path %v", app.ClassPath)
	} else if w.SourceJars[stdlib] == "" {
		t.Errorf("no sources jar for %s: %v", stdlib, w.SourceJars)
	}
	if got, current := Cached(cache, root); got == nil || !current || len(got.Modules) != len(w.Modules) {
		t.Errorf("cached model: %+v %v", got, current)
	}
}
