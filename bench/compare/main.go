// Command compare drives any Kotlin language server over stdio, as an
// editor does, on one workspace, and reports latencies and memory, so
// different servers can be measured on the same workload:
//
//	go run ./bench/compare -name kcls -root dir -- kcls
//	go run ./bench/compare -name kotlin-lsp -diag pull -root dir -- intellij-server --stdio
//
// Unlike bench/lsp it waits for semantic results, not just any
// diagnostics: the opened file gets an unresolved reference, and start
// time is to the diagnostics that report it; each edit adds a new
// unresolved reference and its time is to the diagnostics that name it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-compiler-go/psi"
	"github.com/barteo/kotlin-lsp-go/internal/jsonrpc"
	"github.com/barteo/kotlin-lsp-go/internal/protocol"
)

var (
	name     = flag.String("name", "server", "the server's name in the report")
	root     = flag.String("root", "", "the workspace")
	file     = flag.String("file", "", "the file to open, relative to the workspace (default: its largest .kt file)")
	edits    = flag.Int("edits", 20, "body edits to measure")
	sites    = flag.Int("sites", 30, "member accesses to complete, go to and hover")
	diagMode = flag.String("diag", "push", "how to get diagnostics: push or pull")
	timeout  = flag.Duration("timeout", 10*time.Minute, "how long to wait for the first semantic diagnostics")
	logFile  = flag.String("log", "", "write the server's stderr to this file")
	jsonOut  = flag.String("json", "", "append the results as a JSON line to this file")
	initOpts = flag.String("init-options", "", "initializationOptions, as JSON")
	warm     = flag.Int("warm", 0, "completion/definition rounds to run before measuring")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench/compare:", err)
		os.Exit(2)
	}
}

type client struct {
	conn *jsonrpc.Conn
	uri  protocol.DocumentURI

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on each publish
	last    []protocol.Diagnostic
	lastVer int32
	pushes  int
}

func (c *client) handle(_ context.Context, _ *jsonrpc.Conn, req *jsonrpc.Request) (any, error) {
	switch req.Method {
	case "textDocument/publishDiagnostics":
		var p protocol.PublishDiagnosticsParams
		if json.Unmarshal(req.Params, &p) == nil && p.URI == c.uri {
			c.mu.Lock()
			c.last = p.Diagnostics
			if p.Version != nil {
				c.lastVer = *p.Version
			}
			c.pushes++
			close(c.changed)
			c.changed = make(chan struct{})
			c.mu.Unlock()
		}
		return nil, nil
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		json.Unmarshal(req.Params, &p)
		return make([]any, len(p.Items)), nil
	}
	return nil, nil
}

func has(diags []protocol.Diagnostic, marker string) bool {
	for _, d := range diags {
		if strings.Contains(d.Message, marker) {
			return true
		}
	}
	return false
}

// waitDiags waits until the diagnostics of the document satisfy ok.
func (c *client) waitDiags(ctx context.Context, ok func([]protocol.Diagnostic) bool, d time.Duration) error {
	_, err := c.waitDiagsGet(ctx, ok, d)
	return err
}

func (c *client) waitDiagsGet(ctx context.Context, ok func([]protocol.Diagnostic) bool, d time.Duration) ([]protocol.Diagnostic, error) {
	deadline := time.Now().Add(d)
	if *diagMode == "pull" {
		for time.Now().Before(deadline) {
			var res struct {
				Kind  string                `json:"kind"`
				Items []protocol.Diagnostic `json:"items"`
			}
			cctx, cancel := context.WithDeadline(ctx, deadline)
			err := c.conn.Call(cctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]any{"uri": c.uri}}, &res)
			cancel()
			if err == nil && ok(res.Items) {
				return res.Items, nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		return nil, fmt.Errorf("no matching diagnostics in %v", d)
	}
	for {
		c.mu.Lock()
		last, ch := c.last, c.changed
		c.mu.Unlock()
		if ok(last) {
			return last, nil
		}
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
			return nil, fmt.Errorf("no matching diagnostics in %v", d)
		}
	}
}

