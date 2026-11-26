// Package server is kcls's Language Server Protocol server: the
// lifecycle, the documents the client edits, and the dispatch of
// requests to package features.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/barteo/kotlin-lsp-go/internal/engine"
	"github.com/barteo/kotlin-lsp-go/internal/features"
	"github.com/barteo/kotlin-lsp-go/internal/jsonrpc"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// Options configure a server.
type Options struct {
	// Version is reported to the client as the server's version.
	Version string
	// Logf logs what the server does; nil discards it. It must not write
	// to the connection's stream.
	Logf func(format string, args ...any)
	// DiagnosticsDelay is how long after an edit diagnostics are
	// published; 0 means 50 ms.
	DiagnosticsDelay time.Duration
	// CacheDir holds Gradle models and extracted library sources; empty
	// for the user's cache directory.
	CacheDir string
	// NoImport never runs Gradle.
	NoImport bool
	// KC is the kc binary that compiles what run lenses run; empty to
	// look for it.
	KC string
}

// state is a server's place in the lifecycle.
type state int

const (
	uninitialized state = iota
	running
	shutDown
)

// Server is one client's server.
type Server struct {
	opts Options
	logf func(string, ...any)
	conn *jsonrpc.Conn
	ctx  context.Context // cancelled when Serve returns
	eng  *engine.Engine
	// ready is closed on the initialized notification, after which the
	// server may send the client requests.
	ready     chan struct{}
	readyOnce sync.Once

	mu       sync.Mutex
	state    state
	exit     chan struct{} // closed on the exit notification
	exitOnce sync.Once
	roots    []string // workspace folders' paths

	// From the client's capabilities.
	enc                 string
	hierarchicalSymbols bool
	pullDiagnostics     bool
	watchFiles          bool
	linkSupport         bool // definition
	typeLinkSupport     bool // typeDefinition
	completionOpts      features.CompletionOptions
	labelOffsets        bool // signature help's parameter labels
	refreshDiagnostics  bool
	refreshTokens       bool
	refreshHints        bool
	workDoneProgress    bool

	// hints are the kcls.hints settings, under mu.
	hints features.HintOptions

	docs *docStore

	pubMu   sync.Mutex
	pending map[protocol.DocumentURI]*time.Timer // diagnostics to publish
}

