package features

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

// RunCommand is the client's command that a run code lens calls with a
// RunArgs. The client asks the server how to run it with
// PrepareRunCommand, and runs that in a terminal.
const RunCommand = "kcls.run"

// PrepareRunCommand is the server's command that returns the RunPlan of
// a RunArgs.
const PrepareRunCommand = "kcls.prepareRun"

// Run target kinds.
const (
	RunMain  = "main"  // a main function
	RunTest  = "test"  // one test function
	RunTests = "tests" // a class's tests
)

// RunArgs say what a run code lens runs.
type RunArgs struct {
	URI  protocol.DocumentURI `json:"uri"`
	Kind string               `json:"kind"`
	// Class is the JVM name of the class that has the main function or
	// the tests: p.MainKt, p.Outer$Inner.
	Class string `json:"class"`
	// Method is the test function, for RunTest.
	Method string `json:"method,omitempty"`
	// Framework is the tests' framework, from their annotation: junit4,
	// junit5, testng or kotlin.test (whichever kotlin-test maps to).
	Framework string `json:"framework,omitempty"`
}

// RunTarget is a main function or tests a code lens runs.
type RunTarget struct {
	RunArgs
	Node psi.NodeRef // the declaration
}

// testAnnotations maps the annotations that make a function a test to
// their frameworks.
var testAnnotations = map[string]string{
	"org.junit.Test":                             "junit4",
	"org.junit.jupiter.api.Test":                 "junit5",
	"org.junit.jupiter.api.RepeatedTest":         "junit5",
	"org.junit.jupiter.api.TestFactory":          "junit5",
	"org.junit.jupiter.api.TestTemplate":         "junit5",
	"org.junit.jupiter.params.ParameterizedTest": "junit5",
	"org.testng.annotations.Test":                "testng",
	"kotlin.test.Test":                           "kotlin.test",
}

// RunTargets returns the file's main functions, test classes and test
// functions, judged by the psi tree alone: a top-level main with no
// parameters or an Array<String> one, a @JvmStatic main in an object or
// companion object, and the functions of a class annotated as tests of
// JUnit 4 or 5, TestNG or kotlin.test.
func RunTargets(f *File) []RunTarget {
	s := f.Snapshot
	if strings.HasSuffix(s.Path(), ".kts") {
		return nil
	}
	root := s.RootNode()
	pkg := root.ChildKind(psi.KindPACKAGE_DIRECTIVE)
	r := runFinder{f: f, pkg: compactPath(pkg)}
	for imp := root.ChildKind(psi.KindIMPORT_LIST).FirstChild(); imp.Valid(); imp = imp.NextSibling() {
		if imp.NumericKind() != psi.KindIMPORT_DIRECTIVE || imp.Path() == "" {
			continue
		}
		switch {
		case imp.IsAllUnder():
			r.stars = append(r.stars, imp.Path())
		case imp.Alias() != "":
			r.imports = append(r.imports, [2]string{imp.Alias(), imp.Path()})
		default:
			p := imp.Path()
			r.imports = append(r.imports, [2]string{p[strings.LastIndexByte(p, '.')+1:], p})
		}
	}
	mainFound := false
	for c := root.FirstChild(); c.Valid(); c = c.NextSibling() {
		switch c.NumericKind() {
		case psi.KindFUN:
			if !mainFound && topLevelMain(c) {
				mainFound = true
				r.out = append(r.out, RunTarget{RunArgs{URI: f.URI, Kind: RunMain, Class: qualify(r.pkg, facadeName(s))}, c})
			}
		case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
			r.class(c, qualify(r.pkg, c.Name()))
		}
	}
	return r.out
}

type runFinder struct {
	f       *File
	pkg     string
	imports [][2]string // name, qualified name
	stars   []string
	out     []RunTarget
}

// class adds the targets of a class or object whose JVM name is name.
func (r *runFinder) class(c psi.NodeRef, name string) {
	if c.Name() == "" && !hasModifier(c, psi.KindCOMPANION_KEYWORD) {
		return
	}
	body := c.ChildKind(psi.KindCLASS_BODY)
	object := c.NumericKind() == psi.KindOBJECT_DECLARATION
	runnable := !object && !c.ChildKind(psi.KindINTERFACE_KEYWORD).Valid() && !hasModifier(c, psi.KindABSTRACT_KEYWORD) &&
		!hasModifier(c, psi.KindSEALED_KEYWORD) && !hasModifier(c, psi.KindENUM_KEYWORD) && !hasModifier(c, psi.KindANNOTATION_KEYWORD)
	classAt := len(r.out)
	var tests []RunTarget
	for m := body.FirstChild(); m.Valid(); m = m.NextSibling() {
		switch m.NumericKind() {
		case psi.KindFUN:
			if object && m.Name() == "main" && annotated(m, "JvmStatic") && arrayOfStrings(m) {
				owner := name
				if hasModifier(c, psi.KindCOMPANION_KEYWORD) {
					owner = name[:max(strings.LastIndexByte(name, '$'), 0)]
				}
				if owner != "" {
					r.out = append(r.out, RunTarget{RunArgs{URI: r.f.URI, Kind: RunMain, Class: owner}, m})
				}
				continue
			}
			if !runnable || m.Name() == "" {
				continue
			}
			if fw := r.testFramework(m); fw != "" {
				tests = append(tests, RunTarget{RunArgs{URI: r.f.URI, Kind: RunTest, Class: name, Method: m.Name(), Framework: fw}, m})
			}
		case psi.KindCLASS, psi.KindOBJECT_DECLARATION:
			inner := m.Name()
			if inner == "" && hasModifier(m, psi.KindCOMPANION_KEYWORD) {
				inner = "Companion"
			}
			r.class(m, name+"$"+inner)
		}
	}
	if len(tests) > 0 {
		cls := RunTarget{RunArgs{URI: r.f.URI, Kind: RunTests, Class: name, Framework: tests[0].Framework}, c}
		r.out = append(r.out[:classAt], append(append([]RunTarget{cls}, tests...), r.out[classAt:]...)...)
	}
}

