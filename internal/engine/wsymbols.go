package engine

import (
	"archive/zip"
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// symbolLimit bounds the symbols a workspace query returns, and
// librarySymbolLimit those of them from libraries, which each need a
// sources jar's file extracted to locate.
const (
	symbolLimit        = 200
	librarySymbolLimit = 50
)

// symbolIndex caches the declarations of the workspace's files and the
// class names of its libraries.
type symbolIndex struct {
	mu    sync.Mutex
	files map[string]fileSymbols // by path
	jars  map[string]*jarClasses // by jar path
}

type fileSymbols struct {
	f    *features.File // the parse the symbols are of
	syms []protocol.SymbolInformation
}

// jarClasses are the top-level classes of a jar of classes, or of a
// JDK's src.zip, by binary name.
type jarClasses struct {
	mod     time.Time
	size    int64
	once    sync.Once
	classes []string
}

func newSymbolIndex() *symbolIndex {
	return &symbolIndex{files: map[string]fileSymbols{}, jars: map[string]*jarClasses{}}
}

// WorkspaceSymbols returns the declarations in the workspace's Kotlin
// files, and the classes of its libraries that have sources, whose names
// match query, best first.
func (e *Engine) WorkspaceSymbols(ctx context.Context, query string) ([]protocol.SymbolInformation, error) {
	mods := e.allModules()
	var ms []features.Match
	seen := map[string]bool{}
	for _, m := range mods {
		for _, p := range m.sources()[m.pm] {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if seen[p] || !strings.HasSuffix(p, ".kt") {
				continue
			}
			seen[p] = true
			for _, s := range e.fileSymbols(p) {
				if s.Kind == protocol.SymbolConstructor {
					continue
				}
				name := s.Name[strings.LastIndexByte(s.Name, '.')+1:] // less an extension's receiver
				if score, ok := features.MatchScore(query, name); ok {
					ms = append(ms, features.Match{Symbol: s, Score: score})
				}
			}
		}
	}
	if len(query) >= 2 {
		ms = append(ms, e.librarySymbols(ctx, mods, query)...)
	}
	return features.BestMatches(ms, symbolLimit), nil
}

// allModules returns the modules of every workspace folder.
func (e *Engine) allModules() []*module {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*module
	for _, r := range e.roots {
		for _, pm := range r.ws.Modules {
			m := r.modules[pm.ID]
			if m == nil {
				m = e.newModule(r, pm)
				r.modules[pm.ID] = m
			}
			out = append(out, m)
		}
	}
	return out
}

// fileSymbols returns the declarations of the file at path, parsed again
// only when it changed.
func (e *Engine) fileSymbols(path string) []protocol.SymbolInformation {
	f, err := e.opts.Files.File(path)
	if err != nil {
		return nil
	}
	ix := e.symbols
	ix.mu.Lock()
	c, ok := ix.files[path]
	ix.mu.Unlock()
	if ok && c.f == f {
		return c.syms
	}
	syms := features.WorkspaceSymbols(f)
	ix.mu.Lock()
	ix.files[path] = fileSymbols{f, syms}
	ix.mu.Unlock()
	return syms
}

// librarySymbols returns the classes of the modules' libraries whose
// simple names match query, located in their sources.
func (e *Engine) librarySymbols(ctx context.Context, mods []*module, query string) []features.Match {
	type cand struct {
		bin   string
		score int
		m     *module
	}
	var cands []cand
	seen := map[string]bool{}
	for _, m := range mods {
		jars := slices.Clone(m.pm.ClassPath)
		for _, j := range m.sourceJars() {
			if strings.HasSuffix(j, "src.zip") {
				jars = append(jars, j)
			}
		}
		for _, jar := range jars {
			if !strings.HasSuffix(jar, ".jar") && !strings.HasSuffix(jar, ".zip") {
				continue
			}
			for _, bin := range e.symbols.classes(jar) {
				if seen[bin] {
					continue
				}
				simple := bin[strings.LastIndexByte(bin, '/')+1:]
				if score, ok := features.MatchScore(query, simple); ok {
					seen[bin] = true
					cands = append(cands, cand{bin, score - 50, m}) // sources first
				}
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.bin, b.bin)
	})
	var out []features.Match
	for _, c := range cands {
		if len(out) == librarySymbolLimit || ctx.Err() != nil {
			break
		}
		libs := e.libs.forJars(c.m.sourceJars(), e.opts.Files)
		t, ok := libs.Find(ctx, features.LibraryDecl{Kind: "class", Class: c.bin, Parameters: -1})
		if !ok {
			continue
		}
		r := t.File.Mapper.Range(t.Start, t.End)
		if t.Node.Valid() {
			if id := t.Node.NameIdentifier(); id.Valid() {
				r = t.File.Range(id)
			}
		}
		i := strings.LastIndexByte(c.bin, '/')
		out = append(out, features.Match{Score: c.score, Symbol: protocol.SymbolInformation{
			Name: c.bin[i+1:], Kind: protocol.SymbolClass, ContainerName: strings.ReplaceAll(c.bin[:max(i, 0)], "/", "."),
			Location: protocol.Location{URI: t.File.URI, Range: r},
		}})
	}
	return out
}

// classes returns the binary names of a jar's top-level classes, but
// for Kotlin's file facades, or those of a src.zip's Java files.
func (ix *symbolIndex) classes(jar string) []string {
	fi, err := os.Stat(jar)
	if err != nil {
		return nil
	}
	ix.mu.Lock()
	c := ix.jars[jar]
	if c == nil || !c.mod.Equal(fi.ModTime()) || c.size != fi.Size() {
		c = &jarClasses{mod: fi.ModTime(), size: fi.Size()}
		ix.jars[jar] = c
	}
	ix.mu.Unlock()
	c.once.Do(func() { c.classes = listClasses(jar) })
	return c.classes
}

func listClasses(jar string) []string {
	z, err := zip.OpenReader(jar)
	if err != nil {
		return nil
	}
	defer z.Close()
	var out []string
	for _, f := range z.File {
		name := f.Name
		var bin string
		switch {
		case strings.HasSuffix(name, ".class"):
			bin = strings.TrimSuffix(name, ".class")
			if strings.HasPrefix(bin, "META-INF/") || strings.HasSuffix(bin, "Kt") {
				continue
			}
		case strings.HasSuffix(name, ".java") && strings.HasSuffix(jar, "src.zip"):
			bin = strings.TrimSuffix(name, ".java")
			if i := strings.IndexByte(bin, '/'); i > 0 && strings.Contains(bin[:i], ".") {
				bin = bin[i+1:] // java.base/java/lang/String
			}
		default:
			continue
		}
		base := bin[strings.LastIndexByte(bin, '/')+1:]
		if strings.ContainsRune(base, '$') || base == "module-info" || base == "package-info" {
			continue
		}
		out = append(out, bin)
	}
	return out
}

// clear drops the cached symbols.
func (ix *symbolIndex) clear() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.files = map[string]fileSymbols{}
	ix.jars = map[string]*jarClasses{}
}

// LibraryClasses returns the binary names of the top-level classes of the
// libraries of the module of the file at path, and of its JDK's src.zip,
// whose simple names match.
func (e *Engine) LibraryClasses(ctx context.Context, path string, match func(simple string) bool) []string {
	m, err := e.module(ctx, path)
	if err != nil {
		return nil
	}
	jars := slices.Clone(m.pm.ClassPath)
	for _, j := range m.sourceJars() {
		if strings.HasSuffix(j, "src.zip") {
			jars = append(jars, j)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, jar := range jars {
		if !strings.HasSuffix(jar, ".jar") && !strings.HasSuffix(jar, ".zip") {
			continue
		}
		for _, bin := range e.symbols.classes(jar) {
			if !seen[bin] && match(bin[strings.LastIndexByte(bin, '/')+1:]) {
				seen[bin] = true
				out = append(out, bin)
			}
		}
	}
	return out
}