func run() error {
	if flag.NArg() == 0 {
		return fmt.Errorf("no server command after --")
	}
	dir, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	path := *file
	if path == "" {
		path = largest(dir)
	} else {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	at := bodyStart(path, text)
	if at < 0 {
		return fmt.Errorf("no function with a block body in %s", path)
	}
	withRef := func(ref string) string { return text[:at] + " val probe = " + ref + ";" + text[at:] }

	start := time.Now()
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Dir = dir
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if *logFile != "" {
		f, err := os.Create(*logFile)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer killTree(cmd.Process.Pid)
	uri := protocol.DocumentURI("file://" + path)
	c := &client{uri: uri, changed: make(chan struct{})}
	c.conn = jsonrpc.NewConn(stdout, stdin, c.handle, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.conn.Run(ctx)

	rootURI := protocol.DocumentURI("file://" + dir)
	caps := map[string]any{
		"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "diagnostics": map[string]any{"refreshSupport": false}},
		"textDocument": map[string]any{
			"synchronization":    map[string]any{"didSave": true},
			"publishDiagnostics": map[string]any{"versionSupport": true, "relatedInformation": true},
			"completion":         map[string]any{"completionItem": map[string]any{"snippetSupport": true, "labelDetailsSupport": true}},
			"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
			"definition":         map[string]any{"linkSupport": true},
		},
		"window": map[string]any{"workDoneProgress": true},
	}
	if *diagMode == "pull" {
		caps["textDocument"].(map[string]any)["diagnostic"] = map[string]any{"dynamicRegistration": false}
	}
	params := map[string]any{
		"processId": os.Getpid(), "rootUri": rootURI, "rootPath": dir,
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": filepath.Base(dir)}},
		"capabilities":     caps,
	}
	if *initOpts != "" {
		params["initializationOptions"] = json.RawMessage(*initOpts)
	}
	var initRes json.RawMessage
	if err := c.conn.Call(ctx, "initialize", params, &initRes); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	initTime := time.Since(start)
	if f := os.Getenv("COMPARE_DUMP_INIT"); f != "" {
		os.WriteFile(f, initRes, 0o644)
	}
	c.conn.Notify("initialized", map[string]any{})
	version := int32(1)
	c.conn.Notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "kotlin", "version": version, "text": withRef("undefinedCold")}})
	coldDiags, err := c.waitDiagsGet(ctx, func(d []protocol.Diagnostic) bool { return has(d, "undefinedCold") }, *timeout)
	if err != nil {
		return fmt.Errorf("first semantic diagnostics: %w", err)
	}
	cold := time.Since(start)
	rssCold := rssTreeMB(cmd.Process.Pid)
	otherErrs, warns := 0, 0
	for _, d := range coldDiags {
		if strings.Contains(d.Message, "undefinedCold") {
			continue
		}
		if d.Severity == 1 {
			otherErrs++
			if os.Getenv("COMPARE_SHOW_ERRORS") != "" {
				fmt.Fprintf(os.Stderr, "error %d:%d %s\n", d.Range.Start.Line, d.Range.Start.Character, d.Message)
			}
		} else {
			warns++
		}
	}

	change := func(t string) {
		version++
		c.conn.Notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": t}},
		})
	}
	var editTimes []time.Duration
	for i := 0; i < *edits; i++ {
		marker := fmt.Sprintf("undefinedBench%d", i)
		t0 := time.Now()
		change(withRef(marker))
		if err := c.waitDiags(ctx, func(d []protocol.Diagnostic) bool { return has(d, marker) }, time.Minute); err != nil {
			return fmt.Errorf("edit %d: %w", i, err)
		}
		editTimes = append(editTimes, time.Since(t0))
	}
	t0 := time.Now()
	change(text)
	if err := c.waitDiags(ctx, func(d []protocol.Diagnostic) bool { return !has(d, "undefinedBench") }, time.Minute); err != nil {
		return fmt.Errorf("revert: %w", err)
	}
	revert := time.Since(t0)

	mapper := protocol.NewMapper(text, protocol.UTF16)
	accesses := memberAccesses(path, text, *sites)
	var compTimes, defTimes, hoverTimes []time.Duration
	compItems, defHits, hoverHits := 0, 0, 0
	measure := func(record bool) error {
		for _, off := range accesses {
			p := mapper.Position(off)
			pos := map[string]any{"textDocument": map[string]any{"uri": uri}, "position": p}
			var comp json.RawMessage
			t0 := time.Now()
			if err := c.conn.Call(ctx, "textDocument/completion", pos, &comp); err != nil {
				return fmt.Errorf("completion: %w", err)
			}
			dc := time.Since(t0)
			// definition and hover on the member's name, just after the dot.
			np := map[string]any{"textDocument": map[string]any{"uri": uri}, "position": protocol.Position{Line: p.Line, Character: p.Character + 1}}
			var def json.RawMessage
			t0 = time.Now()
			if err := c.conn.Call(ctx, "textDocument/definition", np, &def); err != nil {
				return fmt.Errorf("definition: %w", err)
			}
			dd := time.Since(t0)
			var hov json.RawMessage
			t0 = time.Now()
			if err := c.conn.Call(ctx, "textDocument/hover", np, &hov); err != nil {
				return fmt.Errorf("hover: %w", err)
			}
			dh := time.Since(t0)
			if record && os.Getenv("COMPARE_SHOW_DEFS") != "" {
				line := strings.SplitN(text[strings.LastIndexByte(text[:off], '\n')+1:], "\n", 2)[0]
				fmt.Fprintf(os.Stderr, "def %d:%d %q -> %.200s\n", p.Line, p.Character+1, strings.TrimSpace(line), def)
			}
			if record {
				compTimes, defTimes, hoverTimes = append(compTimes, dc), append(defTimes, dd), append(hoverTimes, dh)
				compItems += countItems(comp)
				if n := countLocs(def); n > 0 {
					defHits++
				}
				if s := string(hov); s != "null" && s != "" {
					hoverHits++
				}
			}
		}
		return nil
	}
	for i := 0; i < *warm; i++ {
		if err := measure(false); err != nil {
			return err
		}
	}
	if err := measure(true); err != nil {
		return err
	}
	rss := rssTreeMB(cmd.Process.Pid)

	sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
	c.conn.Call(sctx, "shutdown", nil, nil)
	scancel()
	c.conn.Notify("exit", nil)

	n, _ := countKt(dir)
	fmt.Printf("%s on %s (%d .kt files), opened %s; diagnostics by %s\n\n", *name, dir, n, filepath.Base(path), *diagMode)
	fmt.Println("| measurement | p50 | p95 |")
	fmt.Println("|---|---|---|")
	fmt.Printf("| initialize response | — | %s |\n", ms(initTime))
	fmt.Printf("| start to first semantic diagnostics | — | %s |\n", ms(cold))
	fmt.Printf("| other diagnostics at cold start | — | %d errors, %d warnings/hints |\n", otherErrs, warns)
	fmt.Printf("| body edit to its diagnostics (%d) | %s | %s |\n", len(editTimes), ms(pct(editTimes, 50)), ms(pct(editTimes, 95)))
	fmt.Printf("| completion after dot (%d, avg %d items) | %s | %s |\n", len(compTimes), compItems/max(len(compTimes), 1), ms(pct(compTimes, 50)), ms(pct(compTimes, 95)))
	fmt.Printf("| definition (%d, %d resolved) | %s | %s |\n", len(defTimes), defHits, ms(pct(defTimes, 50)), ms(pct(defTimes, 95)))
	fmt.Printf("| hover (%d, %d non-empty) | %s | %s |\n", len(hoverTimes), hoverHits, ms(pct(hoverTimes, 50)), ms(pct(hoverTimes, 95)))
	fmt.Printf("| RSS after first diagnostics / at end | — | %d / %d MB |\n", rssCold, rss)

	if *jsonOut != "" {
		f, err := os.OpenFile(*jsonOut, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		json.NewEncoder(f).Encode(map[string]any{
			"name": *name, "init_ms": initTime.Milliseconds(), "cold_ms": cold.Milliseconds(),
			"edit_p50_ms": pct(editTimes, 50).Milliseconds(), "edit_p95_ms": pct(editTimes, 95).Milliseconds(),
			"revert_ms":   revert.Milliseconds(),
			"comp_p50_ms": pct(compTimes, 50).Milliseconds(), "comp_p95_ms": pct(compTimes, 95).Milliseconds(), "comp_items_avg": compItems / max(len(compTimes), 1),
			"def_p50_ms": pct(defTimes, 50).Milliseconds(), "def_p95_ms": pct(defTimes, 95).Milliseconds(), "def_resolved": defHits, "sites": len(defTimes),
			"hover_p50_ms": pct(hoverTimes, 50).Milliseconds(), "hover_p95_ms": pct(hoverTimes, 95).Milliseconds(), "hover_nonempty": hoverHits,
			"rss_cold_mb": rssCold, "rss_mb": rss, "other_errors": otherErrs, "warnings": warns, "label": os.Getenv("COMPARE_LABEL"),
		})
	}
	return nil
}

