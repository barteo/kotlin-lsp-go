package server

import (
	"context"
	"encoding/json"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// actions answers textDocument/inlayHint and textDocument/codeAction.
func (s *Server) actions(ctx context.Context, req *jsonrpc.Request) (any, error) {
	switch req.Method {
	case "textDocument/inlayHint":
		var p protocol.InlayHintParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		a, err := s.analysisOf(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logf("inlay hints in %s: %v", p.TextDocument.URI, err)
			return []protocol.InlayHint{}, nil
		}
		s.mu.Lock()
		o := s.hints
		s.mu.Unlock()
		return features.InlayHints(a, f, p.Range, o), nil
	case "textDocument/codeAction":
		var p protocol.CodeActionParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		var a *features.Analysis
		if s.analyzed(f) {
			if a, err = s.analysisOf(ctx, f); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				s.logf("code actions in %s: %v", p.TextDocument.URI, err)
				a = nil
			}
		}
		var c features.Completer
		if a != nil {
			c = s.eng
		}
		return features.CodeActions(ctx, a, c, f, p.Range, p.Context.Diagnostics, p.Context.Only), nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}

// configure applies the kcls settings in raw, {"hints": {...}}, to the
// server, under s.mu, and reports whether they changed it.
func (s *Server) configure(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var settings struct {
		Hints json.RawMessage `json:"hints"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		s.logf("settings: %v", err)
		return false
	}
	if len(settings.Hints) == 0 {
		return false
	}
	h := s.hints // what the settings leave out stays
	if err := json.Unmarshal(settings.Hints, &h); err != nil {
		s.logf("kcls.hints: %v", err)
		return false
	}
	changed := h != s.hints
	s.hints = h
	return changed
}

// refreshInlayHints asks the client to ask for the inlay hints again, if
// it can.
func (s *Server) refreshInlayHints() {
	if !s.refreshHints {
		return
	}
	go func() {
		if err := s.conn.Call(s.ctx, "workspace/inlayHint/refresh", nil, nil); err != nil && s.ctx.Err() == nil {
			s.logf("inlay hint refresh: %v", err)
		}
	}()
}
