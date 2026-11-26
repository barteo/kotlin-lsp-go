# kcls for Claude Code

This plugin gives Claude Code's LSP tool Kotlin code intelligence from
`kcls`, the Kotlin language server built on kc. Claude Code starts it
for `.kt` and `.kts` files and reads the errors it reports after each
edit.

## Install

Put `kcls` on `PATH`; the plugin runs it by name:

```sh
go install github.com/barteo/kotlin-compiler-go/cmd/kcls@latest
```

Then, in Claude Code, add this repository's marketplace and install the
plugin:

```
/plugin marketplace add barteo/kotlin-compiler-go
/plugin install kcls@kotlin-compiler-go
```

From a clone, `/plugin marketplace add ./` in its root does the same, and
`claude --plugin-dir ./editors/claude-code` loads the plugin for one
session.

## What it answers

At milestone M6 (see [docs/lsp.md](../../docs/lsp.md)), kcls reports
type errors and warnings, and answers every request of the LSP tool:
hover, go to definition (into library sources too), find references,
document and workspace symbols, go to implementation, and incoming and
outgoing calls. kcls reads files Claude Code never opened from disk, and
reads them again when they change, so the answers follow the edits an
agent makes. It imports a Gradle build in the background the first time
and caches the model.

If Claude Code can't start the server, `/plugin` shows
`Executable not found in $PATH: "kcls"`; `claude --debug` logs the
server's start and its stderr.
