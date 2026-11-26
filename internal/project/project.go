// Package project finds the modules of a workspace: their sources, class
// paths and compiler settings. A workspace is described, in this order
// of preference, by a kcls.json file at its root, by its Gradle build, by
// its Maven build, or else as one module of every Kotlin file under the root, compiled
// against the JDK and the Kotlin standard library kc finds.
package project

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/barteo/kotlin-compiler-go/internal/classpath"
)

// Workspace is the modules of a workspace folder.
type Workspace struct {
	Root string
	// Source says where the model comes from: "kcls.json", "gradle",
	// "maven" or "folder".
	Source  string
	Modules []*Module
	// SourceJars maps a class path jar to the jar of its sources, where
	// the build knows one.
	SourceJars map[string]string `json:",omitempty"`
}

// Module is a set of sources compiled together, as a Gradle source set
// is: main and test are two modules.
type Module struct {
	// ID identifies the module in its workspace, as ":app:main".
	ID string
	// ModuleName is the Kotlin module name, which internal declarations'
	// JVM names end with.
	ModuleName string `json:",omitempty"`
	// Roots are the directories whose .kt, .kts and .java files are the
	// module's, and Files further files that are.
	Roots []string `json:",omitempty"`
	Files []string `json:",omitempty"`
	// ClassPath and FriendPaths are as for kotlinc: the jars and class
	// directories the module compiles against, and those of them whose
	// internal declarations it sees.
	ClassPath   []string `json:",omitempty"`
	FriendPaths []string `json:",omitempty"`
	// JDKHome is the JDK whose classes the module sees; empty for the
	// one kc finds. NoJDK leaves the JDK out, as for Android modules,
	// whose class path has android.jar.
	JDKHome string `json:",omitempty"`
	NoJDK   bool   `json:",omitempty"`
	// JVMTarget, ExplicitAPI ("", "warning" or "strict"), Plugins
	// ("serialization", "parcelize", "compose") and OptIns are the
	// compiler's settings.
	JVMTarget   string   `json:",omitempty"`
	ExplicitAPI string   `json:",omitempty"`
	Plugins     []string `json:",omitempty"`
	OptIns      []string `json:",omitempty"`
	// MultiPlatform compiles expect and actual declarations, as kotlinc's
	// -Xmulti-platform: a multiplatform module's JVM sources.
	MultiPlatform bool `json:",omitempty"`
	// Dir is the build's project directory of the module: a Gradle
	// project's or a Maven module's. Empty for other workspaces.
	Dir string `json:",omitempty"`
	// Output is where the build writes the module's classes. A module
	// whose class path has another's Output depends on it.
	Output string `json:",omitempty"`
	// DependsOn are the IDs of the modules whose sources this one uses.
	DependsOn []string `json:",omitempty"`
}

// Has reports whether path is one of the module's sources.
func (m *Module) Has(path string) bool {
	if slices.Contains(m.Files, path) {
		return true
	}
	for _, r := range m.Roots {
		if within(path, r) {
			return true
		}
	}
	return false
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// IsSource reports whether path names a file a module compiles.
func IsSource(path string) bool {
	switch filepath.Ext(path) {
	case ".kt", ".kts", ".java":
		return true
	}
	return false
}

// skipDirs are directories never searched for sources: build outputs,
// tool state and dependencies.
var skipDirs = map[string]bool{
	"build": true, "out": true, "target": true, "node_modules": true,
	".git": true, ".gradle": true, ".idea": true, ".kotlin": true, ".claude": true,
}

// Sources returns the module's source files, sorted: the files under its
// roots and its Files that exist.
func (m *Module) Sources() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, r := range m.Roots {
		filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() && p != r {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != r && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return fs.SkipDir
				}
				return nil
			}
			if IsSource(p) && !strings.HasSuffix(p, ".kts") {
				add(p)
			}
			return nil
		})
	}
	for _, f := range m.Files {
		if _, err := os.Stat(f); err == nil {
			add(f)
		}
	}
	slices.Sort(out)
	return out
}

