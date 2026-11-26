package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// client is a scripted LSP client connected to a Server.
type client struct {
	t     *testing.T
	conn  *jsonrpc.Conn
	code  chan int
	diags chan protocol.PublishDiagnosticsParams

	mu         sync.Mutex
	registered []string
	refreshes  int
}

func start(t *testing.T) *client {
	t.Helper()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	c := &client{t: t, code: make(chan int, 1), diags: make(chan protocol.PublishDiagnosticsParams, 16)}
	go func() {
		c.code <- Serve(context.Background(), sr, sw, Options{Version: "test", Logf: t.Logf, DiagnosticsDelay: time.Millisecond, CacheDir: t.TempDir(), NoImport: true})
		sw.Close()
	}()
	c.conn = jsonrpc.NewConn(cr, cw, func(ctx context.Context, _ *jsonrpc.Conn, req *jsonrpc.Request) (any, error) {
		switch req.Method {
		case "textDocument/publishDiagnostics":
			var p protocol.PublishDiagnosticsParams
			json.Unmarshal(req.Params, &p)
			c.diags <- p
		case "workspace/diagnostic/refresh":
			c.mu.Lock()
			c.refreshes++
			c.mu.Unlock()
		case "client/registerCapability":
			var p protocol.RegistrationParams
			json.Unmarshal(req.Params, &p)
			c.mu.Lock()
			for _, r := range p.Registrations {
				c.registered = append(c.registered, r.Method)
			}
			c.mu.Unlock()
		}
		return nil, nil
	}, t.Logf)
	go c.conn.Run(context.Background())
	t.Cleanup(func() { cw.Close() })
	return c
}

func (c *client) call(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.conn.Call(ctx, method, params, result)
}

