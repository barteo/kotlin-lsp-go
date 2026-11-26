package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// The commands the server executes.
const (
	cmdReimport    = "kcls.reimport"
	cmdClearCaches = "kcls.clearCaches"
)

var commands = []string{cmdReimport, cmdClearCaches}

// analysisTimeout bounds how long a published diagnostic waits for an
// analysis, which waits for a Gradle import.
const analysisTimeout = 10 * time.Minute

// analysisOf returns the analysis of the module of the file f, or nil
// for a file the engine doesn't analyze: a script, a library's source.
func (s *Server) analysisOf(ctx context.Context, f *features.File) (*features.Analysis, error) {
	path := f.Snapshot.Path()
	if !strings.HasSuffix(path, ".kt") || s.eng.IsLibrarySource(path) {
		return nil, nil
	}
	return s.eng.Analyze(ctx, path)
}

// diagnostics returns the syntax and semantic diagnostics of the
// document at uri. If version is set, it is set to the version of the
// text they are of.
func (s *Server) diagnostics(ctx context.Context, uri protocol.DocumentURI, version *int32) ([]protocol.Diagnostic, error) {
	f, v, err := s.docs.file(uri)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
	}
	if version != nil {
		*version = v
	}
	a, err := s.analysisOf(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.logf("analysis of %s: %v", uri, err)
		a = nil // syntax errors still
	}
	return features.Diagnostics(a, f), nil
}

// publishDiagnostics publishes the diagnostics of the open document at
// uri, unless the client edited it while they were computed: the edit
// publishes them again.
func (s *Server) publishDiagnostics(uri protocol.DocumentURI) {
	if !s.docs.isOpen(uri) {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, analysisTimeout)
	defer cancel()
	var version int32
	diags, err := s.diagnostics(ctx, uri, &version)
	if err != nil {
		if s.ctx.Err() == nil {
			s.logf("diagnostics of %s: %v", uri, err)
		}
		return
	}
	if _, v, err := s.docs.file(uri); err != nil || v != version || !s.docs.isOpen(uri) {
		return
	}
	s.publish(uri, &version, diags)
}

// tokensWait bounds how long semantic tokens wait for an analysis
// before they fall back to the lexical ones; a client that can be asked
// to refresh them is, when the analysis is done.
const tokensWait = 2 * time.Second

// tokensAnalysis returns the analysis of f for its semantic tokens, or
// nil if it isn't done soon.
func (s *Server) tokensAnalysis(ctx context.Context, f *features.File) *features.Analysis {
	path := f.Snapshot.Path()
	if !strings.HasSuffix(path, ".kt") || s.eng.IsLibrarySource(path) {
		return nil
	}
	type result struct {
		a   *features.Analysis
		err error
	}
	done := make(chan result, 1)
	go func() {
		actx, cancel := context.WithTimeout(s.ctx, analysisTimeout)
		defer cancel()
		a, err := s.eng.Analyze(actx, path)
		done <- result{a, err}
	}()
	select {
	case r := <-done:
		return r.a
	case <-time.After(tokensWait):
		if s.refreshTokens {
			go func() {
				if r := <-done; r.err == nil {
					s.refreshSemanticTokens()
				}
			}()
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}

func (s *Server) refreshSemanticTokens() {
	if err := s.conn.Call(s.ctx, "workspace/semanticTokens/refresh", nil, nil); err != nil && s.ctx.Err() == nil {
		s.logf("semantic tokens refresh: %v", err)
	}
}

// refreshAll computes the diagnostics of every open document again, as
// after a change on disk or of the project model: by publishing them, or
// by asking a client that pulls them to pull them again.
func (s *Server) refreshAll() {
	if s.refreshTokens {
		go s.refreshSemanticTokens()
	}
	s.refreshInlayHints()
	if s.pullDiagnostics {
		if s.refreshDiagnostics {
			go func() {
				if err := s.conn.Call(s.ctx, "workspace/diagnostic/refresh", nil, nil); err != nil && s.ctx.Err() == nil {
					s.logf("diagnostic refresh: %v", err)
				}
			}()
		}
		return
	}
	for _, uri := range s.docs.openDocs() {
		s.schedule(uri)
	}
}

// semantic answers a request at a position that needs the analysis.
func (s *Server) semantic(ctx context.Context, method string, p protocol.TextDocumentPositionParams) (any, error) {
	f, err := s.file(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	a, err := s.analysisOf(ctx, f)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.logf("%s: analysis of %s: %v", method, p.TextDocument.URI, err)
	}
	if a == nil {
		return nil, nil
	}
	switch method {
	case "textDocument/hover":
		if h := features.Hover(ctx, a, f, p.Position); h != nil {
			return h, nil
		}
	case "textDocument/definition":
		links := features.Definition(ctx, a, f, p.Position)
		if s.linkSupport {
			return links, nil
		}
		return features.Locations(links), nil
	case "textDocument/typeDefinition":
		links := features.TypeDefinition(ctx, a, f, p.Position)
		if s.typeLinkSupport {
			return links, nil
		}
		return features.Locations(links), nil
	case "textDocument/documentHighlight":
		return features.DocumentHighlights(a, f, p.Position), nil
	}
	return nil, nil
}

func (s *Server) executeCommand(p protocol.ExecuteCommandParams) (any, error) {
	switch p.Command {
	case cmdReimport:
		s.logf("command: reimport")
		s.eng.Reimport()
		return nil, nil
	case cmdClearCaches:
		s.logf("command: clear caches")
		s.eng.ClearCaches()
		s.refreshAll()
		return nil, nil
	}
	return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown command %q", p.Command)
}

// modelChanged is called when a workspace's project model changes.
func (s *Server) modelChanged(root string, err error) {
	if err != nil {
		msg := fmt.Sprintf("kcls: couldn't read the project model of %s, so it is analyzed as a folder of sources: %v", filepath.Base(root), firstLine(err.Error()))
		s.showMessage(protocol.MessageWarning, msg)
	}
	s.refreshAll()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " (see the server's log)"
	}
	return s
}

func (s *Server) showMessage(t protocol.MessageType, msg string) {
	if err := s.conn.Notify("window/showMessage", protocol.ShowMessageParams{Type: t, Message: msg}); err != nil {
		s.logf("show message: %v", err)
	}
}

var progressTokens atomic.Int64

// progress reports long work to a client that shows progress, once it
// is initialized, and logs it.
func (s *Server) progress(title string) func(string) {
	s.logf("progress: %s", title)
	logEnd := func(msg string) {
		if msg != "" {
			s.logf("progress: %s: %s", title, msg)
		}
	}
	if !s.workDoneProgress {
		return logEnd
	}
	select {
	case <-s.ready:
	case <-time.After(10 * time.Second):
		return logEnd
	case <-s.ctx.Done():
		return logEnd
	}
	token := fmt.Sprintf("kcls-%d", progressTokens.Add(1))
	if err := s.conn.Call(s.ctx, "window/workDoneProgress/create", protocol.WorkDoneProgressCreateParams{Token: token}, nil); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logf("progress: %v", err)
		}
		return logEnd
	}
	s.conn.Notify("$/progress", protocol.ProgressParams{Token: token, Value: protocol.WorkDoneProgressBegin{Kind: "begin", Title: title}})
	return func(msg string) {
		logEnd(msg)
		s.conn.Notify("$/progress", protocol.ProgressParams{Token: token, Value: protocol.WorkDoneProgressEnd{Kind: "end", Message: msg}})
	}
}
