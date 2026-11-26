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

At milestone M0 (see [docs/lsp.md](../../docs/lsp.md)), kcls answers
document symbols and reports syntax errors. Definition, references,
hover, workspace symbols, implementations and call hierarchy come with
the next milestones. kcls reads files Claude Code never opened from disk,
and reads them again when they change, so the answers follow the edits
an agent makes.

If Claude Code can't start the server, `/plugin` shows
`Executable not found in $PATH: "kcls"`; `claude --debug` logs the
server's start and its stderr.
