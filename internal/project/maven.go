package project

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

// ImportMaven runs the Maven build at root to find its modules: the
// effective POM of each module of the reactor, for its source and output
// directories, its Kotlin compiler settings and its dependencies on the
// other modules, and the dependency plugin's class paths, compile scope
// for main and test scope for test. It compiles nothing. A module whose
// dependencies Maven can't resolve, as one on a module of the reactor
// that isn't installed, gets its direct dependencies from the local
// repository. The model is cached as ImportGradle's is.
func ImportMaven(ctx context.Context, root string, opts GradleOptions) (*Workspace, error) {
	cmd, err := mavenCommand(root)
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
	run := func(args ...string) (string, error) {
		args = append([]string{"-B", "--no-transfer-progress"}, args...)
		c := exec.CommandContext(ctx, cmd, args...)
		c.Dir = root
		c.Env = os.Environ()
		if os.Getenv("JAVA_HOME") == "" {
			if jdk := FindJDK(); jdk != "" {
				c.Env = append(c.Env, "JAVA_HOME="+jdk)
			}
		}
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		if opts.Logf != nil {
			opts.Logf("project: %s %s", cmd, strings.Join(args, " "))
		}
		err := c.Run()
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return out.String(), fmt.Errorf("maven: %w: %s", err, lastLines(out.String(), 20))
		}
		return out.String(), nil
	}
	pom := filepath.Join(dir, "effective-pom.xml")
	os.Remove(pom)
	if _, err := run("-q", "help:effective-pom", "-Doutput="+pom); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(pom)
	if err != nil {
		return nil, fmt.Errorf("maven: no effective POM: %w", err)
	}
	projects, err := parseEffectivePOM(b)
	if err != nil {
		return nil, err
	}
	// -fn: a module whose dependencies don't resolve doesn't stop the
	// others'.
	classPaths := map[string]map[string][]string{} // scope, artifactId
	for _, scope := range []string{"compile", "test"} {
		out, err := run("-fn", "dependency:build-classpath", "-Dmdep.includeScope="+scope)
		if err != nil && ctx.Err() != nil {
			return nil, err
		}
		if err != nil && opts.Logf != nil {
			opts.Logf("project: maven: %v", err)
		}
		classPaths[scope] = parseBuildClasspath(out)
	}
	w := fromMaven(root, projects, classPaths, localRepository())
	if len(w.Modules) == 0 {
		return nil, errors.New("maven: the build has no modules with sources")
	}
	saveCache(dir, key, w)
	return w, nil
}

// mavenCommand returns the build's wrapper, or else mvn on PATH.
func mavenCommand(root string) (string, error) {
	name := "mvnw"
	if runtime.GOOS == "windows" {
		name = "mvnw.cmd"
	}
	if fi, err := os.Stat(filepath.Join(root, name)); err == nil && !fi.IsDir() {
		return filepath.Join(root, name), nil
	}
	if p, err := exec.LookPath("mvn"); err == nil {
		return p, nil
	}
	return "", errors.New("maven: no mvnw in the workspace and no mvn on PATH")
}

// pomProject is the part of a module's effective POM kcls reads.
type pomProject struct {
	GroupID      string    `xml:"groupId"`
	ArtifactID   string    `xml:"artifactId"`
	Version      string    `xml:"version"`
	Packaging    string    `xml:"packaging"`
	Parent       pomParent `xml:"parent"`
	Properties   xmlNode   `xml:"properties"`
	Dependencies []pomDep  `xml:"dependencies>dependency"`
	Build        pomBuild  `xml:"build"`
}

type pomParent struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

type pomDep struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Type       string `xml:"type"`
	Classifier string `xml:"classifier"`
	Scope      string `xml:"scope"`
	Optional   string `xml:"optional"`
}

type pomBuild struct {
	Directory           string      `xml:"directory"`
	OutputDirectory     string      `xml:"outputDirectory"`
	TestOutputDirectory string      `xml:"testOutputDirectory"`
	SourceDirectory     string      `xml:"sourceDirectory"`
	TestSourceDirectory string      `xml:"testSourceDirectory"`
	Resources           []string    `xml:"resources>resource>directory"`
	TestResources       []string    `xml:"testResources>testResource>directory"`
	Plugins             []pomPlugin `xml:"plugins>plugin"`
}

type pomPlugin struct {
	GroupID       string  `xml:"groupId"`
	ArtifactID    string  `xml:"artifactId"`
	Configuration xmlNode `xml:"configuration"`
	Executions    []struct {
		ID            string   `xml:"id"`
		Phase         string   `xml:"phase"`
		Goals         []string `xml:"goals>goal"`
		Configuration xmlNode  `xml:"configuration"`
	} `xml:"executions>execution"`
}

// xmlNode is any XML element.
type xmlNode struct {
	XMLName xml.Name
	Text    string    `xml:",chardata"`
	Nodes   []xmlNode `xml:",any"`
}

// child returns the first child element named name.
func (n xmlNode) child(name string) (xmlNode, bool) {
	for _, c := range n.Nodes {
		if c.XMLName.Local == name {
			return c, true
		}
	}
	return xmlNode{}, false
}

