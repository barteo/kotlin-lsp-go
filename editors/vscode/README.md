# Kotlin (kcls) for VS Code

Kotlin language support from `kcls`, the Kotlin language server built on
[kc](https://github.com/barteo/kotlin-compiler-go). It starts in
milliseconds and needs no JVM.

What it does so far (milestone M0 of [docs/lsp.md](../../docs/lsp.md)):

- Syntax errors as you type
- Outline and breadcrumbs, with each function's signature
- Folding, expand and shrink selection
- Semantic highlighting from the syntax tree, and a TextMate grammar
- Bracket, quote and KDoc comment completion

Type checking, navigation, completion and refactorings come in the next
milestones.

## Install from source

```sh
cd editors/vscode
npm install
go build -o server/kcls ../../cmd/kcls
npx vsce package
code --install-extension kcls-*.vsix
```

`server/kcls` is bundled in the package. Without it, the extension runs
the `kcls` on `PATH`, or the binary `kcls.path` names.

## Settings

| Setting | Meaning |
|---|---|
| `kcls.path` | the server binary (default: the bundled one, else `kcls` on `PATH`) |
| `kcls.args` | extra server arguments; `["-lsp"]` with `kcls.path` set to `kc` runs kc's built-in server |
| `kcls.trace.server` | `messages` or `verbose` traces the protocol in the output channel |

## Commands

- **Kotlin: Restart Language Server**
- **Kotlin: Show Language Server Log**

Disable JetBrains' Kotlin extension (`jetbrains.kotlin-server`) or any
other Kotlin language server in the same workspace: two servers answer
every request twice.