// testFramework returns the framework of a test function's annotation,
// or "".
func (r *runFinder) testFramework(fn psi.NodeRef) string {
	for a := fn.ChildKind(psi.KindMODIFIER_LIST).FirstChild(); a.Valid(); a = a.NextSibling() {
		if a.NumericKind() != psi.KindANNOTATION_ENTRY {
			continue
		}
		t := a.ChildKind(psi.KindCONSTRUCTOR_CALLEE).ChildKind(psi.KindTYPE_REFERENCE).ChildKind(psi.KindUSER_TYPE)
		name := compact(t)
		if fw, ok := testAnnotations[name]; ok && strings.Contains(name, ".") {
			return fw
		}
		if strings.Contains(name, ".") {
			continue
		}
		for _, imp := range r.imports {
			if imp[0] == name {
				return testAnnotations[imp[1]]
			}
		}
		for _, pkg := range r.stars {
			if fw, ok := testAnnotations[pkg+"."+name]; ok {
				return fw
			}
		}
	}
	return ""
}

// topLevelMain reports whether a top-level function is a JVM main
// function.
func topLevelMain(fn psi.NodeRef) bool {
	if fn.Name() != "main" || fn.ReceiverType().Valid() || fn.ChildKind(psi.KindTYPE_PARAMETER_LIST).Valid() || !fn.Body().Valid() {
		return false
	}
	if rt := fn.ReturnType(); rt.Valid() && compact(rt) != "Unit" {
		return false
	}
	return len(fn.Parameters()) == 0 || arrayOfStrings(fn)
}

// arrayOfStrings reports whether a function's one parameter is an
// Array<String> or a vararg of strings.
func arrayOfStrings(fn psi.NodeRef) bool {
	ps := fn.Parameters()
	if len(ps) != 1 {
		return false
	}
	t := strings.ReplaceAll(compact(ps[0].TypeReference()), " ", "")
	if hasModifier(ps[0], psi.KindVARARG_KEYWORD) {
		return t == "String" || t == "kotlin.String"
	}
	switch t {
	case "Array<String>", "Array<outString>", "kotlin.Array<kotlin.String>", "Array<kotlin.String>":
		return true
	}
	return false
}

// facadeName returns the simple name of the class of a file's top-level
// functions: the @file:JvmName, else the file's name as kotlinc makes it
// a class name, with Kt.
func facadeName(s *psi.Snapshot) string {
	for a := s.RootNode().ChildKind(psi.KindFILE_ANNOTATION_LIST).FirstChild(); a.Valid(); a = a.NextSibling() {
		if a.NumericKind() != psi.KindANNOTATION_ENTRY {
			continue
		}
		t := compact(a.ChildKind(psi.KindCONSTRUCTOR_CALLEE))
		if t != "JvmName" && t != "kotlin.jvm.JvmName" {
			continue
		}
		args := a.ChildKind(psi.KindVALUE_ARGUMENT_LIST).ChildKind(psi.KindVALUE_ARGUMENT)
		if str := args.ChildKind(psi.KindSTRING_TEMPLATE); str.Valid() {
			if name := strings.Trim(str.Text(), `"`); name != "" && !strings.ContainsAny(name, "$\\{") {
				return name
			}
		}
	}
	base := s.Path()
	base = base[strings.LastIndexAny(base, `/\`)+1:]
	base = strings.TrimSuffix(base, ".kt")
	var b strings.Builder
	for i, r := range base {
		switch {
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) && i > 0:
			b.WriteRune(r)
		case unicode.IsDigit(r):
			b.WriteString("_")
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	r, n := utf8.DecodeRuneInString(name)
	return string(unicode.ToUpper(r)) + name[n:] + "Kt"
}

// compactPath returns a package directive's name, or "".
func compactPath(pkg psi.NodeRef) string {
	var parts []string
	psi.WalkNodes(pkg, func(n psi.NodeRef) bool {
		if n.IsLeaf() && n.NumericKind() == psi.KindIDENTIFIER {
			parts = append(parts, strings.Trim(n.Text(), "`"))
		}
		return true
	})
	return strings.Join(parts, ".")
}

// CodeLenses returns a file's run code lenses: Run on a main function,
// Run tests on a test class, Run test on a test function.
func CodeLenses(f *File) []protocol.CodeLens {
	out := []protocol.CodeLens{}
	for _, t := range RunTargets(f) {
		title := "▶ Run"
		switch t.Kind {
		case RunTest:
			title = "▶ Run test"
		case RunTests:
			title = "▶ Run tests"
		}
		// Above the declaration's annotations, below its KDoc.
		at := t.Node.FirstChild()
		for at.Valid() && (at.IsTrivia() || at.NumericKind() == psi.KindKDOC) {
			at = at.NextSibling()
		}
		if !at.Valid() {
			at = t.Node
		}
		r := f.Range(at)
		out = append(out, protocol.CodeLens{
			Range:   protocol.Range{Start: r.Start, End: r.Start},
			Command: &protocol.Command{Title: title, Command: RunCommand, Arguments: []any{t.RunArgs}},
		})
	}
	return out
}
