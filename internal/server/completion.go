package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// completion answers textDocument/completion, completionItem/resolve and
// textDocument/signatureHelp.
func (s *Server) completion(ctx context.Context, req *jsonrpc.Request) (any, error) {
	switch req.Method {
	case "textDocument/completion":
		var p protocol.CompletionParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		empty := &protocol.CompletionList{Items: []protocol.CompletionItem{}}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		if !s.analyzed(f) {
			return empty, nil
		}
		if c := p.Context; c != nil && c.TriggerCharacter != "" && c.TriggerCharacter != "." && !s.triggered(f, p.Position, c.TriggerCharacter) {
			return empty, nil
		}
		l, err := features.Completion(ctx, s.eng, f, p.Position, s.completionOpts)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logf("completion in %s: %v", p.TextDocument.URI, err)
			return empty, nil
		}
		return l, nil
	case "completionItem/resolve":
		var it protocol.CompletionItem
		if err := decode(req, &it); err != nil {
			return nil, err
		}
		var d features.CompletionData
		if len(it.Data) == 0 || json.Unmarshal(it.Data, &d) != nil || d.URI == "" {
			return it, nil
		}
		f, err := s.file(d.URI)
		if err != nil {
			return it, nil
		}
		a, err := s.analysisOf(ctx, f)
		if err != nil {
			return it, nil
		}
		return features.ResolveCompletion(ctx, a, it), nil
	case "textDocument/signatureHelp":
		var p protocol.SignatureHelpParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		if !s.analyzed(f) {
			return nil, nil
		}
		h, err := features.SignatureHelp(ctx, s.eng, f, p.Position, s.labelOffsets)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logf("signature help in %s: %v", p.TextDocument.URI, err)
			return nil, nil
		}
		if h == nil {
			return nil, nil
		}
		return h, nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}

// analyzed reports whether the engine analyzes f: not a script, nor a
// library's source.
func (s *Server) analyzed(f *features.File) bool {
	path := f.Snapshot.Path()
	return strings.HasSuffix(path, ".kt") && !s.eng.IsLibrarySource(path)
}

// triggered reports whether a trigger character other than a dot starts
// a completion: a colon of ::, an @ of a label or annotation.
func (s *Server) triggered(f *features.File, pos protocol.Position, ch string) bool {
	off, err := f.Mapper.Offset(pos)
	if err != nil {
		return false
	}
	text := f.Mapper.Text()
	switch ch {
	case ":":
		return off >= 2 && text[off-2:off] == "::"
	case "@":
		return true
	}
	return false
}
