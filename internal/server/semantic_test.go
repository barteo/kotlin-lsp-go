package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/classpath"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// at returns the position of sub in text, plus delta characters.
func at(t *testing.T, text, sub string, delta int) protocol.Position {
	t.Helper()
	i := strings.Index(text, sub)
	if i < 0 {
		t.Fatalf("%q not in the text", sub)
	}
	m := protocol.NewMapper(text, protocol.UTF16)
	return m.Position(i + delta)
}

func position(uri protocol.DocumentURI, p protocol.Position) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: p}
}

// TestSemantic drives the semantic features through the protocol on a
// workspace of two modules described by a kcls.json.
func TestSemantic(t *testing.T) {
	if _, err := classpath.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	app := "package app\n\nimport lib.Greeter\n\nfun main() {\n    val g = Greeter(\"x\")\n    println(g.greet() + missing)\n}\n"
	writeFiles(t, root, map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "app", "roots": ["app"], "dependsOn": ["lib"], "noJdk": true}
]}`,
		"lib/Greeter.kt": "package lib\n\n/** Says hello. */\nclass Greeter(val name: String) {\n    fun greet(): String = \"hi \" + name\n}\n",
		"app/Main.kt":    app,
	})
	c := start(t)
	res := c.initialize(protocol.ClientCapabilities{
		TextDocument: &protocol.TextDocumentClientCapabilities{
			Definition: &protocol.LinkClientCapabilities{LinkSupport: true},
		},
	}, root)
	caps := res.Capabilities
	if !caps.HoverProvider || !caps.DefinitionProvider || !caps.TypeDefinitionProvider || !caps.DocumentHighlightProvider {
		t.Fatalf("capabilities: %+v", caps)
	}
	if caps.ExecuteCommandProvider == nil || len(caps.ExecuteCommandProvider.Commands) != 3 {
		t.Fatalf("commands: %+v", caps.ExecuteCommandProvider)
	}
	uri := pathURI(filepath.Join(root, "app/Main.kt"))
	c.notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "kotlin", Version: 1, Text: app}})
	d := c.nextDiagnostics()
	if len(d.Diagnostics) != 1 || !strings.Contains(d.Diagnostics[0].Message, "missing") || d.Diagnostics[0].Source != "kc" {
		t.Fatalf("diagnostics: %+v", d)
	}
	if r := d.Diagnostics[0].Range; int(r.End.Character-r.Start.Character) != len("missing") {
		t.Errorf("unresolved reference's range %+v", r)
	}

	var h protocol.Hover
	c.must("textDocument/hover", position(uri, at(t, app, "greet()", 0)), &h)
	if !strings.Contains(h.Contents.Value, "fun greet(): String") || h.Contents.Kind != "markdown" {
		t.Errorf("hover of greet: %+v", h)
	}
	c.must("textDocument/hover", position(uri, at(t, app, "Greeter(", 2)), &h)
	if !strings.Contains(h.Contents.Value, "class Greeter(val name: String)") || !strings.Contains(h.Contents.Value, "Says hello.") {
		t.Errorf("hover of Greeter: %+v", h)
	}

	var links []protocol.LocationLink
	c.must("textDocument/definition", position(uri, at(t, app, "greet()", 1)), &links)
	if len(links) != 1 || !strings.HasSuffix(string(links[0].TargetURI), "lib/Greeter.kt") || links[0].TargetSelectionRange.Start.Line != 4 {
		t.Fatalf("definition of greet: %+v", links)
	}
	c.must("textDocument/typeDefinition", position(uri, at(t, app, "g.greet", 0)), &links)
	var locs []protocol.Location
	c.must("textDocument/typeDefinition", position(uri, at(t, app, "g.greet", 0)), &locs)
	if len(locs) != 1 || locs[0].Range.Start.Line != 3 {
		t.Errorf("type definition of g (locations, no link support): %+v", locs)
	}
	var hs []protocol.DocumentHighlight
	c.must("textDocument/documentHighlight", position(uri, at(t, app, "val g", 4)), &hs)
	if len(hs) != 2 {
		t.Errorf("highlights of g: %+v", hs)
	}

	// An agent's request on a file it never opened.
	lib := pathURI(filepath.Join(root, "lib/Greeter.kt"))
	c.must("textDocument/hover", position(lib, protocol.Position{Line: 4, Character: 25}), &h)
	if !strings.Contains(h.Contents.Value, "String") {
		t.Errorf("hover in an unopened file: %+v", h)
	}
	// It renames greet on disk: Main.kt's diagnostics follow.
	writeFiles(t, root, map[string]string{"lib/Greeter.kt": "package lib\n\nclass Greeter(val name: String) {\n    fun hello(): String = name\n}\n"})
	c.notify("workspace/didChangeWatchedFiles", protocol.DidChangeWatchedFilesParams{Changes: []protocol.FileEvent{{URI: lib, Type: protocol.FileChanged}}})
	d = c.nextDiagnostics()
	if len(d.Diagnostics) != 2 {
		t.Errorf("diagnostics after greet's rename on disk: %+v", d.Diagnostics)
	}

	if err := c.call("workspace/executeCommand", protocol.ExecuteCommandParams{Command: "kcls.reimport"}, nil); err != nil {
		t.Errorf("reimport: %v", err)
	}
	if err := c.call("workspace/executeCommand", protocol.ExecuteCommandParams{Command: "kcls.nope"}, nil); err == nil {
		t.Error("unknown command: no error")
	}
	c.shutdown()
}

// TestPullDiagnostics checks semantic diagnostics pulled by the client,
// and the refresh request after a change on disk.
func TestPullDiagnostics(t *testing.T) {
	if _, err := classpath.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	writeFiles(t, root, map[string]string{
		"kcls.json": `{"modules": [{"name": "m", "roots": ["."], "noJdk": true}]}`,
		"a.kt":      "fun a(): Int = b()\n",
		"b.kt":      "fun b(): String = \"\"\n",
	})
	c := start(t)
	c.initialize(protocol.ClientCapabilities{
		TextDocument: &protocol.TextDocumentClientCapabilities{Diagnostic: &protocol.DynamicRegistration{}},
		Workspace:    &protocol.WorkspaceClientCapabilities{Diagnostics: &protocol.RefreshCapability{RefreshSupport: true}},
	}, root)
	uri := pathURI(filepath.Join(root, "a.kt"))
	var rep protocol.DocumentDiagnosticReport
	c.must("textDocument/diagnostic", protocol.DocumentDiagnosticParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &rep)
	if len(rep.Items) != 1 || rep.Items[0].Severity != protocol.SeverityError {
		t.Fatalf("diagnostics: %+v", rep.Items)
	}
	writeFiles(t, root, map[string]string{"b.kt": "fun b(): Int = 1\n"})
	c.notify("workspace/didChangeWatchedFiles", protocol.DidChangeWatchedFilesParams{Changes: []protocol.FileEvent{{URI: pathURI(filepath.Join(root, "b.kt")), Type: protocol.FileChanged}}})
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		n := c.refreshes
		c.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no workspace/diagnostic/refresh")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.must("textDocument/diagnostic", protocol.DocumentDiagnosticParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &rep)
	if len(rep.Items) != 0 {
		raw, _ := json.Marshal(rep.Items)
		t.Fatalf("diagnostics after b's fix: %s", raw)
	}
	c.shutdown()
}
