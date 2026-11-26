// Command lsp measures kcls as an editor uses it, against performance
// budgets:
//
//	go run ./bench/lsp [-kcls bin] [-root dir | -gen n] [-check] [-soak duration]
//
// It starts kcls on a workspace, by default a module of 200 files that
// bench/gen writes, and drives it over stdio as an editor does: it opens
// a file and waits for its first diagnostics (cold start), edits a
// function body again and again and waits for each edit's diagnostics,
// asks for completion after each member access's dot and for each
// member's definition, and reads the server's resident memory. With
// -soak it repeats edits and completions for that long and reports how
// the memory grew. With -check it exits 1 when a measurement is over its
// budget (the -max flags).
//
// The budgets guard against regressions: they are what kcls does now on
// a laptop (bench/results/lsp.md), with room. They are not the goals: a
// body edit's diagnostics in under 100 ms takes a session that checks
// only the edited bodies, and each edit checks the whole module now.
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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
	"github.com/barteo/kotlin-compiler-go/psi"
)

var (
	kclsBin  = flag.String("kcls", "", "the kcls binary (default: built from this repository)")
	root     = flag.String("root", "", "the workspace to measure (default: a module bench/gen writes)")
	file     = flag.String("file", "", "the file to open, relative to the workspace (default: its largest .kt file)")
	gen      = flag.Int("gen", 200, "the number of files of the generated module")
	edits    = flag.Int("edits", 20, "body edits to measure")
	sites    = flag.Int("sites", 30, "member accesses to complete and go to")
	soak     = flag.Duration("soak", 0, "repeat edits and completions for this long, sampling memory")
	check    = flag.Bool("check", false, "exit 1 if a measurement is over its budget")
	logFile  = flag.String("log", "", "write the server's stderr, its log, to this file")
	maxStart = flag.Duration("max-start", 3*time.Second, "budget: start to the opened file's first diagnostics")
	maxEdit  = flag.Duration("max-edit", 2*time.Second, "budget: p95 of a body edit to its diagnostics")
	maxComp  = flag.Duration("max-completion", time.Second, "budget: p95 of completion")
	maxDef   = flag.Duration("max-definition", 300*time.Millisecond, "budget: p95 of go to definition")
	maxRSS   = flag.Int("max-rss", 2560, "budget: resident memory after the measurements, in MB")
	maxGrow  = flag.Float64("max-growth", 0.25, "budget: memory growth over the soak, as a fraction of the memory after warming up")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench/lsp:", err)
		os.Exit(2)
	}
}

// client is an editor's side of the connection.
type client struct {
	conn *jsonrpc.Conn
	mu   sync.Mutex
	// diags gets the version of each publishDiagnostics of uri.
	uri   protocol.DocumentURI
	diags chan int32
}