func (c *client) must(method string, params, result any) {
	c.t.Helper()
	if err := c.call(method, params, result); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

func (c *client) notify(method string, params any) {
	c.t.Helper()
	if err := c.conn.Notify(method, params); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

func (c *client) nextDiagnostics() protocol.PublishDiagnosticsParams {
	c.t.Helper()
	select {
	case d := <-c.diags:
		return d
	case <-time.After(10 * time.Second):
		c.t.Fatal("no diagnostics published")
	}
	panic("unreachable")
}

func (c *client) initialize(caps protocol.ClientCapabilities, root string) protocol.InitializeResult {
	c.t.Helper()
	var res protocol.InitializeResult
	uri := pathURI(root)
	c.must("initialize", protocol.InitializeParams{RootURI: &uri, Capabilities: caps}, &res)
	c.notify("initialized", protocol.InitializedParams{})
	return res
}

func (c *client) shutdown() int {
	c.t.Helper()
	c.must("shutdown", nil, nil)
	c.notify("exit", nil)
	select {
	case code := <-c.code:
		return code
	case <-time.After(10 * time.Second):
		c.t.Fatal("server didn't exit")
	}
	return -1
}

func TestLifecycle(t *testing.T) {
	c := start(t)
	var je *jsonrpc.Error
	if err := c.call("textDocument/documentSymbol", protocol.DocumentSymbolParams{}, nil); !errors.As(err, &je) || je.Code != jsonrpc.CodeServerNotInitialized {
		t.Fatalf("before initialize: %v", err)
	}
	res := c.initialize(protocol.ClientCapabilities{}, t.TempDir())
	if res.ServerInfo == nil || res.ServerInfo.Name != "kcls" || res.Capabilities.PositionEncoding != protocol.UTF16 {
		t.Fatalf("initialize result: %+v", res)
	}
	if res.Capabilities.DiagnosticProvider != nil {
		t.Error("diagnostic provider offered to a client that doesn't pull")
	}
	if err := c.call("initialize", protocol.InitializeParams{}, nil); err == nil {
		t.Error("second initialize accepted")
	}
	if err := c.call("no/such/method", struct{}{}, nil); !errors.As(err, &je) || je.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("unknown method: %v", err)
	}
	if code := c.shutdown(); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
}

func TestExitWithoutShutdown(t *testing.T) {
	c := start(t)
	c.initialize(protocol.ClientCapabilities{}, t.TempDir())
	c.notify("exit", nil)
	if code := <-c.code; code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

func TestEditingAndPushDiagnostics(t *testing.T) {
	c := start(t)
	dir := t.TempDir()
	c.initialize(protocol.ClientCapabilities{
		TextDocument: &protocol.TextDocumentClientCapabilities{
			DocumentSymbol: &protocol.DocumentSymbolClientCapabilities{HierarchicalDocumentSymbolSupport: true},
		},
		General: &protocol.GeneralClientCapabilities{PositionEncodings: []string{protocol.UTF16}},
	}, dir)
	uri := pathURI(filepath.Join(dir, "a b.kt"))
	c.notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{
		URI: uri, LanguageID: "kotlin", Version: 1, Text: "fun f( {\n}\n",
	}})
	d := c.nextDiagnostics()
	if d.URI != uri || d.Version == nil || *d.Version != 1 || len(d.Diagnostics) == 0 {
		t.Fatalf("diagnostics after open: %+v", d)
	}
	// Fix the error with an incremental edit: insert ")" after "(".
	c.notify("textDocument/didChange", protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{
			Range: &protocol.Range{Start: protocol.Position{Line: 0, Character: 6}, End: protocol.Position{Line: 0, Character: 6}},
			Text:  ")",
		}},
	})
	d = c.nextDiagnostics()
	if *d.Version != 2 || len(d.Diagnostics) != 0 {
		t.Fatalf("diagnostics after fix: %+v", d)
	}
	var syms []protocol.DocumentSymbol
	c.must("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &syms)
	if len(syms) != 1 || syms[0].Name != "f" || syms[0].Detail != "()" {
		t.Fatalf("symbols: %+v", syms)
	}
	c.notify("textDocument/didClose", protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	if d := c.nextDiagnostics(); len(d.Diagnostics) != 0 {
		t.Fatalf("diagnostics after close: %+v", d)
	}
	c.shutdown()
}

// TestUnopenedFiles checks requests on files the client never opened, as
// coding agents send them, and that the server sees changes on disk.
func TestUnopenedFiles(t *testing.T) {
	c := start(t)
	dir := t.TempDir()
	c.initialize(protocol.ClientCapabilities{
		TextDocument: &protocol.TextDocumentClientCapabilities{Diagnostic: &protocol.DynamicRegistration{}},
		Workspace:    &protocol.WorkspaceClientCapabilities{DidChangeWatchedFiles: &protocol.DynamicRegistration{DynamicRegistration: true}},
		General:      &protocol.GeneralClientCapabilities{PositionEncodings: []string{protocol.UTF8, protocol.UTF16}},
	}, dir)
	path := filepath.Join(dir, "b.kt")
	if err := os.WriteFile(path, []byte("class A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := pathURI(path)
	var flat []protocol.SymbolInformation
	c.must("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &flat)
	if len(flat) != 1 || flat[0].Name != "A" {
		t.Fatalf("symbols: %+v", flat)
	}
	// An agent rewrites the file: a different size makes the server read
	// it again even without a watch event.
	if err := os.WriteFile(path, []byte("class B {\n  fun g() = 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c.must("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &flat)
	if len(flat) != 2 || flat[0].Name != "B" || flat[1].Name != "g" || flat[1].ContainerName != "B" {
		t.Fatalf("symbols after rewrite: %+v", flat)
	}
	var rep protocol.DocumentDiagnosticReport
	c.must("textDocument/diagnostic", protocol.DocumentDiagnosticParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &rep)
	if rep.Kind != "full" || len(rep.Items) != 0 {
		t.Fatalf("diagnostic report: %+v", rep)
	}
	var folds []protocol.FoldingRange
	c.must("textDocument/foldingRange", protocol.FoldingRangeParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &folds)
	if len(folds) != 1 || folds[0].StartLine != 0 || folds[0].EndLine != 1 {
		t.Fatalf("folding: %+v", folds)
	}
	var toks protocol.SemanticTokens
	c.must("textDocument/semanticTokens/full", protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &toks)
	if len(toks.Data) == 0 || len(toks.Data)%5 != 0 {
		t.Fatalf("tokens: %v", toks.Data)
	}
	if err := c.call("textDocument/documentSymbol", protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: pathURI(filepath.Join(dir, "missing.kt"))}}, nil); err == nil {
		t.Error("symbols of a missing file: no error")
	}
	// Registration is asynchronous.
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		reg := strings.Join(c.registered, ",")
		c.mu.Unlock()
		if reg == "workspace/didChangeWatchedFiles" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registered %q", reg)
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.shutdown()
}

func TestURI(t *testing.T) {
	for _, p := range []string{"/a/b.kt", "/a b/c#d.kt", "/é/😀.kt"} {
		uri := pathURI(p)
		got, err := uriPath(uri)
		if err != nil || got != p {
			t.Errorf("uriPath(pathURI(%q)) = %q, %v (uri %s)", p, got, err, uri)
		}
	}
	if canonicalURI("file:///a%20b.kt") != canonicalURI("file:///a b.kt") {
		t.Error("escaped and unescaped URIs differ")
	}
	if _, err := uriPath("untitled:Untitled-1"); err == nil {
		t.Error("untitled: URI accepted")
	}
}
