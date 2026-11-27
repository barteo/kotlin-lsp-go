package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barteo/kotlin-lsp-go/internal/features"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// TestDocStoreRelease checks that Release forgets the documents read from
// disk whose parse isn't kept, keeps the kept ones' parses and the
// documents the client opened, and that a forgotten file is read again.
func TestDocStoreRelease(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	kept, dropped, opened := write("K.kt", "class K\n"), write("D.kt", "class D\n"), write("O.kt", "class O\n")
	unparsed := write("U.kt", "class U\n")
	s := newDocStore(protocol.UTF16)
	file := func(p string) *features.File {
		t.Helper()
		f, err := s.File(p)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	fk, fd := file(kept), file(dropped)
	if err := s.open(protocol.TextDocumentItem{URI: pathURI(opened), Version: 1, Text: "class Opened\n"}); err != nil {
		t.Fatal(err)
	}
	fo := file(opened)
	// Read, not parsed: as a document a request reads only the text of.
	key := pathURI(unparsed)
	if _, err := s.load(key, key, nil); err != nil {
		t.Fatal(err)
	}

	s.Release(map[*features.File]bool{fk: true})

	s.mu.Lock()
	n := len(s.docs)
	s.mu.Unlock()
	if n != 2 {
		t.Errorf("%d documents after Release, want 2 (the kept and the opened)", n)
	}
	if file(kept) != fk {
		t.Error("the kept document was parsed again")
	}
	if file(dropped) == fd {
		t.Error("the released document wasn't forgotten")
	}
	if f := file(opened); f != fo || f.Mapper.Text() != "class Opened\n" {
		t.Error("the opened document was released")
	}
	// A kept document is still read again when its file changes.
	write("K.kt", "class K2 {}\n")
	if f := file(kept); f == fk || f.Mapper.Text() != "class K2 {}\n" {
		t.Errorf("the kept document after a change: %q", f.Mapper.Text())
	}
}