func countItems(raw json.RawMessage) int {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return len(list)
	}
	var cl struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(raw, &cl) == nil {
		return len(cl.Items)
	}
	return 0
}

func countLocs(raw json.RawMessage) int {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return len(list)
	}
	var one map[string]any
	if json.Unmarshal(raw, &one) == nil && one != nil {
		return 1
	}
	return 0
}

// bodyStart returns the offset after the { of the first function with a
// block body.
func bodyStart(path, text string) int {
	s := psi.Parse(path, text)
	at := -1
	psi.WalkNodes(s.RootNode(), func(n psi.NodeRef) bool {
		if at >= 0 {
			return false
		}
		if n.NumericKind() == psi.KindFUN {
			if b := n.ChildKind(psi.KindBLOCK); b.Valid() {
				at = b.Range().Start + 1
				return false
			}
		}
		return true
	})
	return at
}

// memberAccesses returns the offsets just after the dots of up to n
// member accesses.
func memberAccesses(path, text string, n int) []int {
	s := psi.Parse(path, text)
	var out []int
	psi.WalkNodes(s.RootNode(), func(x psi.NodeRef) bool {
		if len(out) >= n {
			return false
		}
		if x.NumericKind() == psi.KindDOT_QUALIFIED_EXPRESSION {
			if dot := x.ChildKind(psi.KindDOT); dot.Valid() {
				out = append(out, dot.Range().End)
			}
		}
		return true
	})
	return out
}

