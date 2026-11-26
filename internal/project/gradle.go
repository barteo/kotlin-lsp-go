package project

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// InitScript is the Gradle init script that writes a build's modules as
// JSON.
//
//go:embed kcls-model.init.gradle.kts
var InitScript string

// gradleModel is what the init script writes.
type gradleModel struct {
	GradleVersion string            `json:"gradleVersion"`
	Modules       []gradleModule    `json:"modules"`
	SourceJars    map[string]string `json:"sourceJars"`
	Errors        []string          `json:"errors"`
}

type gradleModule struct {
	Project       string   `json:"project"`
	ProjectDir    string   `json:"projectDir"`
	Task          string   `json:"task"`
	Sources       []string `json:"sources"`
	ClassPath     []string `json:"classPath"`
	FriendPaths   []string `json:"friendPaths"`
	Output        string   `json:"output"`
	ModuleName    string   `json:"moduleName"`
	NoJDK         bool     `json:"noJdk"`
	JDKHome       string   `json:"jdkHome"`
	JVMTarget     string   `json:"jvmTarget"`
	ExplicitAPI   string   `json:"explicitApi"`
	MultiPlatform bool     `json:"multiPlatform"`
	OptIns        []string `json:"optIns"`
	Plugins       []string `json:"plugins"`
}

// GradleOptions configure ImportGradle.
type GradleOptions struct {
	// Logf, if set, gets Gradle's output.
	Logf func(format string, args ...any)
	// NoSources skips resolving the libraries' sources jars, which may
	// download them.
	NoSources bool
	// CacheDir is where the model and the init script are kept; empty for
	// CacheDir().
	CacheDir string
}

// ImportGradle runs the Gradle build at root to find its modules, and
// keeps the model in the cache for Cached. It takes as long as Gradle
// takes to configure the build, which on a first run includes
// downloading Gradle and the build's plugins.
func ImportGradle(ctx context.Context, root string, opts GradleOptions) (*Workspace, error) {
	cmd, err := gradleCommand(root)
	if err != nil {
		return nil, err
	}
	dir := opts.CacheDir
	if dir == "" {
		dir = CacheDir()
	}
	dir = workspaceCache(dir, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	key := BuildKey(root)
	script := filepath.Join(dir, "kcls-model.init.gradle.kts")
	if err := os.WriteFile(script, []byte(InitScript), 0o644); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "gradle-model.json")
	os.Remove(out)
	args := []string{"-q", "--no-configuration-cache", "--console=plain", "-I", script, "-Pkcls.out=" + out}
	if opts.NoSources {
		args = append(args, "-Pkcls.sources=false")
	}
	args = append(args, "kclsModel")
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = root
	c.Env = os.Environ()
	if os.Getenv("JAVA_HOME") == "" {
		// Gradle needs a JDK to run on, and an editor started from the
		// Dock has no JAVA_HOME.
		if jdk := FindJDK(); jdk != "" {
			c.Env = append(c.Env, "JAVA_HOME="+jdk)
		}
	}
	var output bytes.Buffer
	c.Stdout, c.Stderr = &output, &output
	if opts.Logf != nil {
		opts.Logf("project: %s %s", cmd, strings.Join(args, " "))
	}
	err = c.Run()
	if opts.Logf != nil && output.Len() > 0 {
		opts.Logf("project: gradle: %s", strings.TrimSpace(output.String()))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("gradle: %w: %s", err, lastLines(output.String(), 20))
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("gradle: no model: %w", err)
	}
	var gm gradleModel
	if err := json.Unmarshal(b, &gm); err != nil {
		return nil, fmt.Errorf("gradle: model: %w", err)
	}
	if opts.Logf != nil {
		for _, e := range gm.Errors {
			opts.Logf("project: gradle: %s", e)
		}
	}
	w := fromGradle(root, &gm)
	if len(w.Modules) == 0 {
		return nil, errors.New("gradle: the build has no Kotlin/JVM modules")
	}
	saveCache(dir, key, w)
	return w, nil
}

