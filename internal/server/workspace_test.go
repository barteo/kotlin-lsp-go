package server

import (
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/barteo/kotlin-compiler-go/internal/classpath"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// TestWorkspace drives the requests that span modules: references,
// rename, implementations, the call and type hierarchies and workspace
// symbols, on three modules, one of which can't see the others.
func TestWorkspace(t *testing.T) {
	if _, err := classpath.FindStdlib(); err != nil {
		t.Skipf("no kotlin-stdlib.jar (set KOTLIN_HOME): %v", err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	lib := "package lib\n\ninterface Greeter {\n    fun greet(): String\n}\n\nfun welcome(g: Greeter) = g.greet()\n"
	app := "package app\n\nimport lib.Greeter\nimport lib.welcome\n\nclass Polite : Greeter {\n    override fun greet() = \"good day\"\n}\n\nfun main() {\n    println(welcome(Polite()))\n    Polite().greet()\n}\n"
	other := "package other\n\nclass Greeter {\n    fun greet() = \"hey\"\n}\n\nfun use() = Greeter().greet()\n"
	writeFiles(t, root, map[string]string{
		"kcls.json": `{"modules": [
  {"name": "lib", "roots": ["lib"], "noJdk": true},
  {"name": "app", "roots": ["app"], "dependsOn": ["lib"], "noJdk": true},
  {"name": "other", "roots": ["other"], "noJdk": true}
]}`,
		"lib/Greeter.kt": lib,
		"app/Main.kt":    app,
		"other/Other.kt": other,
	})
	c := start(t)
	caps := c.initialize(protocol.ClientCapabilities{}, root).Capabilities
	if !caps.ReferencesProvider || !caps.ImplementationProvider || caps.RenameProvider == nil || !caps.RenameProvider.PrepareProvider ||
		!caps.CallHierarchyProvider || !caps.TypeHierarchyProvider || !caps.WorkspaceSymbolProvider {
		t.Fatalf("capabilities: %+v", caps)
	}
	libURI := pathURI(filepath.Join(root, "lib/Greeter.kt"))
	appURI := pathURI(filepath.Join(root, "app/Main.kt"))
	names := func(locs []protocol.Location) []string {
		var out []string
		for _, l := range locs {
			out = append(out, filepath.Base(string(l.URI))+":"+strconv.Itoa(int(l.Range.Start.Line)+1))
		}
		slices.Sort(out)
		return out
	}

	// greet's references: lib's, and app's call of its override; not
	// other's greet.
	greet := at(t, lib, "greet(): String", 0)
	var locs []protocol.Location
	c.must("textDocument/references", protocol.ReferenceParams{TextDocumentPositionParams: position(libURI, greet), Context: protocol.ReferenceContext{IncludeDeclaration: true}}, &locs)
	if got, want := names(locs), []string{"Greeter.kt:4", "Greeter.kt:7", "Main.kt:12"}; !slices.Equal(got, want) {
		t.Errorf("references of greet: %v, want %v", got, want)
	}
	// Greeter's: in lib and app.
	c.must("textDocument/references", protocol.ReferenceParams{TextDocumentPositionParams: position(libURI, at(t, lib, "Greeter {", 0))}, &locs)
	if got, want := names(locs), []string{"Greeter.kt:7", "Main.kt:3", "Main.kt:6"}; !slices.Equal(got, want) {
		t.Errorf("references of Greeter: %v, want %v", got, want)
	}

	// greet's implementation is in app.
	c.must("textDocument/implementation", position(libURI, greet), &locs)
	if got, want := names(locs), []string{"Main.kt:7"}; !slices.Equal(got, want) {
		t.Errorf("implementations of greet: %v, want %v", got, want)
	}

	// Renaming greet renames its override and the calls of both.
	var pr protocol.PrepareRenameResult
	c.must("textDocument/prepareRename", position(libURI, greet), &pr)
	if pr.Placeholder != "greet" {
		t.Errorf("prepare rename: %+v", pr)
	}
	var edit protocol.WorkspaceEdit
	c.must("textDocument/rename", protocol.RenameParams{TextDocumentPositionParams: position(libURI, greet), NewName: "hello"}, &edit)
	if n, m := len(edit.Changes[libURI]), len(edit.Changes[appURI]); n != 2 || m != 2 || len(edit.Changes) != 2 {
		t.Errorf("rename of greet: %+v", edit.Changes)
	}
	if err := c.call("textDocument/rename", protocol.RenameParams{TextDocumentPositionParams: position(libURI, greet), NewName: "no way"}, &edit); err == nil {
		t.Error("rename to a name with a space: no error")
	}
	// The standard library's println can't be renamed.
	if err := c.call("textDocument/prepareRename", position(appURI, at(t, app, "println", 0)), &pr); err == nil {
		t.Error("prepare rename of println: no error")
	}

	// welcome's callers, and what main calls.
	var items []protocol.HierarchyItem
	c.must("textDocument/prepareCallHierarchy", position(libURI, at(t, lib, "welcome", 0)), &items)
	if len(items) != 1 || items[0].Name != "welcome" {
		t.Fatalf("prepare call hierarchy: %+v", items)
	}
	var in []protocol.CallHierarchyIncomingCall
	c.must("callHierarchy/incomingCalls", protocol.HierarchyItemParams{Item: items[0]}, &in)
	if len(in) != 1 || in[0].From.Name != "main" || len(in[0].FromRanges) != 1 {
		t.Errorf("incoming calls of welcome: %+v", in)
	}
	c.must("textDocument/prepareCallHierarchy", position(appURI, at(t, app, "main", 0)), &items)
	var out []protocol.CallHierarchyOutgoingCall
	c.must("callHierarchy/outgoingCalls", protocol.HierarchyItemParams{Item: items[0]}, &out)
	var callees []string
	for _, o := range out {
		callees = append(callees, o.To.Name)
	}
	if !slices.Contains(callees, "welcome") || !slices.Contains(callees, "greet") {
		t.Errorf("outgoing calls of main: %v", callees)
	}

	// Greeter's subtypes, and Polite's supertypes.
	c.must("textDocument/prepareTypeHierarchy", position(libURI, at(t, lib, "Greeter {", 0)), &items)
	if len(items) != 1 {
		t.Fatalf("prepare type hierarchy: %+v", items)
	}
	var subs []protocol.HierarchyItem
	c.must("typeHierarchy/subtypes", protocol.HierarchyItemParams{Item: items[0]}, &subs)
	if len(subs) != 1 || subs[0].Name != "Polite" {
		t.Fatalf("subtypes of Greeter: %+v", subs)
	}
	var sups []protocol.HierarchyItem
	c.must("typeHierarchy/supertypes", protocol.HierarchyItemParams{Item: subs[0]}, &sups)
	if len(sups) != 1 || sups[0].Name != "Greeter" || sups[0].URI != libURI {
		t.Errorf("supertypes of Polite: %+v", sups)
	}

	// Workspace symbols: both Greeters, by camel humps too.
	var syms []protocol.SymbolInformation
	c.must("workspace/symbol", protocol.WorkspaceSymbolParams{Query: "Greeter"}, &syms)
	var found []string
	for _, s := range syms {
		if s.Name == "Greeter" {
			found = append(found, s.ContainerName)
		}
	}
	slices.Sort(found)
	if !slices.Equal(found, []string{"lib", "other"}) {
		t.Errorf("workspace symbols Greeter: %+v", syms)
	}
	c.must("workspace/symbol", protocol.WorkspaceSymbolParams{Query: "wel"}, &syms)
	if len(syms) == 0 || syms[0].Name != "welcome" {
		t.Errorf("workspace symbols wel: %+v", syms)
	}

	// Semantic tokens from the analysis: greet is a method.
	var toks protocol.SemanticTokens
	c.must("textDocument/semanticTokens/full", protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: appURI}}, &toks)
	if len(toks.Data) == 0 || len(toks.Data)%5 != 0 {
		t.Errorf("semantic tokens: %v", toks.Data)
	}
	c.shutdown()
}
