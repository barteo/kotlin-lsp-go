package engine

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
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
	}
	var cands []cand
	for _, m := range mods {
		var paths []string
		for _, p := range m.sources()[m.pm] {
			if !strings.HasSuffix(p, ".kt") {
				continue
			}
			if text, ok := e.text(p); ok && containsWord(text, words) {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			cands = append(cands, cand{m, paths})
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

// text returns the text of the file at path: the client's if it opened
// it, else the file on disk, which is read again when it changes.
func (e *Engine) text(path string) (string, bool) {
	if t, ok := e.opts.Files.(Texts); ok {
		if s, ok := t.OpenText(path); ok {
			return s, true
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	e.textMu.Lock()
	c, ok := e.texts[path]
	e.textMu.Unlock()
	if ok && c.mod.Equal(fi.ModTime()) && c.size == fi.Size() {
		return c.text, true
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	e.textMu.Lock()
	e.texts[path] = cachedText{text: string(b), mod: fi.ModTime(), size: fi.Size()}
	e.textMu.Unlock()
	return string(b), true
}

type cachedText struct {
	text string
	mod  time.Time
	size int64
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
