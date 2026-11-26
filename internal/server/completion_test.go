package server

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// TestCompletion drives completion, its resolve and signature help
// through the protocol, on an open document being edited.
func TestCompletion(t *testing.T) {
	if _, err := analysis.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	lib := "package lib\n\n/** Says hello. */\nclass Greeter(val name: String) {\n    /** Greets [times] times. */\n    fun greet(times: Int): String = \"hi \" + name\n}\n"
	writeFiles(t, root, map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "app", "roots": ["app"], "dependsOn": ["lib"], "noJdk": true}
]}`,
		"lib/Greeter.kt": lib,
		"app/Main.kt":    "package app\n",
	})
	c := start(t)
	caps := c.initialize(protocol.ClientCapabilities{TextDocument: &protocol.TextDocumentClientCapabilities{
		Completion: &protocol.CompletionClientCapabilities{CompletionItem: &protocol.CompletionItemClientCapabilities{SnippetSupport: true}},
		SignatureHelp: &protocol.SignatureHelpClientCapabilities{SignatureInformation: &protocol.SignatureInformationClientCapabilities{
			ParameterInformation: &protocol.ParameterInformationClientCapabilities{LabelOffsetSupport: true},
		}},
	}}, root).Capabilities
	if caps.CompletionProvider == nil || !caps.CompletionProvider.ResolveProvider || caps.SignatureHelpProvider == nil {
		t.Fatalf("capabilities: %+v", caps)
	}
	uri := pathURI(filepath.Join(root, "app/Main.kt"))
	app := "package app\n\nfun main() {\n    val g = Gre\n}\n"
	c.notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "kotlin", Version: 1, Text: app}})
	complete := func(text, sub string, delta int) protocol.CompletionList {
		t.Helper()
		var l protocol.CompletionList
		c.must("textDocument/completion", protocol.CompletionParams{TextDocumentPositionParams: position(uri, at(t, text, sub, delta))}, &l)
		return l
	}
	find := func(l protocol.CompletionList, label string) *protocol.CompletionItem {
		for i := range l.Items {
			if l.Items[i].Label == label {
				return &l.Items[i]
			}
		}
		t.Fatalf("no %s in %+v", label, l.Items)
		return nil
	}

	// A class of another module's package, imported.
	l := complete(app, "Gre\n", 3)
	it := find(l, "Greeter")
	if len(it.AdditionalTextEdits) != 1 || it.AdditionalTextEdits[0].NewText != "\n\nimport lib.Greeter" {
		t.Errorf("Greeter's import: %+v", it.AdditionalTextEdits)
	}
	if it.Documentation == nil || !strings.Contains(it.Documentation.Value, "Says hello") {
		t.Errorf("Greeter's documentation: %+v", it.Documentation)
	}

	// Members, after an edit.
	app2 := "package app\n\nimport lib.Greeter\n\nfun main() {\n    val g = Greeter(\"x\")\n    g.\n}\n"
	c.notify("textDocument/didChange", protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: app2}},
	})
	l = complete(app2, "g.\n", 2)
	it = find(l, "greet")
	if it.TextEdit == nil || it.TextEdit.NewText != "greet($0)" || it.InsertTextFormat != protocol.SnippetFormat {
		t.Errorf("greet: %+v", it)
	}
	find(l, "name")

	// A library member's documentation comes from resolve: with no
	// sources jar in the test, none, but the item comes back.
	l = complete(app2, "g.\n", 2)
	it = find(l, "toString")
	var resolved protocol.CompletionItem
	c.must("completionItem/resolve", it, &resolved)
	if resolved.Label != "toString" {
		t.Errorf("resolved: %+v", resolved)
	}

	// Signature help in greet's arguments.
	app3 := "package app\n\nimport lib.Greeter\n\nfun main() {\n    val g = Greeter(\"x\")\n    g.greet()\n}\n"
	c.notify("textDocument/didChange", protocol.DidChangeTextDocumentParams{
		TextDocument:   protocol.VersionedTextDocumentIdentifier{URI: uri, Version: 3},
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: app3}},
	})
	var h protocol.SignatureHelp
	c.must("textDocument/signatureHelp", protocol.SignatureHelpParams{TextDocumentPositionParams: position(uri, at(t, app3, "greet()", 6))}, &h)
	if len(h.Signatures) != 1 || h.Signatures[0].Label != "greet(times: Int): String" || h.ActiveParameter != 0 {
		t.Fatalf("signature help: %+v", h)
	}
	s := h.Signatures[0]
	if lbl, ok := s.Parameters[0].Label.([]any); !ok || len(lbl) != 2 || lbl[0].(float64) != 6 || lbl[1].(float64) != 16 {
		t.Errorf("parameter label: %#v", s.Parameters[0].Label)
	}
	if s.Documentation == nil || !strings.Contains(s.Documentation.Value, "Greets") {
		t.Errorf("signature documentation: %+v", s.Documentation)
	}
	c.shutdown()
}
