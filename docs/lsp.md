# kcls: a Kotlin language server on kc

`kcls` is a Language Server Protocol server for Kotlin, built on kc's
[`psi`](psi.md) parser and its checker. The goal is to match JetBrains'
[kotlin-lsp](https://github.com/Kotlin/kotlin-lsp) feature for feature,
for VS Code and for coding agents such as Claude Code. kotlin-lsp is
IntelliJ running headless, needs JDK 25 and takes seconds to start; kcls
is one Go binary that starts in milliseconds.

It is early work: milestone M1 of seven. This page tracks what it does
and what comes next.

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
| Highlighting from syntax, then from resolution | `semanticTokens/full`, `/range` | done |
| Type errors and warnings, with kotlinc's names | diagnostics | done |
| Hover with signature and KDoc | `hover` | done |
| Go to definition and type definition, into library sources | `definition`, `typeDefinition` | done |
| Occurrences of a symbol in a file | `documentHighlight` | done |
| Gradle and `kcls.json` projects; reimport, clear caches | `executeCommand` | done |
| Find references, implementations, rename | `references`, `implementation`, `prepareRename`, `rename` | done |
| Call and type hierarchy | `callHierarchy/*`, `typeHierarchy/*` | done |
| Workspace symbols, with library classes | `workspace/symbol` | done |
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
  client asks for them (`textDocument/diagnostic`), never both. They are
  the syntax errors and the checker's errors and warnings, with
  kotlinc's diagnostic names as codes where kc has them. A construct kc
  doesn't support yet is reported as information, not an error: kotlinc
  compiles it. An edit, or a change on disk, analyzes the file's module
  again; an analysis an edit made out of date is cancelled.
- **Semantic requests** (hover, definition, type definition,
  highlights) analyze the file's module, or reuse the analysis of the
  same files. They work in files with syntax errors, for the code around
  the errors.
- **References and rename** look in the modules that can see the
  declaration: its own and those that depend on it. Only the files that
  contain its name as a word are read, and their modules analyzed. A
  reference counts if it resolves to the same declaration, so a
  same-named declaration in another module or class isn't one. A class's
  references include its constructor calls, type references and
  imports; a parameter's, its named arguments. Renaming a member renames
  the members it overrides and those that override it, and a new name
  that is a keyword is quoted in backticks. Library declarations can't
  be renamed.
- **Implementations** of a class are its subclasses in the sources,
  direct or not; of a member, the members that override it.
  **Hierarchies**: incoming calls group the calls by the function,
  constructor, property or class whose code makes them; outgoing calls
  include operator calls (`a[i]`, `-x`, `x++`) and calls into libraries
  that have sources. Members of Kotlin's built-in types, as `Int.plus` or
  `Map.get`, have none, so calls of them are left out. A type's subtypes are
  the classes that list it as a supertype.
- **Workspace symbols** match the query's letters in order, best first:
  the exact name, a prefix, camel humps (`SB` or `StrBu` for
  `StringBuilder`), a substring. They are the declarations of every
  module's Kotlin files, and, for a query of two letters or more, the
  classes of the class path and the JDK that have sources, at most 200.
- **Semantic tokens** classify names by what they resolve to (a method,
  a read-only property, a parameter, an interface, a type parameter),
  with `deprecated` where kotlinc would warn and `defaultLibrary` for
  Kotlin's and Java's own declarations. They wait up to 2 s for the
  analysis, else they come from the syntax and the client is asked to
  refresh them when the analysis is done.
- A request that fails, even with a panic, is answered with an error, and
  the server keeps serving.

## Projects

kcls finds a workspace folder's modules in the first of:

1. **`kcls.json`** at the folder's root, which lists the modules by hand:

   ```json
   {
     "modules": [
       {"name": "lib", "roots": ["lib/src"], "classPath": ["libs/okio.jar"], "jvmTarget": "17"},
       {"name": "app", "roots": ["app/src"], "dependsOn": ["lib"], "plugins": ["serialization"]}
     ],
     "sourceJars": {"libs/okio.jar": "libs/okio-sources.jar"}
   }
   ```

   Paths are relative to the file. A module also takes `files`,
   `moduleName`, `friendPaths`, `jdkHome`, `noJdk`, `explicitApi`
   (`warning` or `strict`), `optIns`, and `stdlib: false` to leave out
   the Kotlin standard library kc adds by default.
2. **A Gradle build** (`settings.gradle(.kts)` or `build.gradle(.kts)`).
   kcls runs the build's `gradlew`, or `gradle` on `PATH`, with the init
   script [kcls-model.init.gradle.kts](../internal/lsp/project/kcls-model.init.gradle.kts).
   The script compiles nothing. For each Kotlin/JVM compile task it
   writes the sources, class path, JDK, JVM target, explicit API mode,
   opt-ins and compiler plugins, and it resolves the libraries' sources
   jars. Each source set is a module: `:app:main`, `:app:test`. An
   Android module keeps its debug variants. A module that depends on
   another's classes depends on its sources instead.

   The import runs in the background, with progress in the editor. The
   model is cached, keyed by a hash of the build files, so the next start
   uses it at once. Editing a build file, `gradle.properties` or
   `libs.versions.toml` imports again.
3. **Otherwise**, every `.kt` file under the folder is one module,
   compiled against the JDK and the standard library kc finds.

A file outside every module is analyzed on its own. On macOS, when
`JAVA_HOME` isn't set, as for an editor started from the Dock, the JDK is
the one `/usr/libexec/java_home` names.

**Library navigation.** Go to definition on a library declaration opens
its source. kcls finds it in the module's sources jars, the standard
library's, and the JDK's `src.zip`, and extracts that one file into its
cache. The source opens read-only and isn't analyzed.

**Commands** (`workspace/executeCommand`, and in VS Code's command
palette):

- `kcls.reimport` (*Kotlin: Reimport Project*) reads the project model
  again.
- `kcls.clearCaches` (*Kotlin: Clear Caches and Reimport*) also forgets
  the open class paths, the cached model and the extracted sources.

kcls's cache is in the user cache directory (`~/Library/Caches/kcls`,
`~/.cache/kcls`), or in `$KCLS_CACHE`.

