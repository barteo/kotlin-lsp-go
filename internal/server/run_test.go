package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// TestRunLenses gets a main function's run lens and the plan of running
// it through the protocol.
func TestRunLenses(t *testing.T) {
	bin := t.TempDir()
	kc := filepath.Join(bin, "kc")
	if err := os.WriteFile(kc, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root, _ := filepath.EvalSymlinks(t.TempDir())
	writeFiles(t, root, map[string]string{
		"kcls.json":       `{"modules": [{"name": "app", "roots": ["src"], "noJdk": false}]}`,
		"src/app/Main.kt": "package app\n\nfun main() {\n    println(1)\n}\n",
	})
	c := start(t)
	caps := c.initialize(protocol.ClientCapabilities{}, root).Capabilities
	if caps.CodeLensProvider == nil || caps.ExecuteCommandProvider == nil || !strings.Contains(strings.Join(caps.ExecuteCommandProvider.Commands, ","), features.PrepareRunCommand) {
		t.Fatalf("capabilities: %+v", caps)
	}
	uri := pathURI(filepath.Join(root, "src/app/Main.kt"))
	var lenses []protocol.CodeLens
	c.must("textDocument/codeLens", protocol.CodeLensParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}}, &lenses)
	if len(lenses) != 1 || lenses[0].Command.Command != features.RunCommand || lenses[0].Range.Start.Line != 2 {
		t.Fatalf("lenses: %+v", lenses)
	}
	b, _ := json.Marshal(lenses[0].Command.Arguments[0])
	var plan features.RunPlan
	c.must("workspace/executeCommand", protocol.ExecuteCommandParams{Command: features.PrepareRunCommand, Arguments: []json.RawMessage{b}}, &plan)
	if plan.Name != "MainKt" || plan.Cwd != root || !strings.HasPrefix(plan.Command, kc+" @") || !strings.HasSuffix(plan.Command, "java.args") {
		t.Errorf("plan: %+v", plan)
	}
	if code := c.shutdown(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// TestNonJVMSourceSet reports that a file of a source set no JVM
// compilation compiles is analyzed on its own.
func TestNonJVMSourceSet(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	writeFiles(t, root, map[string]string{
		"kcls.json":                 `{"modules": [{"name": "jvm", "roots": ["src/jvmMain/kotlin"], "noJdk": true}]}`,
		"src/jvmMain/kotlin/a/A.kt": "package a\n\nclass A\n",
		"src/jsMain/kotlin/a/B.kt":  "package a\n\nclass B\n",
	})
	c := start(t)
	c.initialize(protocol.ClientCapabilities{}, root)
	for _, tc := range []struct {
		file string
		want bool
	}{{"src/jsMain/kotlin/a/B.kt", true}, {"src/jvmMain/kotlin/a/A.kt", false}} {
		path := filepath.Join(root, tc.file)
		uri := pathURI(path)
		c.notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "kotlin", Version: 1, Text: mustReadFile(t, path)}})
		d := c.nextDiagnostics()
		for d.URI != uri {
			d = c.nextDiagnostics()
		}
		got := len(d.Diagnostics) > 0 && d.Diagnostics[0].Code == "NOT_COMPILED_FOR_JVM"
		if got != tc.want {
			t.Errorf("%s: %+v", tc.file, d.Diagnostics)
		}
	}
	if code := c.shutdown(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

func mustReadFile(t *testing.T, path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