// gradleCommand returns the build's wrapper, or else gradle on PATH.
func gradleCommand(root string) (string, error) {
	name := "gradlew"
	if runtime.GOOS == "windows" {
		name = "gradlew.bat"
	}
	if fi, err := os.Stat(filepath.Join(root, name)); err == nil && !fi.IsDir() {
		return filepath.Join(root, name), nil
	}
	if p, err := exec.LookPath("gradle"); err == nil {
		return p, nil
	}
	return "", errors.New("gradle: no gradlew in the workspace and no gradle on PATH")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// fromGradle turns the init script's model into a workspace: one module
// per compile task, an Android module's debug variants only, test modules
// after main ones.
func fromGradle(root string, gm *gradleModel) *Workspace {
	w := &Workspace{Root: root, Source: "gradle", SourceJars: gm.SourceJars}
	debug := map[string]bool{}
	for _, gmod := range gm.Modules {
		if strings.Contains(strings.ToLower(gmod.Task), "debug") {
			debug[gmod.Project] = true
		}
	}
	type entry struct {
		m    *Module
		dir  string
		test bool
	}
	var entries []entry
	for _, gmod := range gm.Modules {
		if len(gmod.Sources) == 0 {
			continue
		}
		if debug[gmod.Project] && strings.Contains(strings.ToLower(gmod.Task), "release") {
			continue
		}
		name := sourceSetName(gmod.Task)
		m := &Module{
			ID:            strings.TrimSuffix(gmod.Project, ":") + ":" + name,
			ModuleName:    gmod.ModuleName,
			ClassPath:     gmod.ClassPath,
			FriendPaths:   gmod.FriendPaths,
			JDKHome:       gmod.JDKHome,
			NoJDK:         gmod.NoJDK,
			JVMTarget:     gmod.JVMTarget,
			ExplicitAPI:   gmod.ExplicitAPI,
			Plugins:       gmod.Plugins,
			OptIns:        gmod.OptIns,
			Output:        gmod.Output,
			MultiPlatform: gmod.MultiPlatform,
			Dir:           gmod.ProjectDir,
		}
		if m.ModuleName == "" {
			m.ModuleName = name
		}
		m.Roots, m.Files = sourceRoots(gmod.Sources)
		entries = append(entries, entry{m, gmod.ProjectDir, strings.Contains(strings.ToLower(name), "test")})
	}
	slices.SortStableFunc(entries, func(a, b entry) int {
		switch {
		case a.test == b.test:
			return 0
		case b.test:
			return -1
		}
		return 1
	})
	for _, e := range entries {
		w.Modules = append(w.Modules, e.m)
	}
	w.linkModules()
	// A jar or class directory in another project's build directory is
	// that project's main module.
	for _, e := range entries {
		for _, cp := range e.m.ClassPath {
			for _, o := range entries {
				if o.m == e.m || o.test || o.dir == "" || o.dir == e.dir {
					continue
				}
				if within(cp, filepath.Join(o.dir, "build")) && !slices.Contains(e.m.DependsOn, o.m.ID) {
					e.m.DependsOn = append(e.m.DependsOn, o.m.ID)
				}
			}
		}
	}
	return w
}

// sourceSetName returns the source set a compile task compiles:
// compileKotlin's main, compileTestKotlin's test, compileDebugKotlin's
// debug, compileKotlinJvm's jvm.
func sourceSetName(task string) string {
	s := strings.TrimPrefix(task, "compile")
	if t, ok := strings.CutSuffix(s, "Kotlin"); ok {
		s = t
	} else {
		s = strings.Replace(s, "Kotlin", "", 1)
	}
	if s == "" {
		return "main"
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

// sourceRoots returns the source directories of files, as src/main/kotlin
// and src/main/java, and the files in none of them.
func sourceRoots(files []string) (roots, rest []string) {
	seen := map[string]bool{}
	for _, f := range files {
		root := ""
		for d := filepath.Dir(f); ; {
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			if filepath.Base(filepath.Dir(parent)) == "src" {
				root = d
				break
			}
			d = parent
		}
		if root == "" {
			rest = append(rest, f)
			continue
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	slices.Sort(roots)
	return roots, rest
}

// ---- cache ----

// CacheDir returns the directory kcls keeps its caches in.
func CacheDir() string {
	if d := os.Getenv("KCLS_CACHE"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "kcls")
	}
	return filepath.Join(os.TempDir(), "kcls")
}

// workspaceCache returns the cache directory of the workspace at root.
func workspaceCache(dir, root string) string {
	h := sha256.Sum256([]byte(root))
	return filepath.Join(dir, filepath.Base(root)+"-"+hex.EncodeToString(h[:6]))
}

// buildFile reports whether name is a file a Gradle build's model depends
// on.
func buildFile(name string) bool {
	switch name {
	case "settings.gradle.kts", "settings.gradle", "build.gradle.kts", "build.gradle",
		"gradle.properties", "libs.versions.toml", "gradle-wrapper.properties",
		"pom.xml", "maven.config", "maven-wrapper.properties":
		return true
	}
	return false
}

// IsBuildFile reports whether a change to path may change the model of
// the workspace it is in.
func IsBuildFile(path string) bool {
	return buildFile(filepath.Base(path)) || filepath.Base(path) == ConfigFile
}

// BuildKey returns a hash of the build files under root, which changes
// when the model may.
func BuildKey(root string) string {
	h := sha256.New()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || d.Name() == "src") {
				return fs.SkipDir
			}
			return nil
		}
		if buildFile(d.Name()) {
			if b, err := os.ReadFile(p); err == nil {
				rel, _ := filepath.Rel(root, p)
				fmt.Fprintf(h, "%s\x00%d\x00", rel, len(b))
				h.Write(b)
			}
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

type cachedModel struct {
	Key       string
	Workspace *Workspace
}

func saveCache(dir, key string, w *Workspace) {
	b, err := json.MarshalIndent(cachedModel{key, w}, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "model.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, "model.json"))
	}
}

// Cached returns the model ImportGradle last found for root, and whether
// the build files are still those it found it for.
func Cached(cacheDir, root string) (w *Workspace, current bool) {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}
	b, err := os.ReadFile(filepath.Join(workspaceCache(cacheDir, root), "model.json"))
	if err != nil {
		return nil, false
	}
	var c cachedModel
	if json.Unmarshal(b, &c) != nil || c.Workspace == nil || c.Workspace.Root != root {
		return nil, false
	}
	return c.Workspace, c.Key == BuildKey(root)
}

// ClearCache removes what kcls keeps for the workspace at root.
func ClearCache(cacheDir, root string) error {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}
	return os.RemoveAll(workspaceCache(cacheDir, root))
}
