package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/features"
	"github.com/barteo/kotlin-lsp-go/internal/project"
)

// incremental is unset by KCLS_INCREMENTAL=0: each analysis checks all
// the module's files, as before sessions.
var incremental = os.Getenv("KCLS_INCREMENTAL") != "0"

// module is a module the engine analyzes.
type module struct {
	e    *Engine
	pm   *project.Module
	deps []*project.Module // the modules it depends on, transitively
	// cross reports that some of deps are another workspace folder's.
	cross bool

	libMu  sync.Mutex
	lib    *analysis.Library
	sess   *analysis.Session // the library's, which analyses update
	libErr error
	srcJar []string // sources jars and src.zip for its class path

	mu       sync.Mutex
	srcs     map[*project.Module][]string // a module's source files
	srcsAt   time.Time
	last     *features.Analysis
	lastKey  []*features.File
	lastGen  uint64 // the generation of the run that made last
	running  *run
	gen      uint64 // the last run's generation
	analyses int
}

// run is an analysis of some files, which the requests for those files
// share.
type run struct {
	gen    uint64 // runs started later have higher generations
	key    []*features.File
	cancel context.CancelFunc
	done   chan struct{}
	res    *features.Analysis
	err    error
}

func (e *Engine) newModule(r *root, pm *project.Module) *module {
	m := &module{e: e, pm: pm}
	if r != nil {
		seen := map[string]bool{pm.ID: true}
		var walk func(*project.Module)
		walk = func(x *project.Module) {
			for _, id := range x.DependsOn {
				if d := r.ws.ByID(id); d != nil && !seen[id] {
					seen[id] = true
					m.deps = append(m.deps, d)
					walk(d)
				}
			}
		}
		walk(pm)
		for _, e := range pm.ClassPath {
			if j := r.ws.SourceJars[e]; j != "" {
				m.srcJar = append(m.srcJar, j)
			}
		}
		// The modules of other workspace folders whose classes it uses,
		// as an included build's, and theirs.
		for _, o := range e.roots {
			if o == r {
				continue
			}
			for _, q := range o.ws.Modules {
				if seen[o.path+"\x00"+q.ID] || !builds(q, pm.ClassPath) {
					continue
				}
				seen[o.path+"\x00"+q.ID] = true
				m.deps = append(m.deps, q)
				m.cross = true
				for _, id := range q.DependsOn {
					if d := o.ws.ByID(id); d != nil && !seen[o.path+"\x00"+id] {
						seen[o.path+"\x00"+id] = true
						m.deps = append(m.deps, d)
					}
				}
			}
		}
	}
	return m
}

// builds reports whether a class path has the classes of a module that
// isn't a test module: its output directory, or a jar its build writes
// (Gradle's build/libs, Maven's target).
func builds(q *project.Module, classPath []string) bool {
	id := strings.ToLower(q.ID[strings.LastIndexByte(q.ID, ':')+1:])
	if strings.Contains(id, "test") {
		return false
	}
	for _, e := range classPath {
		switch {
		case q.Output != "" && within(e, q.Output):
			return true
		case q.Dir != "" && strings.HasSuffix(e, ".jar") &&
			(filepath.Dir(e) == filepath.Join(q.Dir, "build", "libs") || filepath.Dir(e) == filepath.Join(q.Dir, "target")):
			return true
		}
	}
	return false
}

func (m *module) close() {
	m.mu.Lock()
	if m.running != nil {
		m.running.cancel()
	}
	m.mu.Unlock()
	m.libMu.Lock()
	defer m.libMu.Unlock()
	if m.sess != nil {
		m.sess.Close()
		m.sess = nil
	}
	if m.lib != nil {
		m.lib.Close()
		m.lib = nil
	}
	m.libErr = errors.New("module closed")
}

func (m *module) invalidateSources() {
	m.mu.Lock()
	m.srcs = nil
	m.mu.Unlock()
}

// config returns the analysis configuration of the module: its class
// path less the outputs of the modules whose sources it is analyzed with.
func (m *module) config() analysis.Config {
	pm := m.pm
	cfg := analysis.Config{
		JDKHome: pm.JDKHome, ModuleName: pm.ModuleName, NoJDK: pm.NoJDK, Jobs: m.e.opts.Jobs,
		ExplicitAPIMode: pm.ExplicitAPI, OptIns: pm.OptIns, JVMTarget: pm.JVMTarget,
	}
	if !cfg.NoJDK && cfg.JDKHome == "" {
		cfg.JDKHome = project.FindJDK()
	}
	for _, p := range pm.Plugins {
		switch p {
		case "serialization":
			cfg.Serialization = true
		case "parcelize":
			cfg.Parcelize = true
		case "compose":
			cfg.Compose = true
		}
	}
	var builds []string
	for _, d := range m.deps {
		if d.Output != "" {
			builds = append(builds, d.Output)
			if i := strings.Index(d.Output, string(filepath.Separator)+"build"+string(filepath.Separator)); i >= 0 {
				builds = append(builds, d.Output[:i+len("/build")])
			}
		}
	}
	for _, e := range pm.ClassPath {
		if slices.ContainsFunc(builds, func(b string) bool { return within(e, b) }) || !exists(e) {
			continue
		}
		cfg.ClassPath = append(cfg.ClassPath, e)
		if slices.Contains(pm.FriendPaths, e) {
			cfg.FriendPaths = append(cfg.FriendPaths, e)
		}
	}
	return cfg
}

