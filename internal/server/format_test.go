package server

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/barteo/kotlin-compiler-go/analysis"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

// applyText returns text with LSP edits made, positions as UTF-16.
func applyText(t *testing.T, text string, edits []protocol.TextEdit) string {
	t.Helper()
	off := func(p protocol.Position) int {
		line, i := uint32(0), 0
		for i < len(text) && line < p.Line {
			if text[i] == '\n' {
				line++
			}
			i++
		}
		return i + int(p.Character) // ASCII text
	}
	sort.Slice(edits, func(i, j int) bool { return off(edits[i].Range.Start) > off(edits[j].Range.Start) })
	for _, e := range edits {
		text = text[:off(e.Range.Start)] + e.NewText + text[off(e.Range.End):]
	}
	return text
}

// TestFormattingAndMoves drives formatting, and the edits of moving a
// file to another package, through the protocol.
func TestFormattingAndMoves(t *testing.T) {
	if _, err := analysis.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	util := "package a.b\n\nclass Util\n"
	client := "package c\n\nimport a.b.Util\n\nfun client( u:Util ){\nprintln(u)\n  }\n"
	writeFiles(t, root, map[string]string{
		"kcls.json":       `{"modules": [{"name": "app", "roots": ["src"], "noJdk": true}]}`,
		"src/a/b/Util.kt": util,
		"src/c/Client.kt": client,
		".editorconfig":   "root = true\n[*.kt]\nindent_size = 2\n",
	})
	c := start(t)
	caps := c.initialize(protocol.ClientCapabilities{}, root).Capabilities
	if !caps.DocumentFormattingProvider || caps.Workspace == nil || caps.Workspace.FileOperations == nil {
		t.Fatalf("capabilities: %+v", caps)
	}
	clientURI := pathURI(filepath.Join(root, "src/c/Client.kt"))
	opts := protocol.FormattingOptions{TabSize: 4, InsertSpaces: true}
	var edits []protocol.TextEdit
	c.must("textDocument/formatting", protocol.DocumentFormattingParams{TextDocument: protocol.TextDocumentIdentifier{URI: clientURI}, Options: opts}, &edits)
	want := "package c\n\nimport a.b.Util\n\nfun client(u: Util) {\n  println(u)\n}\n"
	if got := applyText(t, client, edits); got != want {
		t.Errorf("formatted (indent_size 2 from .editorconfig):\n got %q\nwant %q", got, want)
	}
	c.must("textDocument/onTypeFormatting", protocol.DocumentOnTypeFormattingParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: clientURI}, Position: protocol.Position{Line: 6, Character: 3}, Ch: "}", Options: opts,
	}, &edits)
	if len(edits) != 1 || edits[0].NewText != "\n" {
		t.Errorf("typing } on the last line: %+v", edits)
	}

	var e protocol.WorkspaceEdit
	c.must("workspace/willRenameFiles", protocol.RenameFilesParams{Files: []protocol.FileRename{{
		OldURI: pathURI(filepath.Join(root, "src/a/b")), NewURI: pathURI(filepath.Join(root, "src/a/x")),
	}}}, &e)
	utilURI := pathURI(filepath.Join(root, "src/a/b/Util.kt"))
	if got := applyText(t, util, e.Changes[utilURI]); got != "package a.x\n\nclass Util\n" {
		t.Errorf("moved Util.kt: %q", got)
	}
	if got := applyText(t, client, e.Changes[clientURI]); got != "package c\n\nimport a.x.Util\n\nfun client( u:Util ){\nprintln(u)\n  }\n" {
		t.Errorf("Client.kt: %q", got)
	}
	if code := c.shutdown(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}
