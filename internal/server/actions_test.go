package server

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/classpath"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// TestActions drives inlay hints, their settings, and code actions
// through the protocol.
func TestActions(t *testing.T) {
	if _, err := classpath.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	src := "package app\n\nimport kotlin.math.min\n\nfun area(width: Int, height: Int) = width * height\n\nfun main() {\n    val n = area(2, 3)\n    println(abs(n))\n}\n"
	writeFiles(t, root, map[string]string{
		"kcls.json":   `{"modules": [{"name": "app", "roots": ["app"], "noJdk": true}]}`,
		"app/Main.kt": src,
	})
	c := start(t)
	rootURI := pathURI(root)
	var res protocol.InitializeResult
	c.must("initialize", protocol.InitializeParams{
		RootURI:               &rootURI,
		InitializationOptions: json.RawMessage(`{"hints": {"parameterNames": "none"}}`),
		Capabilities:          protocol.ClientCapabilities{Workspace: &protocol.WorkspaceClientCapabilities{InlayHint: &protocol.RefreshCapability{RefreshSupport: true}}},
	}, &res)
	c.notify("initialized", protocol.InitializedParams{})
	if !res.Capabilities.InlayHintProvider || res.Capabilities.CodeActionProvider == nil {
		t.Fatalf("capabilities: %+v", res.Capabilities)
	}
	uri := pathURI(filepath.Join(root, "app/Main.kt"))
	c.notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "kotlin", Version: 1, Text: src}})
	whole := protocol.Range{End: protocol.Position{Line: 20}}
	hints := func() []string {
		t.Helper()
		var hs []protocol.InlayHint
		c.must("textDocument/inlayHint", protocol.InlayHintParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Range: whole}, &hs)
		var out []string
		for _, h := range hs {
			out = append(out, h.Label)
		}
		return out
	}
	if got := hints(); !slices.Equal(got, []string{": Int", ": Int"}) {
		t.Errorf("hints with parameterNames none: %q", got)
	}

	// The setting changed: the client is asked to refresh.
	c.notify("workspace/didChangeConfiguration", protocol.DidChangeConfigurationParams{Settings: json.RawMessage(`{"kcls": {"hints": {"parameterNames": "literals"}}}`)})
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		n := c.hintRefreshes
		c.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no workspace/inlayHint/refresh")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := hints(); !slices.Equal(got, []string{": Int", ": Int", "width:", "height:"}) {
		t.Errorf("hints with parameterNames literals: %q", got)
	}

	// The fixes of the diagnostics.
	var diags []protocol.Diagnostic
	for len(diags) == 0 {
		if p := c.nextDiagnostics(); p.URI == uri {
			diags = p.Diagnostics
		}
	}
	i := slices.IndexFunc(diags, func(d protocol.Diagnostic) bool { return strings.HasPrefix(d.Message, "unresolved reference: abs") })
	if i < 0 {
		t.Fatalf("diagnostics: %+v", diags)
	}
	var acts []protocol.CodeAction
	c.must("textDocument/codeAction", protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Range:        diags[i].Range,
		Context:      protocol.CodeActionContext{Diagnostics: []protocol.Diagnostic{diags[i]}},
	}, &acts)
	var titles []string
	for _, a := range acts {
		titles = append(titles, a.Title+" "+a.Kind)
	}
	if !slices.Contains(titles, "Import 'kotlin.math.abs' quickfix") || !slices.Contains(titles, "Organize imports source.organizeImports") {
		t.Errorf("code actions: %q", titles)
	}

	// Organize imports alone, as on save.
	c.must("textDocument/codeAction", protocol.CodeActionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Context:      protocol.CodeActionContext{Only: []string{protocol.SourceOrganizeImports}},
	}, &acts)
	if len(acts) != 1 || acts[0].Edit == nil || len(acts[0].Edit.Changes[uri]) != 1 || acts[0].Edit.Changes[uri][0].NewText != "" {
		t.Errorf("organize imports: %+v", acts)
	}
	if code := c.shutdown(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}
