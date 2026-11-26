package server

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// uriPath returns the file path of a file URI.
func uriPath(uri protocol.DocumentURI) (string, error) {
	u, err := url.Parse(string(uri))
	if err != nil {
		return "", fmt.Errorf("invalid URI %q: %w", uri, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q in %q", u.Scheme, uri)
	}
	if u.Path == "" {
		return "", fmt.Errorf("URI %q has no path", uri)
	}
	return filepath.Clean(filepath.FromSlash(u.Path)), nil
}

// pathURI returns the file URI of an absolute path.
func pathURI(path string) protocol.DocumentURI {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return protocol.DocumentURI(u.String())
}

// canonicalURI rewrites a file URI as pathURI writes it, so that a
// document has one key however a client escapes its URI
// (file:///a%20b.kt and file:///a b.kt).
func canonicalURI(uri protocol.DocumentURI) protocol.DocumentURI {
	p, err := uriPath(uri)
	if err != nil {
		return uri
	}
	return pathURI(p)
}