// library opens the module's class path, once, and the session its
// analyses update.
func (m *module) library() (*analysis.Library, *analysis.Session, error) {
	m.libMu.Lock()
	defer m.libMu.Unlock()
	if m.lib != nil || m.libErr != nil {
		return m.lib, m.sess, m.libErr
	}
	cfg := m.config()
	start := time.Now()
	lib, err := analysis.OpenLibrary(cfg)
	if err != nil && cfg.JVMTarget != "" {
		m.e.logf("engine: %s: %v; analyzing with the default JVM target", m.pm.ID, err)
		cfg.JVMTarget = ""
		lib, err = analysis.OpenLibrary(cfg)
	}
	if err != nil {
		m.libErr = err
		return nil, nil, err
	}
	m.e.logf("engine: %s: opened %d class path entries in %v", m.pm.ID, len(cfg.ClassPath), time.Since(start).Round(time.Millisecond))
	m.lib, m.sess = lib, lib.Session()
	return m.lib, m.sess, nil
}

// sources returns the source files of the module and of those it depends
// on, listing the directories again when the list is older than the TTL.
func (m *module) sources() map[*project.Module][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srcs != nil && time.Since(m.srcsAt) < m.e.opts.SourcesTTL {
		return m.srcs
	}
	srcs := map[*project.Module][]string{m.pm: m.pm.Sources()}
	for _, d := range m.deps {
		srcs[d] = d.Sources()
	}
	m.srcs, m.srcsAt = srcs, time.Now()
	return srcs
}