// value returns the text of the child element named name.
func (n xmlNode) value(name string) string {
	c, _ := n.child(name)
	return strings.TrimSpace(c.Text)
}

// values returns the texts of the children of the child element named
// name: the values of a list parameter, as <args><arg>…</arg></args>.
func (n xmlNode) values(name string) []string {
	c, ok := n.child(name)
	if !ok {
		return nil
	}
	var out []string
	for _, v := range c.Nodes {
		if t := strings.TrimSpace(v.Text); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseEffectivePOM reads help:effective-pom's output: one <project>, or
// a reactor's <projects>.
func parseEffectivePOM(b []byte) ([]pomProject, error) {
	// The output may start with a comment, and Maven 3 wraps a reactor's
	// projects in <projects>.
	var multi struct {
		XMLName  xml.Name
		Projects []pomProject `xml:"project"`
	}
	if err := xml.Unmarshal(b, &multi); err != nil {
		return nil, fmt.Errorf("maven: effective POM: %w", err)
	}
	if multi.XMLName.Local == "projects" {
		return multi.Projects, nil
	}
	var one pomProject
	if err := xml.Unmarshal(b, &one); err != nil {
		return nil, fmt.Errorf("maven: effective POM: %w", err)
	}
	return []pomProject{one}, nil
}

var (
	classpathGoal = regexp.MustCompile(`--- \S*:build-classpath \([^)]*\) @ (\S+) ---`)
)

// parseBuildClasspath reads the class paths in the log of
// dependency:build-classpath, by artifact ID.
func parseBuildClasspath(log string) map[string][]string {
	out := map[string][]string{}
	current := ""
	sc := bufio.NewScanner(strings.NewReader(log))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	next := false
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if next {
			next = false
			if !strings.HasPrefix(line, "[") {
				var cp []string
				if line != "" {
					cp = filepath.SplitList(line)
				}
				out[current] = cp
				continue
			}
			out[current] = nil
		}
		if m := classpathGoal.FindStringSubmatch(line); m != nil {
			current = m[1]
			continue
		}
		if current != "" && strings.HasSuffix(line, "Dependencies classpath:") {
			next = true
		}
	}
	if next {
		out[current] = nil
	}
	return out
}

// localRepository returns Maven's local repository: the default, in the
// user's home.
func localRepository() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".m2", "repository")
}

// kotlinSettings are the kotlin-maven-plugin's settings of a module's
// main or test compile.
type kotlinSettings struct {
	jvmTarget, moduleName, explicitAPI string
	sourceDirs, optIns, plugins        []string
	multiPlatform                      bool
}

// kotlinConfig returns the Kotlin settings of a module's compile or
// test-compile goal: the plugin's configuration, the goal's execution's
// over it, and the kotlin.compiler properties.
func kotlinConfig(p pomProject, goal string) kotlinSettings {
	var k kotlinSettings
	k.jvmTarget = p.Properties.value("kotlin.compiler.jvmTarget")
	apply := func(c xmlNode) {
		if v := c.value("jvmTarget"); v != "" {
			k.jvmTarget = v
		}
		if v := c.value("moduleName"); v != "" {
			k.moduleName = v
		}
		k.sourceDirs = append(k.sourceDirs, c.values("sourceDirs")...)
		for _, a := range c.values("args") {
			switch {
			case strings.HasPrefix(a, "-Xexplicit-api="):
				k.explicitAPI = strings.TrimPrefix(a, "-Xexplicit-api=")
			case strings.HasPrefix(a, "-opt-in="), strings.HasPrefix(a, "-Xopt-in="):
				k.optIns = append(k.optIns, a[strings.IndexByte(a, '=')+1:])
			case a == "-Xmulti-platform":
				k.multiPlatform = true
			}
		}
		for _, pl := range c.values("compilerPlugins") {
			switch pl {
			case "kotlinx-serialization":
				pl = "serialization"
			}
			if !slices.Contains(k.plugins, pl) {
				k.plugins = append(k.plugins, pl)
			}
		}
	}
	for _, pl := range p.Build.Plugins {
		if pl.ArtifactID != "kotlin-maven-plugin" {
			continue
		}
		apply(pl.Configuration)
		for _, e := range pl.Executions {
			if slices.Contains(e.Goals, goal) {
				apply(e.Configuration)
			}
		}
	}
	return k
}

// extraSources returns the source directories build-helper-maven-plugin
// adds with its add-source or add-test-source goal.
func extraSources(p pomProject, goal string) []string {
	var out []string
	for _, pl := range p.Build.Plugins {
		if pl.ArtifactID != "build-helper-maven-plugin" {
			continue
		}
		for _, e := range pl.Executions {
			if slices.Contains(e.Goals, goal) {
				out = append(out, e.Configuration.values("sources")...)
			}
		}
	}
	return out
}

