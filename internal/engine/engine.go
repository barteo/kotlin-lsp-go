// Package engine analyzes the modules of kcls's workspaces: it keeps
// each module's class path open, analyzes the module's current files on
// request, shares one analysis between the requests that want the same
// files, and cancels an analysis that an edit made out of date.
//
// A module is analyzed with the sources of the modules it depends on, so
// that their declarations resolve to their sources whether or not the
// build has compiled them; only the module's own files' diagnostics are
// its own.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/project"
)

// Files gives the engine the text of files: the client's for a file it
// opened, else the file on disk.
type Files interface {
	File(path string) (*features.File, error)
}

// Options configure an engine.
type Options struct {
	Files Files
	// Logf logs what the engine does; nil discards it.
	Logf func(format string, args ...any)
	// CacheDir holds Gradle models and extracted library sources; empty
	// for project.CacheDir().
	CacheDir string
	// NoImport never runs Gradle: a Gradle workspace without a cached
	// model is a folder.
	NoImport bool
	// Progress, if set, reports long work, as a Gradle import; the
	// function it returns ends the report.
	Progress func(title string) (done func(message string))
	// OnModel is called when a workspace's model changes, as after an
	// import, with the import's error if it failed.
	OnModel func(root string, err error)
	// Jobs is how many goroutines an analysis uses; 0 means GOMAXPROCS.
	Jobs int
	// SourcesTTL is how long a module's list of source files is trusted
	// without a file event; 0 means 2 seconds. Agents create files
	// without telling the server.
	SourcesTTL time.Duration
}

// Engine analyzes the modules of workspaces.
type Engine struct {
	opts    Options
	logf    func(string, ...any)
	ctx     context.Context // cancelled by Close
	stop    context.CancelFunc
	libs    *librarySources
	symbols *symbolIndex

	mu      sync.Mutex
	roots   []*root
	singles map[string]*module // single-file modules, by path

	textMu sync.Mutex
	texts  map[string]cachedText // files' texts for searches
}

// root is a workspace folder.
type root struct {
	path        string
	ws          *project.Workspace
	provisional bool          // a folder model while the first import runs
	importing   chan struct{} // closed when the running import ends; nil if none
	cancel      context.CancelFunc
	modules     map[string]*module
}

// New returns an engine with no workspaces.
func New(opts Options) *Engine {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.CacheDir == "" {
		opts.CacheDir = project.CacheDir()
	}
	if opts.Jobs <= 0 {
		opts.Jobs = runtime.GOMAXPROCS(0)
	}
	if opts.SourcesTTL == 0 {
		opts.SourcesTTL = 2 * time.Second
	}
	ctx, stop := context.WithCancel(context.Background())
	e := &Engine{opts: opts, logf: opts.Logf, ctx: ctx, stop: stop, singles: map[string]*module{}, texts: map[string]cachedText{}, symbols: newSymbolIndex()}
	e.libs = newLibrarySources(filepath.Join(opts.CacheDir, "sources"), opts.Logf)
	return e
}

// Close cancels the engine's work and releases its class paths.
func (e *Engine) Close() {
	e.stop()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.roots {
		if r.cancel != nil {
			r.cancel()
		}
		closeModules(r.modules)
	}
	closeModules(e.singles)
	e.roots, e.singles = nil, map[string]*module{}
}

func closeModules(ms map[string]*module) {
	for _, m := range ms {
		m.close()
	}
}

// AddRoot adds the workspace folder at path. Its model is what is known
// without running a build; a Gradle build whose model isn't cached, or
// whose build files changed, is imported in the background.
func (e *Engine) AddRoot(path string) {
	ws, stale, err := project.Load(path, e.opts.CacheDir)
	if err != nil {
		e.logf("engine: %s: %v; analyzing it as a folder", path, err)
		ws, stale = project.Folder(path), false
		if e.opts.OnModel != nil {
			go e.opts.OnModel(path, err)
		}
	}
	r := &root{path: path, ws: ws, modules: map[string]*module{}}
	e.logf("engine: %s: %s model, %d modules", path, ws.Source, len(ws.Modules))
	e.mu.Lock()
	e.roots = append(e.roots, r)
	e.mu.Unlock()
	if stale && !e.opts.NoImport {
		e.mu.Lock()
		r.provisional = ws.Source == "folder"
		e.mu.Unlock()
		e.startImport(r)
	}
}

// RemoveRoot drops the workspace folder at path.
func (e *Engine) RemoveRoot(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roots = slices.DeleteFunc(e.roots, func(r *root) bool {
		if r.path != path {
			return false
		}
		if r.cancel != nil {
			r.cancel()
		}
		closeModules(r.modules)
		return true
	})
}

// Roots returns the workspace folders' paths.
func (e *Engine) Roots() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, r := range e.roots {
		out = append(out, r.path)
	}
	return out
}

// Workspace returns the model of the workspace folder at path, or nil.
func (e *Engine) Workspace(path string) *project.Workspace {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.roots {
		if r.path == path {
			return r.ws
		}
	}
	return nil
}