// Serve serves one client on r and w until the client sends exit or the
// stream ends. It returns the exit code the process should exit with: 0
// after shutdown and exit, 1 otherwise, as the protocol says.
func Serve(ctx context.Context, r io.Reader, w io.Writer, opts Options) int {
	s := &Server{opts: opts, logf: opts.Logf, exit: make(chan struct{}), ready: make(chan struct{}), pending: map[protocol.DocumentURI]*time.Timer{}}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	if s.opts.DiagnosticsDelay == 0 {
		s.opts.DiagnosticsDelay = 50 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = ctx
	s.conn = jsonrpc.NewConn(r, w, s.handle, s.logf)
	done := make(chan error, 1)
	go func() { done <- s.conn.Run(ctx) }()
	select {
	case <-s.exit:
	case err := <-done:
		if err != nil {
			s.logf("connection: %v", err)
		}
	}
	s.stopPending()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eng != nil {
		s.eng.Close()
	}
	if s.state == shutDown {
		return 0
	}
	return 1
}

// handle dispatches a request or notification.
func (s *Server) handle(ctx context.Context, conn *jsonrpc.Conn, req *jsonrpc.Request) (any, error) {
	s.mu.Lock()
	st := s.state
	s.mu.Unlock()
	switch req.Method {
	case "initialize":
		if st != uninitialized {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "initialize sent twice")
		}
		var p protocol.InitializeParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		return s.initialize(&p), nil
	case "exit":
		s.exitOnce.Do(func() { close(s.exit) })
		return nil, nil
	}
	switch {
	case st == uninitialized:
		if req.IsNotification() {
			return nil, nil // dropped, as the protocol says
		}
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerNotInitialized, "server not initialized")
	case st == shutDown:
		if req.IsNotification() {
			return nil, nil
		}
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidRequest, "server is shut down")
	}

	switch req.Method {
	case "initialized":
		s.readyOnce.Do(func() { close(s.ready) })
		s.initialized(ctx)
		return nil, nil
	case "shutdown":
		s.mu.Lock()
		s.state = shutDown
		s.mu.Unlock()
		s.stopPending()
		return nil, nil

	case "textDocument/didOpen":
		var p protocol.DidOpenTextDocumentParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		if err := s.docs.open(p.TextDocument); err != nil {
			return nil, err
		}
		s.schedule(p.TextDocument.URI)
		return nil, nil
	case "textDocument/didChange":
		var p protocol.DidChangeTextDocumentParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		if err := s.docs.change(p.TextDocument, p.ContentChanges); err != nil {
			return nil, err
		}
		s.schedule(p.TextDocument.URI)
		return nil, nil
	case "textDocument/didClose":
		var p protocol.DidCloseTextDocumentParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		s.docs.close(p.TextDocument.URI)
		s.unschedule(p.TextDocument.URI)
		if !s.pullDiagnostics {
			s.publish(p.TextDocument.URI, nil, []protocol.Diagnostic{})
		}
		return nil, nil
	case "textDocument/didSave":
		return nil, nil
	case "workspace/didChangeWatchedFiles":
		var p protocol.DidChangeWatchedFilesParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		for _, c := range p.Changes {
			s.docs.invalidate(c.URI)
			if path, err := uriPath(c.URI); err == nil {
				s.eng.Changed(path, c.Type == protocol.FileCreated, c.Type == protocol.FileDeleted)
			}
		}
		if len(p.Changes) > 0 {
			s.refreshAll()
		}
		return nil, nil
	case "workspace/didChangeWorkspaceFolders":
		var p protocol.DidChangeWorkspaceFoldersParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		for _, f := range p.Event.Removed {
			if path, err := uriPath(f.URI); err == nil {
				s.eng.RemoveRoot(path)
			}
		}
		for _, f := range p.Event.Added {
			if path, err := uriPath(f.URI); err == nil {
				s.eng.AddRoot(path)
			}
		}
		s.refreshAll()
		return nil, nil
	case "workspace/didChangeConfiguration":
		var p protocol.DidChangeConfigurationParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		var settings struct {
			Kcls json.RawMessage `json:"kcls"`
		}
		if json.Unmarshal(p.Settings, &settings) == nil && len(settings.Kcls) > 0 {
			s.mu.Lock()
			changed := s.configure(settings.Kcls)
			s.mu.Unlock()
			if changed {
				s.refreshInlayHints()
			}
		}
		return nil, nil
	case "$/setTrace":
		return nil, nil
	case "workspace/executeCommand":
		var p protocol.ExecuteCommandParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		return s.executeCommand(ctx, p)

	case "textDocument/hover", "textDocument/definition", "textDocument/typeDefinition", "textDocument/documentHighlight":
		var p protocol.TextDocumentPositionParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		return s.semantic(ctx, req.Method, p)

	case "textDocument/completion", "completionItem/resolve", "textDocument/signatureHelp":
		return s.completion(ctx, req)

	case "textDocument/inlayHint", "textDocument/codeAction":
		return s.actions(ctx, req)

	case "textDocument/formatting", "textDocument/rangeFormatting", "textDocument/onTypeFormatting", "workspace/willRenameFiles":
		return s.formatting(ctx, req)

	case "textDocument/references", "textDocument/implementation", "textDocument/prepareRename", "textDocument/rename",
		"textDocument/prepareCallHierarchy", "textDocument/prepareTypeHierarchy",
		"callHierarchy/incomingCalls", "callHierarchy/outgoingCalls", "typeHierarchy/supertypes", "typeHierarchy/subtypes",
		"workspace/symbol":
		return s.workspace(ctx, req)

	case "textDocument/codeLens":
		return s.codeLens(req)
	case "textDocument/documentSymbol":
		var p protocol.DocumentSymbolParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		syms := features.DocumentSymbols(f)
		if !s.hierarchicalSymbols {
			return features.FlatSymbols(f, syms), nil
		}
		return syms, nil
	case "textDocument/foldingRange":
		var p protocol.FoldingRangeParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		return features.FoldingRanges(f), nil
	case "textDocument/selectionRange":
		var p protocol.SelectionRangeParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		srs, err := features.SelectionRanges(f, p.Positions)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
		}
		return srs, nil
	case "textDocument/semanticTokens/full":
		var p protocol.SemanticTokensParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		return features.SemanticTokensOf(s.tokensAnalysis(ctx, f), f, nil)
	case "textDocument/semanticTokens/range":
		var p protocol.SemanticTokensRangeParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		f, err := s.file(p.TextDocument.URI)
		if err != nil {
			return nil, err
		}
		toks, err := features.SemanticTokensOf(s.tokensAnalysis(ctx, f), f, &p.Range)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
		}
		return toks, nil
	case "textDocument/diagnostic":
		var p protocol.DocumentDiagnosticParams
		if err := decode(req, &p); err != nil {
			return nil, err
		}
		items, err := s.diagnostics(ctx, p.TextDocument.URI, nil)
		if err != nil {
			return nil, err
		}
		return protocol.DocumentDiagnosticReport{Kind: "full", Items: items}, nil
	}
	if req.IsNotification() {
		return nil, nil
	}
	return nil, jsonrpc.ErrMethodNotFound
}