// files returns the current files to analyze: the module's own Kotlin
// files, path among them, and its Java files and those of its
// dependencies. own are the paths of the module's own Kotlin files.
func (m *module) files(path string) (files []*features.File, own []string, err error) {
	srcs := m.sources()
	add := func(p string, mine bool) error {
		f, err := m.e.opts.Files.File(p)
		if err != nil {
			if p == path {
				return err
			}
			return nil // deleted meanwhile
		}
		files = append(files, f)
		if mine && strings.HasSuffix(p, ".kt") {
			own = append(own, p)
		}
		return nil
	}
	mine := srcs[m.pm]
	if !slices.Contains(mine, path) {
		if err := add(path, true); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range mine {
		if err := add(p, true); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range m.deps {
		for _, p := range srcs[d] {
			add(p, false)
		}
	}
	return files, own, nil
}

// analyze returns the analysis of the module's current files.
func (m *module) analyze(ctx context.Context, path string) (*features.Analysis, error) {
	for {
		files, own, err := m.files(path)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.last != nil && slices.Equal(m.lastKey, files) {
			a := m.last
			m.mu.Unlock()
			return a, nil
		}
		r := m.running
		stale := r != nil && !slices.Equal(r.key, files)
		first := stale && m.last == nil
		switch {
		case first:
			// The module's first analysis, which checks every file, is
			// let finish: the next one starts from it.
		case stale:
			r.cancel() // out of date
			r = m.start(files, own)
		case r == nil:
			r = m.start(files, own)
		}
		m.mu.Unlock()
		select {
		case <-r.done:
			if first || r.err != nil && errors.Is(r.err, context.Canceled) && ctx.Err() == nil && m.e.ctx.Err() == nil {
				continue // superseded by newer files
			}
			return r.res, r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// start starts analyzing files. The caller holds m.mu.
func (m *module) start(files []*features.File, own []string) *run {
	ctx, cancel := context.WithCancel(m.e.ctx)
	m.gen++
	r := &run{gen: m.gen, key: files, cancel: cancel, done: make(chan struct{})}
	m.running = r
	m.analyses++
	go func() {
		defer cancel()
		res, err := m.run(ctx, files, own, nil)
		m.mu.Lock()
		if m.running == r {
			m.running = nil
		}
		if err == nil && r.gen > m.lastGen {
			// A run that finishes after a later one doesn't replace it.
			m.last, m.lastKey, m.lastGen = res, files, r.gen
		}
		m.mu.Unlock()
		r.res, r.err = res, err
		close(r.done)
	}()
	return r
}

// run analyzes files; complete, if it isn't nil, asks for a completion.
func (m *module) run(ctx context.Context, files []*features.File, own []string, complete *analysis.Complete) (*features.Analysis, error) {
	lib, sess, err := m.library()
	if err != nil {
		return nil, err
	}
	var opts = analysis.Options{Warnings: complete == nil, ExtraWarnings: true, Complete: complete}
	var kt []*features.File
	for _, f := range files {
		switch filepath.Ext(f.Snapshot.Path()) {
		case ".kt":
			kt = append(kt, f)
		case ".java":
			opts.Java = append(opts.Java, analysis.JavaSource{Path: f.Snapshot.Path(), Text: f.Mapper.Text()})
		}
	}
	snaps := make([]*psi.Snapshot, 0, len(kt))
	for _, f := range kt {
		snaps = append(snaps, f.Snapshot)
	}
	if incremental {
		// The modules it depends on are checked by their own analyses:
		// this one only declares their sources.
		mine := make(map[string]bool, len(own))
		for _, p := range own {
			mine[p] = true
		}
		for _, f := range kt {
			if p := f.Snapshot.Path(); !mine[p] {
				if opts.Declared == nil {
					opts.Declared = map[string]bool{}
				}
				opts.Declared[p] = true
			}
		}
	}
	start := time.Now()
	var res *analysis.Result
	how := ""
	if incremental && complete == nil {
		before := sess.Stats().Incremental
		res, err = sess.Update(ctx, snaps, opts)
		if sess.Stats().Incremental > before {
			how = " (incremental)"
		}
	} else {
		res, err = lib.Analyze(ctx, snaps, opts)
	}
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			m.e.logf("engine: %s: %v", m.pm.ID, err)
		}
		return nil, err
	}
	if complete == nil {
		m.e.logf("engine: %s: analyzed %d files%s in %v", m.pm.ID, len(kt), how, time.Since(start).Round(time.Millisecond))
	} else {
		m.e.logf("engine: %s: completion in %v", m.pm.ID, time.Since(start).Round(time.Millisecond))
	}
	var libs features.Libraries
	if jars := m.sourceJars(); len(jars) > 0 {
		libs = m.e.libs.forJars(jars, m.e.opts.Files)
	}
	return features.NewAnalysis(res, m.pm.ID, kt, own, libs), nil
}

// sourceJars returns the sources of the module's libraries: the sources
// jars the build found and the JDK's src.zip.
func (m *module) sourceJars() []string {
	jars := slices.Clone(m.srcJar)
	if !m.pm.NoJDK {
		jdk := m.pm.JDKHome
		if jdk == "" {
			jdk = project.FindJDK()
		}
		if jdk != "" {
			for _, p := range []string{filepath.Join(jdk, "lib", "src.zip"), filepath.Join(jdk, "src.zip")} {
				if exists(p) {
					jars = append(jars, p)
					break
				}
			}
		}
	}
	// The standard library's sources jar sits next to it in a Maven or
	// Gradle layout, or in kc's own distribution.
	for _, e := range m.pm.ClassPath {
		base := filepath.Base(e)
		if !strings.HasPrefix(base, "kotlin-stdlib") || !strings.HasSuffix(base, ".jar") || strings.HasSuffix(base, "-sources.jar") {
			continue
		}
		for _, cand := range siblingSources(e) {
			if exists(cand) && !slices.Contains(jars, cand) {
				jars = append(jars, cand)
			}
		}
	}
	return jars
}

// siblingSources returns where a jar's sources jar may be: next to it,
// or, in Gradle's cache, in a sibling hash directory.
func siblingSources(jar string) []string {
	name := strings.TrimSuffix(filepath.Base(jar), ".jar") + "-sources.jar"
	out := []string{filepath.Join(filepath.Dir(jar), name)}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(jar)), "*", name)); err == nil {
		out = append(out, matches...)
	}
	return out
}

// Stats are counters for tests and logs.
type Stats struct {
	// Analyses are the analyses started, but completions; Incremental
	// the updates of the module's session that checked only some files.
	Analyses, Incremental int
}

// Stats returns the counters of the module of path.
func (e *Engine) Stats(path string) (Stats, error) {
	m, err := e.module(context.Background(), path)
	if err != nil {
		return Stats{}, err
	}
	m.libMu.Lock()
	sess := m.sess
	m.libMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Stats{Analyses: m.analyses}
	if sess != nil {
		st.Incremental = int(sess.Stats().Incremental)
	}
	return st, nil
}
