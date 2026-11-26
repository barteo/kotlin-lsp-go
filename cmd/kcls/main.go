// Command kcls is the Kotlin language server built on kc.
//
//	kcls [-stdio] [-listen addr] [-log file] [-version]
//
// It speaks the Language Server Protocol on stdin and stdout, or on a TCP
// address with -listen. kc -lsp runs the same server. See docs/lsp.md.
package main

import (
	"os"
	"runtime/debug"

	"github.com/barteo/kotlin-compiler-go/internal/lsp/server"
)

// version is the release this kcls was built as; the release workflow
// sets it with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	os.Exit(server.Main("kcls", os.Args[1:], buildVersion(), os.Stdin, os.Stdout, os.Stderr))
}

// buildVersion returns version, with the commit for a build that isn't a
// release's.
func buildVersion() string {
	v := version
	if v != "dev" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 12 {
				v += " (" + s.Value[:12] + ")"
			}
		}
	}
	return v
}
