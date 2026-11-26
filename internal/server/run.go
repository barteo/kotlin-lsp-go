package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/features"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/jsonrpc"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/project"
	"github.com/barteo/kotlin-compiler-go/internal/lsp/protocol"
)

// codeLens answers textDocument/codeLens: the run lenses, from the psi
// tree alone.
func (s *Server) codeLens(req *jsonrpc.Request) (any, error) {
	var p protocol.CodeLensParams
	if err := decode(req, &p); err != nil {
		return nil, err
	}
	f, err := s.file(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return features.CodeLenses(f), nil
}

// prepareRun answers the kcls.prepareRun command: how to run the target
// of a run lens.
func (s *Server) prepareRun(ctx context.Context, args []json.RawMessage) (any, error) {
	if len(args) != 1 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%s takes one argument", features.PrepareRunCommand)
	}
	var a features.RunArgs
	if err := json.Unmarshal(args[0], &a); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
	}
	path, err := uriPath(a.URI)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
	}
	ws, m, deps, err := s.eng.RunModule(ctx, path)
	if err != nil {
		return nil, err
	}
	key := m.ID
	if ws != nil {
		key = ws.Root + "\x00" + key
	}
	sum := sha256.Sum256([]byte(key))
	cache := s.opts.CacheDir
	if cache == "" {
		cache = project.CacheDir()
	}
	plan, err := features.PlanRun(a, features.RunEnv{
		Path:      path,
		Workspace: ws,
		Module:    m,
		Deps:      deps,
		KC:        s.kc(),
		Dir:       filepath.Join(cache, "run", hex.EncodeToString(sum[:8])),
	})
	if err != nil {
		return nil, &jsonrpc.Error{Code: jsonrpc.CodeRequestFailed, Message: err.Error()}
	}
	s.logf("run %s: %s", plan.Name, plan.Command)
	return plan, nil
}

// kc returns the kc binary runs compile with: Options.KC, else this
// binary if it is kc (kc -lsp), else kc next to it, else kc on PATH.
func (s *Server) kc() string {
	if s.opts.KC != "" {
		return s.opts.KC
	}
	if exe, err := os.Executable(); err == nil {
		if strings.TrimSuffix(filepath.Base(exe), ".exe") == "kc" {
			return exe
		}
		if p := filepath.Join(filepath.Dir(exe), "kc"); isFile(p) {
			return p
		}
	}
	if p, err := exec.LookPath("kc"); err == nil {
		return p
	}
	return ""
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
