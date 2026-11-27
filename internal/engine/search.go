package engine

import (
	"context"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-lsp-go/internal/features"
)

// Texts is implemented by a Files that knows the text of the documents
// the client opened, which a search reads in place of the files on disk.
type Texts interface {
	OpenText(path string) (string, bool)
}

// Scope is a module's analysis and those of its own files a search
// found the words in.
type Scope = features.Scope

// Search returns, for each module that can refer to the declarations in
// the file at from, the module's own Kotlin files that contain one of
// words as a whole word, with the module's analysis: from's module and
// the modules that depend on it. An empty from searches every module.
func (e *Engine) Search(ctx context.Context, words []string, from string) ([]Scope, error) {
	mods, err := e.searchModules(ctx, from)
	if err != nil {
		return nil, err
	}
	type cand struct {
		m     *module
		paths []string
		open  bool // the client opened one of its files
	}
	var cands []cand
	for _, m := range mods {
		c := cand{m: m}
		for _, p := range m.sources()[m.pm] {
			if !strings.HasSuffix(p, ".kt") {
				continue
			}
			text, open, ok := e.text(p)
			c.open = c.open || open
			if ok && containsWord(text, words) {
				c.paths = append(c.paths, p)
			}
		}
		if len(c.paths) > 0 {
			cands = append(cands, c)
		}
	}
	scopes := make([]Scope, len(cands))
	errs := make([]error, len(cands))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			a, err := c.m.analyze(ctx, c.paths[0])
			if err != nil {
				errs[i] = err
				return
			}
			s := Scope{Analysis: a}
			for _, p := range c.paths {
				if f := a.File(p); f != nil {
					s.Files = append(s.Files, f)
				}
			}
			scopes[i] = s
		})
	}
	wg.Wait()
	var searched []*module
	for _, c := range cands {
		if !c.open {
			searched = append(searched, c.m)
		}
	}
	e.keepSearched(searched)
	var out []Scope
	for i, s := range scopes {
		if errs[i] != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			e.logf("engine: search in %s: %v", cands[i].m.pm.ID, errs[i])
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// searchModules returns from's module and the modules that depend on it,
// or every module.
func (e *Engine) searchModules(ctx context.Context, from string) ([]*module, error) {
	var fm *module
	if from != "" {
		var err error
		if fm, err = e.module(ctx, from); err != nil {
			return nil, err
		}
	}
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
			if fm == nil || m == fm || slices.Contains(m.deps, fm.pm) {
				out = append(out, m)
			}
		}
	}
	if fm != nil && !slices.Contains(out, fm) {
		out = append(out, fm) // a single file
	}
	return out, nil
}

// keepSearched releases the analyses of the modules searches analyzed,
// but those of the KeepSearched most recent and of the modules with an
// open document, so that a search across the workspace doesn't keep it
// all in memory. ms are the modules a search just analyzed.
func (e *Engine) keepSearched(ms []*module) {
	keep := max(e.opts.KeepSearched, 0)
	e.searchMu.Lock()
	for _, m := range ms {
		e.searched = slices.DeleteFunc(e.searched, func(x *module) bool { return x == m })
	}
	e.searched = append(slices.Clone(ms), e.searched...)
	var drop []*module
	if len(e.searched) > keep {
		drop = slices.Clone(e.searched[keep:])
		e.searched = slices.Clip(e.searched[:keep])
	}
	e.searchMu.Unlock()
	released := false
	for _, m := range drop {
		if !e.hasOpen(m) && m.release() {
			released = true
		}
	}
	if released {
		e.freeLater()
	}
}

// freeDelay is how long after a search released analyses their memory is
// returned to the system: the request that made them still uses them.
const freeDelay = 5 * time.Second

// freeLater returns the memory of released analyses to the system after
// freeDelay, once for releases that come close together. The runtime
// would return it only bit by bit, over minutes.
func (e *Engine) freeLater() {
	e.searchMu.Lock()
	defer e.searchMu.Unlock()
	if e.freeTimer != nil {
		e.freeTimer.Reset(freeDelay)
		return
	}
	e.freeTimer = time.AfterFunc(freeDelay, func() {
		if e.ctx.Err() == nil {
			e.releaseFiles()
			debug.FreeOSMemory()
		}
	})
}

// releaseFiles has the Files forget the files it read from disk that no
// module's analysis uses, as those of the modules searches released:
// the parses of a workspace's files take more memory than its analyses
// keep. The files of the analyses kept, and of those running, stay, so
// that the next request on them reuses their analysis.
func (e *Engine) releaseFiles() {
	r, ok := e.opts.Files.(Releaser)
	if !ok {
		return
	}
	e.mu.Lock()
	var ms []*module
	for _, x := range e.roots {
		for _, m := range x.modules {
			ms = append(ms, m)
		}
	}
	for _, m := range e.singles {
		ms = append(ms, m)
	}
	e.mu.Unlock()
	e.filesMu.Lock()
	defer e.filesMu.Unlock()
	keep := map[*features.File]bool{}
	for _, m := range ms {
		m.mu.Lock()
		for _, f := range m.lastKey {
			keep[f] = true
		}
		if m.running != nil {
			for _, f := range m.running.key {
				keep[f] = true
			}
		}
		m.mu.Unlock()
	}
	r.Release(keep)
}

// hasOpen reports whether the client opened one of the module's own
// files.
func (e *Engine) hasOpen(m *module) bool {
	t, ok := e.opts.Files.(Texts)
	if !ok {
		return false
	}
	for _, p := range m.sources()[m.pm] {
		if _, ok := t.OpenText(p); ok {
			return true
		}
	}
	return false
}

// text returns the text of the file at path, and whether it is the
// client's: the text of the document if the client opened it, else the
// file on disk. It isn't kept, so that searches don't hold every file's.
func (e *Engine) text(path string) (text string, open, ok bool) {
	if t, ok := e.opts.Files.(Texts); ok {
		if s, ok := t.OpenText(path); ok {
			return s, true, true
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false, false
	}
	return string(b), false, true
}

// containsWord reports whether text has one of words, not as part of a
// longer identifier.
func containsWord(text string, words []string) bool {
	for _, w := range words {
		if w == "" {
			continue
		}
		for off := 0; ; {
			i := strings.Index(text[off:], w)
			if i < 0 {
				break
			}
			start, end := off+i, off+i+len(w)
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			after, _ := utf8.DecodeRuneInString(text[end:])
			if !identRune(before) && !identRune(after) {
				return true
			}
			off = end
		}
	}
	return false
}

func identRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