// startImport imports r's Gradle build in the background, cancelling an
// import already running. The caller must not hold e.mu.
func (e *Engine) startImport(r *root) {
	e.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	ctx, cancel := context.WithTimeout(e.ctx, 30*time.Minute)
	done := make(chan struct{})
	r.cancel, r.importing = cancel, done
	e.mu.Unlock()
	go func() {
		defer cancel()
		end := func(string) {}
		if e.opts.Progress != nil {
			end = e.opts.Progress("Importing the Gradle build of " + filepath.Base(r.path))
		}
		start := time.Now()
		ws, err := project.ImportGradle(ctx, r.path, project.GradleOptions{Logf: e.logf, CacheDir: e.opts.CacheDir})
		e.mu.Lock()
		superseded := r.importing != done
		if !superseded {
			r.importing, r.cancel = nil, nil
			if err == nil {
				closeModules(r.modules)
				r.ws, r.modules = ws, map[string]*module{}
			}
			r.provisional = false
		}
		e.mu.Unlock()
		close(done)
		if superseded || errors.Is(err, context.Canceled) && e.ctx.Err() != nil {
			end("")
			return
		}
		if err != nil {
			e.logf("engine: import of %s: %v", r.path, err)
			end("failed")
		} else {
			e.logf("engine: imported %s in %v: %d modules", r.path, time.Since(start).Round(time.Millisecond), len(ws.Modules))
			end(fmt.Sprintf("%d modules", len(ws.Modules)))
		}
		if e.opts.OnModel != nil {
			e.opts.OnModel(r.path, err)
		}
	}()
}

// Reimport reads every workspace's model again: its kcls.json, or its
// Gradle build, which is imported in the background.
func (e *Engine) Reimport() {
	e.mu.Lock()
	roots := slices.Clone(e.roots)
	e.mu.Unlock()
	for _, r := range roots {
		e.reimport(r)
	}
}

func (e *Engine) reimport(r *root) {
	switch project.Detect(r.path) {
	case "gradle":
		if !e.opts.NoImport {
			e.startImport(r)
			return
		}
	}
	ws, _, err := project.Load(r.path, e.opts.CacheDir)
	if err != nil {
		ws = project.Folder(r.path)
	}
	e.mu.Lock()
	closeModules(r.modules)
	r.ws, r.modules, r.provisional = ws, map[string]*module{}, false
	e.mu.Unlock()
	if e.opts.OnModel != nil {
		e.opts.OnModel(r.path, err)
	}
}

// ClearCaches forgets every analysis, class path, model and extracted
// library source, and imports the workspaces again.
func (e *Engine) ClearCaches() {
	e.mu.Lock()
	roots := slices.Clone(e.roots)
	for _, r := range roots {
		closeModules(r.modules)
		r.modules = map[string]*module{}
		project.ClearCache(e.opts.CacheDir, r.path)
	}
	closeModules(e.singles)
	e.singles = map[string]*module{}
	e.mu.Unlock()
	e.libs.clear()
	e.symbols.clear()
	e.Reimport()
}

// Changed tells the engine that the file at path changed on disk:
// created, changed or deleted. A build file's change reimports its
// workspace; a source's creation or deletion refreshes its module's list
// of files.
func (e *Engine) Changed(path string, created, deleted bool) {
	if project.IsBuildFile(path) {
		e.mu.Lock()
		var r *root
		for _, x := range e.roots {
			if within(path, x.path) {
				r = x
			}
		}
		e.mu.Unlock()
		if r != nil {
			e.logf("engine: %s changed: reimporting %s", path, r.path)
			e.reimport(r)
		}
		return
	}
	if !created && !deleted || !project.IsSource(path) {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.roots {
		for _, m := range r.modules {
			m.invalidateSources()
		}
	}
}

// Busy reports whether a workspace's model is being imported.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.roots {
		if r.importing != nil {
			return true
		}
	}
	return false
}

// IsLibrarySource reports whether path is a library source the engine
// extracted from a sources jar, which is not analyzed.
func (e *Engine) IsLibrarySource(path string) bool {
	return within(path, e.libs.dir)
}

// Analyze returns the analysis of the module of the file at path, of
// its current files. A request while the workspace's first import runs
// waits for it.
func (e *Engine) Analyze(ctx context.Context, path string) (*features.Analysis, error) {
	if !strings.HasSuffix(path, ".kt") {
		return nil, fmt.Errorf("%s: not a Kotlin source", filepath.Base(path))
	}
	if e.IsLibrarySource(path) {
		return nil, errors.New("library sources are not analyzed")
	}
	m, err := e.module(ctx, path)
	if err != nil {
		return nil, err
	}
	return m.analyze(ctx, path)
}

// module returns the module of the file at path, waiting for its
// workspace's first import.
func (e *Engine) module(ctx context.Context, path string) (*module, error) {
	for {
		e.mu.Lock()
		var r *root
		for _, x := range e.roots {
			if within(path, x.path) && (r == nil || len(x.path) > len(r.path)) {
				r = x
			}
		}
		if r != nil && r.provisional && r.importing != nil {
			wait := r.importing
			e.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		defer e.mu.Unlock()
		if r != nil {
			if pm := r.ws.Module(path); pm != nil {
				m := r.modules[pm.ID]
				if m == nil {
					m = e.newModule(r, pm)
					r.modules[pm.ID] = m
				}
				return m, nil
			}
		}
		m := e.singles[path]
		if m == nil {
			m = e.newModule(nil, project.SingleFile(path))
			e.singles[path] = m
		}
		return m, nil
	}
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// exists reports whether there is a file at path.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