// fromMaven turns the effective POMs and class paths into a workspace:
// a main and a test module for each module with sources.
func fromMaven(root string, projects []pomProject, classPaths map[string]map[string][]string, repo string) *Workspace {
	w := &Workspace{Root: root, Source: "maven"}
	reactor := map[string]pomProject{} // group:artifact
	for _, p := range projects {
		reactor[groupOf(p)+":"+p.ArtifactID] = p
	}
	// reactorJar reports whether a class path entry is a reactor module's
	// installed jar or its classes.
	reactorJar := func(e string) bool {
		for _, p := range projects {
			if e == p.Build.OutputDirectory || e == p.Build.TestOutputDirectory {
				return true
			}
			if repo != "" && within(e, filepath.Join(repo, filepath.FromSlash(strings.ReplaceAll(groupOf(p), ".", "/")), p.ArtifactID)) {
				return true
			}
		}
		return false
	}
	var tests []*Module
	for _, p := range projects {
		if p.Packaging == "pom" {
			continue
		}
		base := filepath.Dir(p.Build.Directory)
		if p.Build.Directory == "" {
			base = filepath.Dir(filepath.Dir(filepath.Dir(p.Build.SourceDirectory)))
		}
		var deps []string // the reactor modules it depends on
		for _, d := range p.Dependencies {
			if q, ok := reactor[d.GroupID+":"+d.ArtifactID]; ok && q.Packaging != "pom" && d.Scope != "test" {
				deps = append(deps, q.ArtifactID+":main")
			}
		}
		for _, test := range []bool{false, true} {
			goal, scope, id := "compile", "compile", p.ArtifactID+":main"
			srcDir, kotlinDir, helper, out := p.Build.SourceDirectory, filepath.Join(base, "src", "main", "kotlin"), "add-source", p.Build.OutputDirectory
			if test {
				goal, scope, id = "test-compile", "test", p.ArtifactID+":test"
				srcDir, kotlinDir, helper, out = p.Build.TestSourceDirectory, filepath.Join(base, "src", "test", "kotlin"), "add-test-source", p.Build.TestOutputDirectory
			}
			k := kotlinConfig(p, goal)
			var roots []string
			for _, r := range append(append(k.sourceDirs, srcDir, kotlinDir), extraSources(p, helper)...) {
				if r == "" {
					continue
				}
				if !filepath.IsAbs(r) {
					r = filepath.Join(base, r)
				}
				r = filepath.Clean(r)
				if fi, err := os.Stat(r); err == nil && fi.IsDir() && !slices.ContainsFunc(roots, func(x string) bool { return within(r, x) }) {
					roots = append(roots, r)
				}
			}
			if len(roots) == 0 {
				continue
			}
			cp, resolved := classPaths[scope][p.ArtifactID]
			if !resolved {
				cp = directDependencies(p, test, repo)
			}
			m := &Module{
				ID: id, ModuleName: p.ArtifactID, Roots: roots, Dir: base, Output: out,
				JVMTarget: k.jvmTarget, ExplicitAPI: k.explicitAPI, Plugins: k.plugins, OptIns: k.optIns, MultiPlatform: k.multiPlatform,
				DependsOn: slices.Clone(deps),
			}
			if k.moduleName != "" {
				m.ModuleName = k.moduleName
			}
			for _, e := range cp {
				if !reactorJar(e) {
					m.ClassPath = append(m.ClassPath, e)
				}
			}
			if test {
				m.ModuleName += "_test"
				if w.ByID(p.ArtifactID+":main") != nil {
					m.DependsOn = append([]string{p.ArtifactID + ":main"}, m.DependsOn...)
					m.FriendPaths = []string{p.Build.OutputDirectory}
					m.ClassPath = append([]string{p.Build.OutputDirectory}, m.ClassPath...)
				}
				tests = append(tests, m)
				continue
			}
			w.Modules = append(w.Modules, m)
		}
	}
	w.Modules = append(w.Modules, tests...)
	for _, m := range w.Modules {
		m.DependsOn = slices.DeleteFunc(m.DependsOn, func(id string) bool { return w.ByID(id) == nil })
	}
	return w
}

// groupOf returns a module's group ID, which it may inherit.
func groupOf(p pomProject) string {
	if p.GroupID != "" {
		return p.GroupID
	}
	return p.Parent.GroupID
}

// directDependencies returns the jars in the local repository of a
// module's direct dependencies, as a class path when Maven couldn't
// resolve it.
func directDependencies(p pomProject, test bool, repo string) []string {
	if repo == "" {
		return nil
	}
	var out []string
	for _, d := range p.Dependencies {
		switch d.Scope {
		case "", "compile", "provided", "system":
		case "runtime", "import":
			continue
		case "test":
			if !test {
				continue
			}
		}
		if d.Type != "" && d.Type != "jar" || d.Version == "" {
			continue
		}
		name := d.ArtifactID + "-" + d.Version
		if d.Classifier != "" {
			name += "-" + d.Classifier
		}
		jar := filepath.Join(repo, filepath.FromSlash(strings.ReplaceAll(d.GroupID, ".", "/")), d.ArtifactID, d.Version, name+".jar")
		if _, err := os.Stat(jar); err == nil {
			out = append(out, jar)
		}
	}
	return out
}