func decode(req *jsonrpc.Request, v any) error {
	if len(req.Params) == 0 {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%s: missing params", req.Method)
	}
	if err := json.Unmarshal(req.Params, v); err != nil {
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%s: %v", req.Method, err)
	}
	return nil
}

// file returns a document for a request, answering a file that can't be
// read with an error the client shows.
func (s *Server) file(uri protocol.DocumentURI) (*features.File, error) {
	f, _, err := s.docs.file(uri)
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
	}
	return f, nil
}

func (s *Server) initialize(p *protocol.InitializeParams) *protocol.InitializeResult {
	caps := p.Capabilities
	enc := protocol.UTF16
	if caps.General != nil && slices.Contains(caps.General.PositionEncodings, protocol.UTF8) {
		enc = protocol.UTF8
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enc = enc
	s.docs = newDocStore(enc)
	if td := caps.TextDocument; td != nil {
		s.hierarchicalSymbols = td.DocumentSymbol != nil && td.DocumentSymbol.HierarchicalDocumentSymbolSupport
		s.pullDiagnostics = td.Diagnostic != nil
		s.linkSupport = td.Definition != nil && td.Definition.LinkSupport
		s.typeLinkSupport = td.TypeDefinition != nil && td.TypeDefinition.LinkSupport
		if c := td.Completion; c != nil && c.CompletionItem != nil {
			s.completionOpts = features.CompletionOptions{Snippets: c.CompletionItem.SnippetSupport, DeprecatedTag: c.CompletionItem.TagSupport != nil}
		}
		if h := td.SignatureHelp; h != nil && h.SignatureInformation != nil && h.SignatureInformation.ParameterInformation != nil {
			s.labelOffsets = h.SignatureInformation.ParameterInformation.LabelOffsetSupport
		}
	}
	if ws := caps.Workspace; ws != nil {
		s.watchFiles = ws.DidChangeWatchedFiles != nil && ws.DidChangeWatchedFiles.DynamicRegistration
		s.refreshDiagnostics = ws.Diagnostics != nil && ws.Diagnostics.RefreshSupport
		s.refreshTokens = ws.SemanticTokens != nil && ws.SemanticTokens.RefreshSupport
		s.refreshHints = ws.InlayHint != nil && ws.InlayHint.RefreshSupport
	}
	s.workDoneProgress = caps.Window != nil && caps.Window.WorkDoneProgress
	s.hints = features.DefaultHints()
	s.configure(p.InitializationOptions)
	for _, f := range p.WorkspaceFolders {
		if path, err := uriPath(f.URI); err == nil {
			s.roots = append(s.roots, path)
		}
	}
	if len(s.roots) == 0 && p.RootURI != nil {
		if path, err := uriPath(*p.RootURI); err == nil {
			s.roots = append(s.roots, path)
		}
	}
	client := "unknown client"
	if p.ClientInfo != nil {
		client = p.ClientInfo.Name + " " + p.ClientInfo.Version
	}
	s.logf("initialize: %s, encoding %s, roots %v, pull diagnostics %v", client, enc, s.roots, s.pullDiagnostics)
	s.state = running
	s.eng = engine.New(engine.Options{
		Files:    s.docs,
		Logf:     s.logf,
		CacheDir: s.opts.CacheDir,
		NoImport: s.opts.NoImport,
		Progress: s.progress,
		OnModel:  s.modelChanged,
	})
	for _, r := range s.roots {
		s.eng.AddRoot(r)
	}

	res := &protocol.InitializeResult{
		ServerInfo: &protocol.ServerInfo{Name: "kcls", Version: s.opts.Version},
		Capabilities: protocol.ServerCapabilities{
			PositionEncoding: enc,
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: true,
				Change:    protocol.SyncIncremental,
				Save:      &protocol.SaveOptions{},
			},
			DocumentSymbolProvider: true,
			FoldingRangeProvider:   true,
			SelectionRangeProvider: true,
			SemanticTokensProvider: &protocol.SemanticTokensOptions{
				Legend: features.Legend(),
				Full:   true,
				Range:  true,
			},
			HoverProvider:             true,
			DefinitionProvider:        true,
			TypeDefinitionProvider:    true,
			DocumentHighlightProvider: true,
			ReferencesProvider:        true,
			ImplementationProvider:    true,
			RenameProvider:            &protocol.RenameOptions{PrepareProvider: true},
			CallHierarchyProvider:     true,
			TypeHierarchyProvider:     true,
			WorkspaceSymbolProvider:   true,
			CompletionProvider:        &protocol.CompletionOptions{TriggerCharacters: []string{".", ":", "@"}, ResolveProvider: true},
			SignatureHelpProvider:     &protocol.SignatureHelpOptions{TriggerCharacters: []string{"(", ","}, RetriggerCharacters: []string{")"}},
			InlayHintProvider:         true,
			CodeActionProvider: &protocol.CodeActionOptions{
				CodeActionKinds: []string{protocol.QuickFix, protocol.RefactorRewrite, protocol.RefactorExtract, protocol.RefactorInline,
					protocol.SourceOrganizeImports},
			},
			DocumentFormattingProvider:       true,
			DocumentRangeFormattingProvider:  true,
			DocumentOnTypeFormattingProvider: &protocol.DocumentOnTypeFormattingOptions{FirstTriggerCharacter: "}", MoreTriggerCharacter: []string{";", "\n"}},
			CodeLensProvider:                 &protocol.CodeLensOptions{},
			ExecuteCommandProvider:           &protocol.ExecuteCommandOptions{Commands: commands},
			Workspace: &protocol.WorkspaceOptions{
				WorkspaceFolders: &protocol.WorkspaceFoldersServerCapabilities{Supported: true, ChangeNotifications: true},
				FileOperations: &protocol.FileOperationOptions{WillRename: &protocol.FileOperationRegistrationOptions{Filters: []protocol.FileOperationFilter{
					{Scheme: "file", Pattern: protocol.FileOperationPattern{Glob: "**/*.kt", Matches: "file"}},
					{Scheme: "file", Pattern: protocol.FileOperationPattern{Glob: "**", Matches: "folder"}},
				}}},
			},
		},
	}
	if s.pullDiagnostics {
		res.Capabilities.DiagnosticProvider = &protocol.DiagnosticOptions{Identifier: "kcls", InterFileDependencies: true}
	}
	return res
}

