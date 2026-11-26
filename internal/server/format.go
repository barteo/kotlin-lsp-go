package server

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// formatting answers textDocument/formatting, rangeFormatting and
// onTypeFormatting, and workspace/willRenameFiles.
func (s *Server) formatting(ctx context.Context, req *jsonrpc.Request) (any, error) {
	switch req.Method {
	case "textDocument/formatting":
		var p protocol.DocumentFormattingParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		return features.Formatting(f, features.FormatOptions(f.Snapshot.Path(), p.Options)), nil
	case "textDocument/rangeFormatting":
		var p protocol.DocumentRangeFormattingParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		return features.RangeFormatting(f, p.Range, features.FormatOptions(f.Snapshot.Path(), p.Options)), nil
	case "textDocument/onTypeFormatting":
		var p protocol.DocumentOnTypeFormattingParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		return features.OnTypeFormatting(f, p.Position, p.Ch, features.FormatOptions(f.Snapshot.Path(), p.Options)), nil
	case "workspace/willRenameFiles":
		var p protocol.RenameFilesParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		moves := map[string]string{}
		for _, r := range p.Files {
			from, err1 := uriPath(r.OldURI)
			to, err2 := uriPath(r.NewURI)
			if err1 != nil || err2 != nil {
				continue
			}
			for old, new := range kotlinMoves(from, to) {
				moves[old] = new
			}
		}
		if len(moves) == 0 {
			return nil, nil
		}
		analyze := func(ctx context.Context, path string) (*features.Analysis, error) {
			f, err := s.file(pathURI(path))
			if err != nil || !s.analyzed(f) {
				return nil, nil
			}
			return s.analysisOf(ctx, f)
		}
		e, err := features.MoveFiles(ctx, s.eng, analyze, moves)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logf("moving files: %v", err)
			return nil, nil
		}
		if e == nil {
			return nil, nil
		}
		return e, nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}

// kotlinMoves returns the Kotlin files that renaming from to to moves,
// with their new paths: from itself, or the files in it if it is a
// directory.
func kotlinMoves(from, to string) map[string]string {
	out := map[string]string{}
	fi, err := os.Stat(from)
	if err != nil {
		return out
	}
	if !fi.IsDir() {
		if strings.HasSuffix(from, ".kt") {
			out[from] = to
		}
		return out
	}
	filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".kt") {
			if rel, err := filepath.Rel(from, p); err == nil {
				out[p] = filepath.Join(to, rel)
			}
		}
		return nil
	})
	return out
}