## Design

```
editor ──stdio/TCP──▶ cmd/kcls (or kc -lsp)
  internal/lsp/jsonrpc   framing, dispatch, cancellation
  internal/lsp/protocol  LSP 3.17 types, byte offset ↔ position Mapper
  internal/lsp/server    lifecycle, documents, dispatch
  internal/lsp/project   workspace model: kcls.json, Gradle, folder
  internal/lsp/engine    module analyses, cancellation, library sources
  internal/lsp/features  one file per feature, over a psi snapshot and
                         a module's analysis
```

The server has no dependencies outside the standard library, like the
rest of kc. The protocol types are written by hand for the methods kcls
serves.

Semantic features analyze a module with `analysis.Library`, which keeps
the module's class path open between analyses. It checks a file with
syntax errors as far as lowering recovers it. It reports the checker's
warnings, and a panic in the checker is an error, not a crash. A module
is analyzed together with the sources of the modules it depends on, so
that their declarations resolve to source whether or not the build
compiled them. Only the module's own files' diagnostics are reported.

Still to come from kc:

- a session that checks only the edited files' bodies, for large
  modules (each edit checks the whole module now);
- end positions and kotlinc names for the checker's errors, as warnings
  have; kcls widens an error to the expression that starts where it does;
- the scope at a position, for completion;
- override links and declaration flags (open, val or var, deprecated)
  in the facts: kcls reads overrides and flags from the syntax, and
  library members' flags it doesn't know;
- library top-level functions and properties for workspace symbols.

## Tests

- `go test ./internal/lsp/...` runs the unit tests and the end-to-end
  tests. The latter drive a server through a scripted client over pipes:
  the lifecycle, editing, pushed and pulled diagnostics, and files read
  from disk.
- `internal/lsp/features/testdata/*.golden` records each feature's output
  for the `.kt` files next to it. `testdata/semantic/<dir>.golden`
  records, for each module directory, the diagnostics, and the hover,
  definitions and highlights at each `/*|*/` caret, and the semantic
  tokens of the names. `testdata/workspace/<dir>.golden` records the
  references, implementations, rename and hierarchies at each caret.
  Check a change, then
  accept it with `go test ./internal/lsp/features -update`.
- `KCLS_GRADLE_TEST=1 go test ./internal/lsp/project -run Gradle` imports
  a two-project Gradle build. It needs Gradle (`KCLS_GRADLE` names the
  binary) and the Kotlin Gradle plugin, cached or online.
- `cd editors/vscode && npm test` compiles the extension and tokenizes a
  sample with its TextMate grammar through Oniguruma, as VS Code does.
