package server

import (
	"context"
	"errors"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// workspace answers the requests that look beyond one file: references,
// implementations, rename, the call and type hierarchies, and workspace
// symbols.
func (s *Server) workspace(ctx context.Context, req *jsonrpc.Request) (any, error) {
	switch req.Method {
	case "workspace/symbol":
		var p protocol.WorkspaceSymbolParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		return s.eng.WorkspaceSymbols(ctx, p.Query)

	case "callHierarchy/incomingCalls", "callHierarchy/outgoingCalls", "typeHierarchy/supertypes", "typeHierarchy/subtypes":
		var p protocol.HierarchyItemParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		k, ok := features.ItemKey(p.Item)
		if !ok {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "not an item of this server")
		}
		var a *features.Analysis
		if k.Source() {
			var err error
			if a, err = s.eng.Analyze(ctx, k.Path); err != nil {
				return nil, s.failed(ctx, req.Method, err)
			}
		}
		switch req.Method {
		case "callHierarchy/incomingCalls":
			calls, err := features.IncomingCalls(ctx, s.eng, a, k)
			return calls, s.failed(ctx, req.Method, err)
		case "callHierarchy/outgoingCalls":
			return features.OutgoingCalls(ctx, a, k), nil
		case "typeHierarchy/supertypes":
			return features.Supertypes(ctx, a, k), nil
		default:
			subs, err := features.Subtypes(ctx, s.eng, k)
			return subs, s.failed(ctx, req.Method, err)
		}
	}

	// The rest are at a position.
	var p struct {
		protocol.TextDocumentPositionParams
		Context protocol.ReferenceContext `json:"context"`
		NewName string                    `json:"newName"`
	}
	if err := decode(req, &p); err != nil {
		return nil, err
	}
	f, err := s.file(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	a, err := s.analysisOf(ctx, f)
	if err != nil {
		return nil, s.failed(ctx, req.Method, err)
	}
	if a == nil {
		return nil, nil
	}
	switch req.Method {
	case "textDocument/references":
		locs, err := features.References(ctx, s.eng, a, f, p.Position, p.Context.IncludeDeclaration)
		return locs, s.failed(ctx, req.Method, err)
	case "textDocument/implementation":
		locs, err := features.Implementations(ctx, s.eng, a, f, p.Position)
		return locs, s.failed(ctx, req.Method, err)
	case "textDocument/prepareRename":
		r, err := features.PrepareRename(a, f, p.Position)
		if err != nil {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
		}
		if r == nil {
			return nil, nil
		}
		return r, nil
	case "textDocument/rename":
		edit, err := features.Rename(ctx, s.eng, a, f, p.Position, p.NewName)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
		}
		return edit, nil
	case "textDocument/prepareCallHierarchy":
		return nilIfEmpty(features.PrepareCallHierarchy(ctx, a, f, p.Position)), nil
	case "textDocument/prepareTypeHierarchy":
		return nilIfEmpty(features.PrepareTypeHierarchy(ctx, a, f, p.Position)), nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}

// failed turns an error of a workspace request into its response's:
// the request's cancellation, or a failure, which it logs.
func (s *Server) failed(ctx context.Context, method string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return ctx.Err()
	}
	s.logf("%s: %v", method, err)
	return &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
}

// nilIfEmpty returns nil for no items: the protocol's "nothing here".
func nilIfEmpty(items []protocol.HierarchyItem) any {
	if len(items) == 0 {
		return nil
	}
	return items
}