// Module returns the module whose source path is, preferring a main
// module to a test module that also lists it, or nil.
func (w *Workspace) Module(path string) *Module {
	for _, m := range w.Modules {
		if m.Has(path) {
			return m
		}
	}
	return nil
}

// ByID returns the module with the ID id, or nil.
func (w *Workspace) ByID(id string) *Module {
	for _, m := range w.Modules {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// linkModules sets each module's DependsOn from the outputs on its class
// path.
func (w *Workspace) linkModules() {
	byOutput := map[string]string{}
	for _, m := range w.Modules {
		if m.Output != "" {
			byOutput[filepath.Clean(m.Output)] = m.ID
		}
	}
	for _, m := range w.Modules {
		for _, e := range m.ClassPath {
			if id, ok := byOutput[filepath.Clean(e)]; ok && id != m.ID && !slices.Contains(m.DependsOn, id) {
				m.DependsOn = append(m.DependsOn, id)
			}
		}
	}
}

// ---- kcls.json ----

// ConfigFile is the name of the file that describes a workspace's
// modules by hand.
const ConfigFile = "kcls.json"

// configJSON is kcls.json: the modules, with paths relative to the
// file's directory.
type configJSON struct {
	Modules []struct {
		Name          string   `json:"name"`
		ModuleName    string   `json:"moduleName"`
		Roots         []string `json:"roots"`
		Files         []string `json:"files"`
		ClassPath     []string `json:"classPath"`
		FriendPaths   []string `json:"friendPaths"`
		JDKHome       string   `json:"jdkHome"`
		NoJDK         bool     `json:"noJdk"`
		JVMTarget     string   `json:"jvmTarget"`
		ExplicitAPI   string   `json:"explicitApi"`
		Plugins       []string `json:"plugins"`
		OptIns        []string `json:"optIns"`
		MultiPlatform bool     `json:"multiPlatform"`
		Output        string   `json:"output"`
		DependsOn     []string `json:"dependsOn"`
		// Stdlib adds the Kotlin standard library kc finds to the class
		// path; true unless set to false.
		Stdlib *bool `json:"stdlib"`
	} `json:"modules"`
	SourceJars map[string]string `json:"sourceJars"`
}

// LoadConfig reads the workspace described by the kcls.json at root.
func LoadConfig(root string) (*Workspace, error) {
	path := filepath.Join(root, ConfigFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c configJSON
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	absAll := func(ps []string) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, abs(p))
		}
		return out
	}
	w := &Workspace{Root: root, Source: ConfigFile, SourceJars: map[string]string{}}
	for k, v := range c.SourceJars {
		w.SourceJars[abs(k)] = abs(v)
	}
	if len(c.Modules) == 0 {
		return nil, fmt.Errorf("%s: no modules", path)
	}
	for i, cm := range c.Modules {
		m := &Module{
			ID: cm.Name, ModuleName: cm.ModuleName, Roots: absAll(cm.Roots), Files: absAll(cm.Files),
			ClassPath: absAll(cm.ClassPath), FriendPaths: absAll(cm.FriendPaths), JDKHome: abs(cm.JDKHome),
			NoJDK: cm.NoJDK, JVMTarget: cm.JVMTarget, ExplicitAPI: cm.ExplicitAPI, Plugins: cm.Plugins,
			OptIns: cm.OptIns, MultiPlatform: cm.MultiPlatform, Output: abs(cm.Output), DependsOn: cm.DependsOn,
		}
		if m.ID == "" {
			m.ID = fmt.Sprintf("module%d", i+1)
		}
		if m.ModuleName == "" {
			m.ModuleName = m.ID
		}
		if len(m.Roots) == 0 && len(m.Files) == 0 {
			return nil, fmt.Errorf("%s: module %s has no roots or files", path, m.ID)
		}
		if cm.Stdlib == nil || *cm.Stdlib {
			m.ClassPath = withStdlib(m.ClassPath)
		}
		w.Modules = append(w.Modules, m)
	}
	for _, m := range w.Modules {
		for _, d := range m.DependsOn {
			if w.ByID(d) == nil {
				return nil, fmt.Errorf("%s: module %s depends on unknown module %s", path, m.ID, d)
			}
		}
	}
	return w, nil
}

