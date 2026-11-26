# kcls: a Kotlin language server on kc

`kcls` is a Language Server Protocol server for Kotlin, built on kc's
[`psi`](psi.md) parser and its checker. The goal is to match JetBrains'
[kotlin-lsp](https://github.com/Kotlin/kotlin-lsp) feature for feature,
for VS Code and for coding agents such as Claude Code. kotlin-lsp is
IntelliJ running headless, needs JDK 25 and takes seconds to start; kcls
is one Go binary that starts in milliseconds.

It is early work. This page tracks what it does and what comes next.

## Run it

```sh
go build -o bin/kcls ./cmd/kcls
bin/kcls            # LSP on stdin and stdout
bin/kcls -listen :9999   # LSP on TCP, as kotlin-lsp's default port
```

`kc -lsp` runs the same server, so a release's `kc` binary is also the
language server. Logs go to stderr, or to the file `-log` names.

- **VS Code:** [editors/vscode](../editors/vscode/README.md).
- **Claude Code:** [editors/claude-code](../editors/claude-code/README.md),
  installed from this repository's marketplace:
  `/plugin marketplace add barteo/kotlin-compiler-go`, then
  `/plugin install kcls@kotlin-compiler-go`.
- **Other editors:** run `kcls` as a stdio language server for the
  `kotlin` language and `.kt` and `.kts` files.

## What works

| Feature | LSP | Status |
|---|---|---|
| Syntax errors, as kotlinc reports them | `publishDiagnostics`, `textDocument/diagnostic` | done |
| Outline and breadcrumbs, with signatures | `documentSymbol` | done |
| Folding | `foldingRange` | done |
| Expand and shrink selection | `selectionRange` | done |
| Highlighting from syntax | `semanticTokens/full`, `/range` | done |
| Type errors and warnings | diagnostics | M1 |
| Hover, go to definition, type definition, highlights | `hover`, `definition`, `typeDefinition`, `documentHighlight` | M1 |
| Find references, implementations, rename | `references`, `implementation`, `rename` | M2 |
| Call and type hierarchy | `callHierarchy/*`, `typeHierarchy/*` | M2 |
| Workspace symbols, with libraries | `workspace/symbol` | M2 |
| Completion, auto-import, signature help | `completion`, `signatureHelp` | M3 |
| Quick fixes, organize imports, inlay hints | `codeAction`, `inlayHint` | M4 |
| Formatting, extract and inline variable, move | `formatting`, `codeAction`, `willRenameFiles` | M5 |
| Run `main` and tests | `codeLens` | M6 |
| Maven, multiplatform polish, release packaging | | M7 |

How the server behaves:

- **Positions** are UTF-8 when the client offers it, else UTF-16.
- **Documents** are synchronized incrementally. A request on a file the
  client never opened reads the file from disk, and again when its size
  or modification time changes. Coding agents edit files on disk without
  opening them, and the answers follow their edits. Where the client
  allows it, the server also registers to watch `**/*.{kt,kts}`.
- **Diagnostics** are pushed 50 ms after an edit, or pulled when the
  client asks for them (`textDocument/diagnostic`), never both.
- A request that fails, even with a panic, is answered with an error, and
  the server keeps serving.

## Design

```
editor ──stdio/TCP──▶ cmd/kcls (or kc -lsp)
  internal/lsp/jsonrpc   framing, dispatch, cancellation
  internal/lsp/protocol  LSP 3.17 types, byte offset ↔ position Mapper
  internal/lsp/server    lifecycle, documents, dispatch
  internal/lsp/features  one file per feature, over a psi snapshot
```

The server has no dependencies outside the standard library, like the
rest of kc. The protocol types are written by hand for the methods kcls
serves.

Semantic features (M1 on) analyze each module with kc's checker on the
psi tree. The checker already resolves what it can around errors. What
the server still needs from kc:

- facts from files with syntax errors;
- warnings, with kotlinc's diagnostic names and end positions;
- a session that checks only the edited files' bodies;
- the scope at a position, for completion.

The project model comes from a `kcls.json` file, or from a Gradle build
through an init script like [use-kc.init.gradle.kts](../tools/gradle/use-kc.init.gradle.kts),
or else from every `.kt` file under the workspace root as one module.

## Tests

- `go test ./internal/lsp/...` runs the unit tests and the end-to-end
  tests. The latter drive a server through a scripted client over pipes:
  the lifecycle, editing, pushed and pulled diagnostics, and files read
  from disk.
- `internal/lsp/features/testdata/*.golden` records each feature's output
  for the `.kt` files next to it. Check a change, then accept it with
  `go test ./internal/lsp/features -update`.
- `cd editors/vscode && npm test` compiles the extension and tokenizes a
  sample with its TextMate grammar through Oniguruma, as VS Code does.
