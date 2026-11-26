package server

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-lsp-go/internal/features"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// document is a file the server knows: one the client opened, whose text
// the client owns, or one read from disk for a request on a file the
// client never opened (as coding agents ask), which is read again when
// its modification time or size changes.
type document struct {
	uri     protocol.DocumentURI
	path    string
	open    bool
	version int32
	text    string

	modTime time.Time // of a document read from disk
	size    int64

	parsed *features.File // of text, parsed on first use
}

// docStore holds the documents.
type docStore struct {
	enc string // the negotiated position encoding

	mu   sync.Mutex
	docs map[protocol.DocumentURI]*document
}

func newDocStore(enc string) *docStore {
	return &docStore{enc: enc, docs: map[protocol.DocumentURI]*document{}}
}

// open records a document the client opened.
func (s *docStore) open(item protocol.TextDocumentItem) error {
	uri := canonicalURI(item.URI)
	path, err := uriPath(uri)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{uri: item.URI, path: path, open: true, version: item.Version, text: item.Text}
	return nil
}

// change applies a client's edits to an open document.
func (s *docStore) change(id protocol.VersionedTextDocumentIdentifier, changes []protocol.TextDocumentContentChangeEvent) error {
	uri := canonicalURI(id.URI)
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.docs[uri]
	if d == nil || !d.open {
		return fmt.Errorf("change of %s, which isn't open", uri)
	}
	text := d.text
	for _, c := range changes {
		if c.Range == nil {
			text = c.Text
			continue
		}
		start, end, err := protocol.NewMapper(text, s.enc).OffsetRange(*c.Range)
		if err != nil {
			return fmt.Errorf("change of %s: %w", uri, err)
		}
		text = text[:start] + c.Text + text[end:]
	}
	d.text, d.version, d.parsed = text, id.Version, nil
	return nil
}

// close forgets an open document; the file on disk is read for later
// requests.
func (s *docStore) close(uri protocol.DocumentURI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, canonicalURI(uri))
}

// invalidate drops a document read from disk, after the file changed.
func (s *docStore) invalidate(uri protocol.DocumentURI) {
	uri = canonicalURI(uri)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.docs[uri]; d != nil && !d.open {
		delete(s.docs, uri)
	}
}

// isOpen reports whether the client opened the document.
func (s *docStore) isOpen(uri protocol.DocumentURI) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.docs[canonicalURI(uri)]
	return d != nil && d.open
}

// file returns the parsed document and its version (0 for one read from
// disk). A document the client hasn't opened is read from disk, again if
// the file changed since it was last read.
func (s *docStore) file(uri protocol.DocumentURI) (*features.File, int32, error) {
	key := canonicalURI(uri)
	s.mu.Lock()
	d := s.docs[key]
	s.mu.Unlock()
	if d == nil || !d.open {
		var err error
		if d, err = s.load(uri, key, d); err != nil {
			return nil, 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.parsed == nil {
		d.parsed = features.NewFile(d.uri, d.path, d.text, s.enc)
	}
	return d.parsed, d.version, nil
}

// File returns the parsed file at path, for the engine: the client's
// text if it opened the file, else the file on disk.
func (s *docStore) File(path string) (*features.File, error) {
	f, _, err := s.file(pathURI(path))
	return f, err
}

// OpenText returns the text of the document at path if the client
// opened it, for the engine's searches.
func (s *docStore) OpenText(path string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.docs[pathURI(path)]; d != nil && d.open {
		return d.text, true
	}
	return "", false
}

// openDocs returns the URIs of the documents the client opened.
func (s *docStore) openDocs() []protocol.DocumentURI {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []protocol.DocumentURI
	for _, d := range s.docs {
		if d.open {
			out = append(out, d.uri)
		}
	}
	return out
}

// load reads the file of uri from disk, unless old is up to date; key
// is uri's canonical form.
func (s *docStore) load(uri, key protocol.DocumentURI, old *document) (*document, error) {
	path, err := uriPath(key)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if old != nil && fi.ModTime().Equal(old.modTime) && fi.Size() == old.size {
		return old, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d := &document{uri: uri, path: path, text: string(b), modTime: fi.ModTime(), size: fi.Size()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.docs[key]; cur != nil && cur.open {
		// Opened meanwhile: the client's text wins.
		return cur, nil
	}
	s.docs[key] = d
	return d, nil
}

// isKotlin reports whether a path is a Kotlin source or script.
func isKotlin(path string) bool {
	return strings.HasSuffix(path, ".kt") || strings.HasSuffix(path, ".kts")
}