func largest(dir string) string {
	best, size := "", int64(-1)
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".kt") && fi.Size() > size {
			best, size = p, fi.Size()
		}
		return nil
	})
	return best
}

func countKt(dir string) (int, error) {
	n := 0
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".kt") {
			n++
		}
		return nil
	})
	return n, err
}

// children returns the pids of a process's descendants, and its own.
func tree(pid int) []int {
	out := []int{pid}
	entries, _ := os.ReadDir("/proc")
	parent := map[int]int{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		f := strings.Fields(s[i+1:])
		if len(f) > 1 {
			pp, _ := strconv.Atoi(f[1])
			parent[p] = pp
		}
	}
	for changed := true; changed; {
		changed = false
		for p, pp := range parent {
			if slices.Contains(out, pp) && !slices.Contains(out, p) {
				out = append(out, p)
				changed = true
			}
		}
	}
	return out
}

func rssTreeMB(pid int) int {
	kb := 0
	for _, p := range tree(pid) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "VmRSS:") {
				f := strings.Fields(l)
				n, _ := strconv.Atoi(f[1])
				kb += n
			}
		}
	}
	return kb / 1024
}

func killTree(pid int) {
	for _, p := range tree(pid) {
		if proc, err := os.FindProcess(p); err == nil {
			proc.Kill()
		}
	}
}

func pct(ds []time.Duration, p int) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[min(max(i-1, 0), len(s)-1)]
}

func ms(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}
