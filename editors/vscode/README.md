# Kotlin (kcls) for VS Code

Kotlin language support from `kcls`, the Kotlin language server built on
[kc](https://github.com/barteo/kotlin-compiler-go). It starts in
milliseconds and needs no JVM.

What it does (see [docs/lsp.md](../../docs/lsp.md)):

- Type errors and warnings as you type, from kc's checker
- Code completion: locals, members, extensions, auto-import, named
  arguments, overrides, keywords and live templates (`main`, `ifn`,
  `iter`, `sout`, …); signature help in a call's arguments
- Quick fixes: import, add missing `when` branches, implement members,
  remove unused or redundant code, suppress a warning; specify a type,
  add argument names; organize imports
- Inlay hints for inferred types, parameter names and a lambda's `it`
  and `this`
- Formatting of files, selections and as you type, by the Kotlin coding
  conventions and `.editorconfig`; extract and inline variable; moving
  files and folders updates packages and imports
- ▶ Run above `main` functions, and ▶ Run test(s) above JUnit, TestNG
  and kotlin.test tests, in a terminal: tests with the Gradle or Maven
  build's test task, `main` compiled with kc and run with `java`
- Hover with the signature and KDoc
- Go to definition and type definition, into library sources too
- Highlights of a symbol's occurrences
- Find references, rename (overrides included), go to implementation
- Call hierarchy and type hierarchy
- Go to symbol in workspace, over the sources and library classes
- Gradle and Maven projects, imported in the background, or a
  `kcls.json`; multiplatform modules' JVM sources; multi-root workspaces,
  linked where one folder's build uses another's
- Outline and breadcrumbs, with each function's signature
- Folding, expand and shrink selection
- Semantic highlighting from resolution (methods, read-only properties,
  parameters, deprecated uses), and a TextMate grammar
- Bracket, quote and KDoc comment completion

## Install

From the VS Code Marketplace or Open VSX (*Kotlin (kcls)*), or from a
[release](https://github.com/barteo/kotlin-lsp-go/releases): each
has the extension for Linux and macOS, amd64 and arm64, with `kcls` and
`kc` in it.

```sh
code --install-extension kcls-darwin-arm64-<release>.vsix
```

## Install from source

```sh
cd editors/vscode
npm install
mkdir -p server
go build -o server/kcls ../../cmd/kcls
go build -o server/kc github.com/barteo/kotlin-compiler-go/cmd/kc
npx vsce package
code --install-extension kcls-*.vsix
```

`server/kcls` and `server/kc` are bundled in the package. Without them,
the extension runs the `kcls` on `PATH`, or the binary `kcls.path`
names. Running `main` compiles with the `kc` next to the server, else
the one on `PATH`.

## Settings

| Setting | Meaning |
|---|---|
| `kcls.path` | the server binary (default: the bundled one, else `kcls` on `PATH`) |
| `kcls.args` | extra server arguments |
| `kcls.trace.server` | `messages` or `verbose` traces the protocol in the output channel |
| `kcls.hints.types` | inlay hints for inferred types (default `true`) |
| `kcls.hints.parameterNames` | parameter name hints: `literals` (default), `all` or `none` |
| `kcls.hints.lambdas` | inlay hints for a lambda's `it` and `this` (default `true`) |

`"editor.codeActionsOnSave": {"source.organizeImports": "explicit"}`
organizes the imports on save, `"editor.formatOnSave": true` formats,
and `"editor.formatOnType": true` formats as you type. The extension
makes itself the default formatter for Kotlin.

## Commands

- **Kotlin: Restart Language Server**
- **Kotlin: Show Language Server Log**
- **Kotlin: Reimport Project**: read the Gradle build or `kcls.json` again
- **Kotlin: Clear Caches and Reimport**

Disable JetBrains' Kotlin extension (`jetbrains.kotlin-server`) or any
other Kotlin language server in the same workspace: two servers answer
every request twice.