// ---- folder ----

// Folder returns the workspace of every Kotlin file under root, as one
// module compiled against the JDK and the standard library kc finds.
func Folder(root string) *Workspace {
	return &Workspace{Root: root, Source: "folder", Modules: []*Module{{
		ID: "root", ModuleName: "main", Roots: []string{root}, ClassPath: withStdlib(nil), JDKHome: FindJDK(),
	}}}
}

// SingleFile returns a module of one file outside every module, compiled
// against the JDK and the standard library kc finds.
func SingleFile(path string) *Module {
	return &Module{ID: "file:" + path, ModuleName: "main", Files: []string{path}, ClassPath: withStdlib(nil), JDKHome: FindJDK()}
}

// withStdlib adds the Kotlin standard library kc finds to cp, unless cp
// has one.
func withStdlib(cp []string) []string {
	for _, e := range cp {
		base := filepath.Base(e)
		if strings.HasPrefix(base, "kotlin-stdlib") && !strings.Contains(base, "-sources") {
			return cp
		}
	}
	jar, err := classpath.FindStdlib()
	if err != nil {
		return cp
	}
	return append(slices.Clone(cp), jar)
}

// FindJDK returns the JDK kc would use, or, on macOS without JAVA_HOME,
// the one /usr/libexec/java_home names: an editor started from the Dock
// has no JAVA_HOME. It returns "" if there is none.
func FindJDK() string {
	if jdk, err := classpath.FindJDK(); err == nil {
		return jdk
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/libexec/java_home").Output(); err == nil {
			if jdk := strings.TrimSpace(string(out)); jdk != "" {
				return jdk
			}
		}
	}
	return ""
}

// Load returns the workspace at root as far as it is known without
// running a build: from its kcls.json, from the Gradle or Maven model
// cached for it, or else as a folder. stale reports that the workspace
// has a build whose model is not cached or whose build files changed
// since: Import finds it.
func Load(root, cacheDir string) (w *Workspace, stale bool, err error) {
	switch Detect(root) {
	case ConfigFile:
		w, err := LoadConfig(root)
		return w, false, err
	case "gradle", "maven":
		if w, current := Cached(cacheDir, root); w != nil {
			return w, !current, nil
		}
		return Folder(root), true, nil
	}
	return Folder(root), false, nil
}

// Detect says what describes the workspace at root: "kcls.json",
// "gradle", "maven" or "folder".
func Detect(root string) string {
	if _, err := os.Stat(filepath.Join(root, ConfigFile)); err == nil {
		return ConfigFile
	}
	for _, f := range []string{"settings.gradle.kts", "settings.gradle", "build.gradle.kts", "build.gradle"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			return "gradle"
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pom.xml")); err == nil {
		return "maven"
	}
	return "folder"
}

// Import runs the build of the workspace at root, Gradle or Maven, to
// find its modules.
func Import(ctx context.Context, root string, opts GradleOptions) (*Workspace, error) {
	if Detect(root) == "maven" {
		return ImportMaven(ctx, root, opts)
	}
	return ImportGradle(ctx, root, opts)
}

// nonJVMSets are the prefixes of the multiplatform source sets of
// targets other than the JVM, and of the shared ones.
var nonJVMSets = []string{"common", "native", "apple", "ios", "macos", "tvos", "watchos", "linux", "mingw", "androidNative", "js", "wasm"}

// NonJVMSourceSet returns the source set of a multiplatform module that
// the file at path is in, src/<set>/kotlin, if it is one that may compile
// for targets other than the JVM only: commonMain, jsMain, iosArm64Test.
func NonJVMSourceSet(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := len(parts) - 3; i > 0; i-- {
		if parts[i-1] != "src" || parts[i+1] != "kotlin" {
			continue
		}
		set := parts[i]
		for _, p := range nonJVMSets {
			if rest, ok := strings.CutPrefix(set, p); ok && rest != "" && rest[0] >= 'A' && rest[0] <= 'Z' {
				return set
			}
		}
		return ""
	}
	return ""
}