func run() error {
	tmp, err := os.MkdirTemp("", "bench-lsp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := *kclsBin
	if bin == "" {
		bin = filepath.Join(tmp, "kcls")
		if out, err := exec.Command("go", "build", "-o", bin, "./cmd/kcls").CombinedOutput(); err != nil {
			return fmt.Errorf("building kcls: %v\n%s", err, out)
		}
	}
	dir := *root
	if dir == "" {
		dir = filepath.Join(tmp, "src")
		if out, err := exec.Command("go", "run", "./bench/gen", "-n", strconv.Itoa(*gen), "-o", dir).CombinedOutput(); err != nil {
			return fmt.Errorf("generating sources: %v\n%s", err, out)
		}
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	path := *file
	if path == "" {
		path = largest(dir)
		if path == "" {
			return fmt.Errorf("no .kt file in %s", dir)
		}
	} else {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(b)
	n, _ := countKt(dir)

	start := time.Now()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "KCLS_CACHE="+filepath.Join(tmp, "cache"))
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
	defer cmd.Process.Kill()
	uri := protocol.DocumentURI("file://" + path)
	c := &client{uri: uri, diags: make(chan int32, 64)}
	c.conn = jsonrpc.NewConn(stdout, stdin, c.handle, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.conn.Run(ctx)

	rootURI := protocol.DocumentURI("file://" + dir)
	var initRes json.RawMessage
	if err := c.conn.Call(ctx, "initialize", map[string]any{
		"processId": os.Getpid(), "rootUri": rootURI,
		"workspaceFolders": []map[string]any{{"uri": rootURI, "name": filepath.Base(dir)}},
		"capabilities":     map[string]any{},
	}, &initRes); err != nil {
		return err
	}
	c.conn.Notify("initialized", map[string]any{})
	c.conn.Notify("textDocument/didOpen", protocol.DidOpenTextDocumentParams{TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "kotlin", Version: 1, Text: text}})
	if _, err := c.wait(1, 5*time.Minute); err != nil {
		return fmt.Errorf("first diagnostics: %w", err)
	}
	cold := time.Since(start)

	// Body edits: a statement at the start of the first block body,
	// different each time.
	at := bodyStart(path, text)
	if at < 0 {
		return fmt.Errorf("no function with a block body in %s", path)
	}
	version := int32(1)
	change := func(t string) error {
		version++
		c.conn.Notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": t}},
		})
		_, err := c.wait(version, time.Minute)
		return err
	}
	var editTimes []time.Duration
	for i := 0; i < *edits; i++ {
		t0 := time.Now()
		if err := change(text[:at] + fmt.Sprintf(" val bench%d = %d;", i, i) + text[at:]); err != nil {
			return fmt.Errorf("edit %d: %w", i, err)
		}
		editTimes = append(editTimes, time.Since(t0))
	}
	if err := change(text); err != nil {
		return err
	}

	// Completion after each member access's dot, and definition of each
	// member.
	mapper := protocol.NewMapper(text, protocol.UTF16)
	var compTimes, defTimes []time.Duration
	accesses := memberAccesses(path, text, *sites)
	measure := func() error {
		for _, off := range accesses {
			pos := protocol.TextDocumentPositionParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}, Position: mapper.Position(off)}
			t0 := time.Now()
			var res json.RawMessage
			if err := c.conn.Call(ctx, "textDocument/completion", pos, &res); err != nil {
				return fmt.Errorf("completion: %w", err)
			}
			compTimes = append(compTimes, time.Since(t0))
			t0 = time.Now()
			if err := c.conn.Call(ctx, "textDocument/definition", pos, &res); err != nil {
				return fmt.Errorf("definition: %w", err)
			}
			defTimes = append(defTimes, time.Since(t0))
		}
		return nil
	}
	if err := measure(); err != nil {
		return err
	}
	rss := rssMB(cmd.Process.Pid)

	var soakLine string
	var growth float64
	if *soak > 0 {
		base, peak, last := rss, rss, rss
		end := time.Now().Add(*soak)
		next := time.Now().Add(30 * time.Second)
		rounds := 0
		for time.Now().Before(end) {
			for i := 0; i < 5; i++ {
				if err := change(text[:at] + fmt.Sprintf(" val soak%d = %d;", rounds, i) + text[at:]); err != nil {
					return err
				}
			}
			if err := change(text); err != nil {
				return err
			}
			compTimes, defTimes = compTimes[:0], defTimes[:0]
			if err := measure(); err != nil {
				return err
			}
			rounds++
			if time.Now().After(next) {
				last = rssMB(cmd.Process.Pid)
				peak = max(peak, last)
				next = time.Now().Add(30 * time.Second)
			}
		}
		last = rssMB(cmd.Process.Pid)
		peak = max(peak, last)
		growth = float64(last-base) / float64(max(base, 1))
		soakLine = fmt.Sprintf("| memory growth over a %v soak (%d rounds) | %d → %d MB, peak %d MB | %+.0f%%", *soak, rounds, base, last, peak, 100*growth)
	}

	c.conn.Call(ctx, "shutdown", nil, nil)
	c.conn.Notify("exit", nil)

	type row struct {
		name   string
		got    time.Duration
		budget time.Duration
	}
	rows := []row{
		{"start to first diagnostics", cold, *maxStart},
		{fmt.Sprintf("body edit to diagnostics, p95 of %d", len(editTimes)), pct(editTimes, 95), *maxEdit},
		{fmt.Sprintf("completion, p95 of %d", len(compTimes)), pct(compTimes, 95), *maxComp},
		{fmt.Sprintf("definition, p95 of %d", len(defTimes)), pct(defTimes, 95), *maxDef},
	}
	fmt.Printf("kcls on %s: %d Kotlin files, opened %s (%d lines); %s/%s, %d CPUs\n\n", dir, n, filepath.Base(path), strings.Count(text, "\n"), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	fmt.Println("| measurement | median | result | budget |")
	fmt.Println("|---|---|---|---|")
	over := false
	medians := map[string]time.Duration{rows[1].name: pct(editTimes, 50), rows[2].name: pct(compTimes, 50), rows[3].name: pct(defTimes, 50)}
	for _, r := range rows {
		mark := ""
		if r.got > r.budget {
			mark, over = " **over**", true
		}
		med := "—"
		if m, ok := medians[r.name]; ok {
			med = ms(m)
		}
		fmt.Printf("| %s | %s | %s%s | %s |\n", r.name, med, ms(r.got), mark, ms(r.budget))
	}
	mark := ""
	if rss > *maxRSS {
		mark, over = " **over**", true
	}
	fmt.Printf("| resident memory | — | %d MB%s | %d MB |\n", rss, mark, *maxRSS)
	if soakLine != "" {
		mark := ""
		if growth > *maxGrow {
			mark, over = " **over**", true
		}
		fmt.Printf("%s%s | %+.0f%% |\n", soakLine, mark, 100**maxGrow)
	}
	if *check && over {
		os.Exit(1)
	}
	return nil
}

func (c *client) handle(_ context.Context, _ *jsonrpc.Conn, req *jsonrpc.Request) (any, error) {
	if req.Method == "textDocument/publishDiagnostics" {
		var p protocol.PublishDiagnosticsParams
		if json.Unmarshal(req.Params, &p) == nil && p.URI == c.uri {
			v := int32(0)
			if p.Version != nil {
				v = *p.Version
			}
			c.diags <- v
		}
	}
	return nil, nil
}

// wait waits for the diagnostics of the document's version.
func (c *client) wait(version int32, timeout time.Duration) (int32, error) {
	deadline := time.After(timeout)
	for {
		select {
		case v := <-c.diags:
			if v >= version {
				return v, nil
			}
		case <-deadline:
			return 0, fmt.Errorf("no diagnostics of version %d in %v", version, timeout)
		}
	}
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

// rssMB returns a process's resident memory in MB, from ps.
func rssMB(pid int) int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return kb / 1024
}

// pct returns the pth percentile of ds.
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