// initialized registers for file events, if the client lets the server.
func (s *Server) initialized(ctx context.Context) {
	if !s.watchFiles {
		return
	}
	go func() {
		params := protocol.RegistrationParams{Registrations: []protocol.Registration{{
			ID:     "kcls-watch",
			Method: "workspace/didChangeWatchedFiles",
			RegisterOptions: protocol.DidChangeWatchedFilesRegistrationOptions{
				Watchers: []protocol.FileSystemWatcher{
					{GlobPattern: "**/*.{kt,kts,java}"},
					{GlobPattern: "**/{build.gradle,build.gradle.kts,settings.gradle,settings.gradle.kts,gradle.properties,libs.versions.toml,gradle-wrapper.properties,pom.xml,maven.config,maven-wrapper.properties,kcls.json}"},
				},
			},
		}}}
		if err := s.conn.Call(ctx, "client/registerCapability", params, nil); err != nil && !errors.Is(err, context.Canceled) {
			s.logf("register file watching: %v", err)
		}
	}()
}

// schedule publishes the document's diagnostics after the delay, unless
// the client pulls them. A later edit restarts the delay.
func (s *Server) schedule(uri protocol.DocumentURI) {
	if s.pullDiagnostics {
		return
	}
	key := canonicalURI(uri)
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if t := s.pending[key]; t != nil {
		t.Stop()
	}
	s.pending[key] = time.AfterFunc(s.opts.DiagnosticsDelay, func() {
		s.pubMu.Lock()
		delete(s.pending, key)
		s.pubMu.Unlock()
		s.publishDiagnostics(uri)
	})
}

func (s *Server) unschedule(uri protocol.DocumentURI) {
	key := canonicalURI(uri)
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if t := s.pending[key]; t != nil {
		t.Stop()
		delete(s.pending, key)
	}
}

func (s *Server) stopPending() {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	for k, t := range s.pending {
		t.Stop()
		delete(s.pending, k)
	}
}

func (s *Server) publish(uri protocol.DocumentURI, version *int32, diags []protocol.Diagnostic) {
	err := s.conn.Notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{URI: uri, Version: version, Diagnostics: diags})
	if err != nil {
		s.logf("publish diagnostics of %s: %v", uri, err)
	}
}
