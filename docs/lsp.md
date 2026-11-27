# kcls: a Kotlin language server on kc

`kcls` is a Language Server Protocol server for Kotlin, built on kc's
[`psi`](https://github.com/barteo/kotlin-compiler-go/blob/main/docs/psi.md) parser and its checker. The goal is to match JetBrains'
[kotlin-lsp](https://github.com/Kotlin/kotlin-lsp) feature for feature,
for VS Code and for coding agents such as Claude Code. kotlin-lsp is
IntelliJ running headless, needs JDK 25 and takes seconds to start; kcls
is one Go binary that starts in milliseconds.

Its roadmap's milestones, M0 to M7, are done; it is still young. This
page tracks what it does, how fast, and what comes next.

## Run it

```sh
go build -o bin/kcls ./cmd/kcls
bin/kcls            # LSP on stdin and stdout
bin/kcls -listen :9999   # LSP on TCP, as kotlin-lsp's default port
```

Logs go to stderr, or to the file `-log` names. Running `main`
functions and tests outside Gradle and Maven compiles with
[kc](https://github.com/barteo/kotlin-compiler-go):

```sh
go install github.com/barteo/kotlin-compiler-go/cmd/kc@latest
```

Each [release](https://github.com/barteo/kotlin-lsp-go/releases) has,
for Linux and macOS on amd64 and arm64, an archive with `kcls` and the
`kc` that go.mod requires, and the VS Code extension with both in it
(`kcls-darwin-arm64-<release>.vsix`); a release that isn't a prerelease
also goes to the VS Code Marketplace and Open VSX.

- **VS Code:** [editors/vscode](../editors/vscode/README.md).
- **Claude Code:** [editors/claude-code](../editors/claude-code/README.md),
  installed from this repository's marketplace:
  `/plugin marketplace add barteo/kotlin-lsp-go`, then
  `/plugin install kcls@kotlin-lsp-go`.
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
| Completion, auto-import, overrides, live templates | `completion`, `completionItem/resolve` | done |
| Signature help | `signatureHelp` | done |
| Quick fixes, intentions, organize imports | `codeAction` | done |
| Inlay hints: types, parameter names, lambdas | `inlayHint` | done |
| Formatting: whole file, range, on type; `.editorconfig` | `formatting`, `rangeFormatting`, `onTypeFormatting` | done |
| Extract and inline variable | `codeAction` | done |
| Moving files and packages | `workspace/willRenameFiles` | done |
| Run `main` and tests | `codeLens`, `kcls.prepareRun` | done |
| Maven projects, multiplatform modules, multi-root workspaces | | done |
| Releases: binaries, VS Code extensions, Claude Code plugin | | done |

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
  private declaration, one in a private class, and a local are looked
  for in their own file only, and the overrides of a member are looked
  for only if it can be overridden: not in an object, a companion object
  or a final class. Of the modules a search analyzed that have no open
  document, only the two most recent keep their analysis. A
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
- **Completion** lists what the code could name at the caret, as the
  checker sees it there: locals with their smart-cast types, the
  members and extensions of the receivers, explicit (`a.`, `a?.`) or
  implicit (`this`, a lambda's), the top-level declarations in scope,
  and then those an import would bring in: the program's and the
  libraries' functions and properties, and classes, with the import as
  an additional edit. A function inserts its parentheses, or braces when
  it takes one lambda. In a call's arguments the parameters come first as
  named arguments (`name = `); in a type, only classes; after `@`, only
  annotation classes first. In a class body, a member's name (or
  `override fun`) lists the members of the supertypes the class could
  override, which insert a stub that calls `super` or `TODO()`. Keywords
  and IntelliJ's live templates (`main`, `fun0`, `ifn`, `inn`, `iter`,
  `sout`, `soutv`, …) complete too, the latter where the client takes
  snippets. Items list nearest first: named arguments, those of the
  expected type, locals, members, the rest in scope, keywords, imports.
  Libraries' documentation comes with `completionItem/resolve`.
  Completion checks the bodies of its own file only; the other files'
  omitted types are inferred as it needs them.
- **Signature help** lists the overloads the callee could mean, the
  receiver's members or those in scope, and a class's constructors, with
  the parameter of the argument at the caret, by its position or its
  name; the overload whose parameters fit the arguments' types is the
  active one.
- **Inlay hints** show the inferred type of a property, local variable,
  destructuring entry, `for` variable or expression-bodied function
  that doesn't declare it, unless the initializer shows it (a literal, a
  constructor call, a cast); the names of the parameters that literal
  arguments are passed to (`kcls.hints.parameterNames`: `literals`,
  `all` or `none`), not where the argument already is a variable of that
  name, nor after a named argument, nor for Java methods; and a lambda's
  implicit `it` and receiver `this`, with their types. The
  `kcls.hints.types`, `.parameterNames` and `.lambdas` settings come as
  `initializationOptions` (`{"hints": {...}}`) and with
  `workspace/didChangeConfiguration` (`{"kcls": {"hints": {...}}}`),
  after which the client is asked to refresh the hints.
- **Code actions**: quick fixes for the diagnostics the client sends,
  intentions at the caret, and organizing the imports:
  - an unresolved name: import each class or function of that name the
    completion would import;
  - a `when` that isn't exhaustive: add its missing branches (enum
    entries, sealed subclasses and objects, `true`, `false`, `null`) or
    an `else` branch, each `-> TODO()`;
  - a class or object that doesn't implement abstract members:
    implement them with `TODO()` stubs;
  - warnings: remove an unused variable (keeping an initializer that
    calls something), rename an unused lambda parameter to `_`, remove a
    redundant modifier, a useless cast, an unnecessary `?.` or `!!`, an
    unused import; and suppress any named warning with `@Suppress` on
    the declarations around it or `@file:Suppress`;
  - intentions (`refactor.rewrite`): specify an inferred type
    explicitly, importing its classes, and add names to a call's
    positional arguments;
  - `source.organizeImports` removes unused and duplicate imports and
    sorts the rest as IntelliJ does: others, `java`, `javax`, `kotlin`,
    then aliases. An import counts as used if a name or a KDoc link in
    the file names it, or it is an operator function (`getValue`,
    `plus`, `component1`, …). Unused imports are also reported as hints
    tagged unnecessary.

  Warnings include kotlinc's extra checkers, as IntelliJ shows them:
  unused variables and parameters, redundant modifiers, unreachable
  code.
- **Refactorings** at the caret or selection: `refactor.extract`
  declares the selected expression, or the call or chain at the caret,
  as a `val` before its statement, named after what it calls or its type,
  and uses it there. It isn't offered where that would change when the
  expression is evaluated: in a branch, a loop's condition or body, a
  lambda, or on the right of `&&`, `||` and `?:`. `refactor.inline`
  replaces a local `val`'s references with its initializer, in
  parentheses where precedence needs them, and removes it; not for a
  `var`, nor for an initializer that calls something and is read more
  than once.
- **Formatting** follows the Kotlin coding conventions as IntelliJ
  applies them, and changes whitespace only. It keeps the line breaks
  the code has, as IntelliJ does by default, and never joins or wraps
  lines: it indents each line (blocks, brackets, and continuations such
  as `a +`, `val x =`, `.call()` and `?:` on the next line, by 4), spaces
  each line's tokens (around operators and `->`, after commas, keywords
  and the colons of types, inside a one-line lambda's braces), keeps at
  most 2 blank lines and none before `}`, and ends the file with one
  line break. Strings and comments stay as they are, but KDoc lines are
  aligned. On the formatter's own test corpus, the stdlib's sources,
  it changes 0.3% of the lines, mostly old-style continuation indents and
  `while(` without a space. `.editorconfig` files set `indent_style`,
  `indent_size`, `tab_width`, `insert_final_newline` and IntelliJ's
  `ij_kotlin_keep_blank_lines_in_code` and
  `ij_kotlin_keep_blank_lines_before_right_brace`; without them the
  client's tab size and spaces apply. `max_line_length` is ignored, as
  nothing is wrapped. Range formatting formats the lines the range
  touches. On-type formatting formats a line ended by `}` or `;`, and on
  a line break the line ended and the indentation of the new one (VS
  Code's `editor.formatOnType`). A file with syntax errors is indented
  only.
- **Moving files**: before the client renames or moves `.kt` files or
  directories, `workspace/willRenameFiles` returns the edits that keep
  the code compiling. A moved file's package directive follows its
  directory, judged by its old package's directories (`src/a/b` for
  `a.b`); a file whose directory didn't match its package moves as it
  is. The imports and package-qualified references of its top-level
  declarations follow it in the modules that can see it; the files of
  its old package that used them get imports, and so does the moved file
  for the old package's declarations it used.
- **Running** `main` functions and tests from code lenses: ▶ Run above
  a top-level `main` (no parameters, or `Array<String>`) and a
  `@JvmStatic main` in an object or companion object, ▶ Run tests above
  a class with tests, and ▶ Run test above each test function. A test
  function is one annotated `@Test` (JUnit 4 or 5, TestNG, kotlin.test),
  `@ParameterizedTest`, `@RepeatedTest`, `@TestFactory` or
  `@TestTemplate`, as the file's imports resolve it, in a class that is
  not abstract. The lenses come from the syntax, so they show at once.
  The lens's command, `kcls.run`, is the client's: it asks the server
  how to run the target with `kcls.prepareRun`, which answers a shell
  command and its directory, and runs that in a terminal (VS Code saves
  the open files first).
  - Tests of a Gradle build run with the module's test task, filtered:
    `./gradlew :app:cleanTest :app:test --tests 'a.FooTest.bar'`; the
    clean task makes Gradle run them again when nothing changed. Tests
    of a Maven build run with Surefire, in the module and the modules it
    depends on: `mvn -pl app -am test -Dtest='a.FooTest#bar'`.
  - A `main` function, and tests outside Gradle, compile with kc into
    kcls's cache: the module's Kotlin sources together with those of the
    modules it depends on, its Java sources with the JDK's `javac`,
    against the module's class path, with its compiler plugins. Then
    `java` runs the main class with the modules' `resources` directories
    on the class path, or the tests with a launcher on the module's class
    path: the JUnit Platform's console launcher
    (`junit-platform-console-standalone`), JUnit 4's `JUnitCore` (a
    whole class) or TestNG. Argument files keep the class path off the
    command line. kc is the `kc` next to `kcls`, else the one on
    `PATH`.
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
   (`warning` or `strict`), `optIns`, `multiPlatform`, `output` (where
   its classes are built, which another workspace folder's class path
   may have), and `stdlib: false` to leave out the Kotlin standard
   library kc adds by default.
2. **A Gradle build** (`settings.gradle(.kts)` or `build.gradle(.kts)`).
   kcls runs the build's `gradlew`, or `gradle` on `PATH`, with the init
   script [kcls-model.init.gradle.kts](../internal/project/kcls-model.init.gradle.kts).
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

   A multiplatform module's JVM compilation (`compileKotlinJvm`) is a
   module of its common and JVM sources, compiled as kotlinc's
   `-Xmulti-platform` does. Kotlin/JS, Native and Wasm compilations
   aren't modules: a file of a source set only they compile, such as
   `jsMain` or `iosMain`, is analyzed on its own, against the JDK and
   the standard library, and an information diagnostic,
   `NOT_COMPILED_FOR_JVM`, says so.
3. **A Maven build** (`pom.xml`). kcls runs the build's `mvnw`, or `mvn`
   on `PATH`, twice over, compiling nothing: `help:effective-pom` gives
   each module's source and output directories (the
   `kotlin-maven-plugin`'s `sourceDirs`, `src/main/kotlin`, the source
   directory, and `build-helper-maven-plugin`'s sources), its Kotlin
   settings (`jvmTarget`, `-Xexplicit-api`, `-opt-in`, compiler plugins)
   and its dependencies on the reactor's other modules, and
   `dependency:build-classpath` its class paths, compile scope for main
   and test scope for test. A module of the reactor uses another's
   sources, not its jar. A module whose dependencies Maven can't resolve,
   as one on a module of the reactor that was never installed, gets its
   direct dependencies from the local repository. The model is cached
   and imported again as Gradle's is.
4. **Otherwise**, every `.kt` file under the folder is one module,
   compiled against the JDK and the standard library kc finds.

**Multi-root workspaces.** Each workspace folder has its own model. A
module whose class path has the classes of another folder's module (its
output directory, or a jar in its `build/libs` or `target`), as with a
Gradle composite build or two projects opened side by side, is analyzed
with that module's sources: definitions go to them, and references and
renames reach across folders. Adding or removing a folder, or importing
one again, relinks them.

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
- `kcls.prepareRun` takes a run lens's argument (`uri`, `kind` of `main`,
  `test` or `tests`, `class`, `method`) and returns how to run it:
  `{"name", "cwd", "command"}`, `command` a POSIX shell command.

kcls's cache is in the user cache directory (`~/Library/Caches/kcls`,
`~/.cache/kcls`), or in `$KCLS_CACHE`.

## Performance

[bench/lsp](../bench/lsp/main.go) drives a `kcls` process over stdio as
an editor does, on a module of 200 files [bench/gen](https://github.com/barteo/kotlin-compiler-go/blob/main/bench/gen/main.go)
writes, or on any workspace (`-root`): the start to the first diagnostics
of the opened file, a body edit to its diagnostics, completion after
each member access's dot, go to definition, and the server's resident
memory, over a soak with `-soak`. `-check` fails a run that is over its
budgets.

```sh
go run ./bench/lsp -soak 10m -check
```

On the generated module (201 Kotlin files, File100.kt opened, 403 lines), linux/amd64, 4 CPUs:

| measurement | median | result | budget |
|---|---|---|---|
| start to first diagnostics | — | 3.46 s | 3.00 s |
| body edit to diagnostics, p95 of 20 | 353 ms | 389 ms | 600 ms |
| completion, p95 of 30 | 234 ms | 298 ms | 600 ms |
| definition, p95 of 30 | 1 ms | 17 ms | 300 ms |
| resident memory | — | 628 MB | 2048 MB |
| memory growth over a 10m0s soak (78 rounds) | 628 → 672 MB, peak 715 MB | +7% | +25% |

The budgets catch regressions, at what kcls does now with room to spare
(the start's is an 8-CPU laptop's, where it takes 1.3 s). A body edit
checks only its file (an incremental update, see Design): its
diagnostics took 1.1 s on the laptop when each edit checked the whole
module, and take 0.35 s here. What is left of an update is mostly
lowering and resolving the other files' signatures again, which a
session keeping them between updates would save. Completion checks its
file against the other files' signatures. The memory is the Go heap
with the garbage collector's room, with the session's last result and
the one replacing it alive: about 300 MB live, of which kc's facts are a
third since kc stores them compactly (it was 1110 MB, 550 MB live,
before). Features that look at every name of a file (occurrences,
semantic tokens) read its facts in one pass without copying them.
[bench/results/lsp.md](../bench/results/lsp.md) has the numbers from
before compact facts and before incremental updates.

## Design

```
editor ──stdio/TCP──▶ cmd/kcls
  internal/jsonrpc       framing, dispatch, cancellation
  internal/protocol      LSP 3.17 types, byte offset ↔ position Mapper
  internal/server        lifecycle, documents, dispatch
  internal/project       workspace model: kcls.json, Gradle, folder
  internal/engine        module analyses, cancellation, library sources
  internal/features      one file per feature, over a psi snapshot and
                         a module's analysis
```

The server depends on nothing but kc's module (its `psi` and `analysis`
packages) and the standard library. The protocol types are written by hand for the methods kcls
serves.

Semantic features analyze a module with an `analysis.Session` of an
`analysis.Library`, which keeps the module's class path open between
analyses. It checks a file with syntax errors as far as lowering
recovers it. It reports the checker's warnings, and a panic in the
checker is an error, not a crash. A module is analyzed together with
the sources of the modules it depends on, so that their declarations
resolve to source whether or not the build compiled them; those files
are only declared (`Options.Declared`), not checked, and only the
module's own files have diagnostics and facts.

An update after an edit is incremental: the session checks the edited
files and those that use a declaration whose signature changed (a body
edit checks only its file), keeps the other files' diagnostics and
facts, and reads the files it doesn't check without their block bodies.
An edit that changes declarations most files use, or the Java sources,
checks the module whole. Completion checks the completion's file only,
against the other files' signatures, and doesn't change what the next
update starts from. `KCLS_INCREMENTAL=0` checks every update whole, as
a fallback.

Still to come from kc:

- end positions and kotlinc names for the checker's errors, as warnings
  have; kcls widens an error to the expression that starts where it does;
- the type of a constructor call with wrong arguments, which is `Any`
  for now: `val x = Foo()`, for a `Foo` that takes arguments, completes
  `x.` with `Any`'s members;
- which library classes are public: importable classes are all the
  top-level classes of the class path's jars and the JDK's `src.zip`;
- completion in imports and package directives, and of nested classes in
  types (`Map.Entry`); override stubs name types by their simple names,
  without importing them;
- module source dependencies as ABI stubs, for large multi-module builds
  (a module is analyzed with its dependencies' sources);
- override links and declaration flags (open, val or var, deprecated)
  in the facts: kcls reads overrides and flags from the syntax, and
  library members' flags it doesn't know;
- library top-level functions and properties for workspace symbols;
- kotlinc names for errors, so quick fixes need not match messages: the
  fixes of errors (unresolved references, non-exhaustive `when`, missing
  implementations) recognize them by kc's message; more fixes for
  errors (type mismatches, missing arguments) need names and ranges;
- which names an import brings into use: organize imports judges by
  names, so an import whose name a member or local also has counts as
  used;
- formatting that wraps long lines, adds or removes line breaks, or
  aligns, as IntelliJ's options for each construct do; and extracting a
  variable from an expression-bodied function or a property initializer,
  which takes converting it to a block first;
- renaming a class with its file, which IntelliJ offers on a file
  rename;
- running Android code, which needs Gradle and a device, and debugging,
  which needs a debug adapter (not a goal of kcls); a run doesn't wait
  for a Gradle import that has yet to see a new module, so a test file
  in a new source set runs with the task its directories name
  (`src/test` → `test`, `src/jvmTest` → `jvmTest`);
- Kotlin/JS, Native and Wasm, which kc doesn't compile;
- a Maven local repository other than `~/.m2/repository`, from
  `settings.xml`, for the class path of a module Maven couldn't resolve.

## Tests

- `go test ./...` runs the unit tests and the end-to-end
  tests. The latter drive a server through a scripted client over pipes:
  the lifecycle, editing, pushed and pulled diagnostics, and files read
  from disk.
- `internal/features/testdata/*.golden` records each feature's output
  for the `.kt` files next to it. `testdata/semantic/<dir>.golden`
  records, for each module directory, the diagnostics, and the hover,
  definitions and highlights at each `/*|*/` caret, and the semantic
  tokens of the names. `testdata/workspace/<dir>.golden` records the
  references, implementations, rename and hierarchies at each caret.
  `testdata/completion/<dir>.golden` records the first completion items
  and the signature help at each caret, the carets removed.
  `testdata/inlay/<dir>.golden` records the inlay hints, inline, with
  the default settings and with every parameter name.
  `testdata/actions/<dir>.golden` records each diagnostic's quick fixes,
  the intentions at each word, and organize imports, with their edits.
  Check a change, then
  accept it with `go test ./internal/features -update`.
- `internal/format/testdata/<name>.golden` is `<name>.kt` formatted.
  `TestWhitespaceOnly` formats every Kotlin file of this repository and
  of the kotlin-compiler-go module go.mod requires, and
  checks that only whitespace changes and that formatting again changes
  nothing. `KCLS_FORMAT_CORPUS=<dir> go test ./internal/format -run
  Corpus -v` reports the lines formatting changes in formatted code,
  such as the stdlib's sources (`kotlin-stdlib-sources.jar`, unzipped).
- `TestRunMain` (`internal/features`) builds kc, plans a run lens of
  a module with Kotlin, Java and resources, and runs it with the JDK;
  `-short` skips it.
- `TestMavenModel` (`internal/project`) reads a reactor's effective
  POM and the dependency plugin's log, written in Maven's formats, into
  modules. `TestRoots` (`internal/engine`) links two workspace
  folders.
- `KCLS_MAVEN_TEST=1 go test ./internal/project -run ImportMaven`
  imports a two-module Maven build. It needs Maven (`KCLS_MAVEN` names
  the binary) and the help and dependency plugins, cached or online.
- `KCLS_GRADLE_TEST=1 go test ./internal/project -run Gradle` imports
  a two-project Gradle build. It needs Gradle (`KCLS_GRADLE` names the
  binary) and the Kotlin Gradle plugin, cached or online.
- `cd editors/vscode && npm test` compiles the extension and tokenizes a
  sample with its TextMate grammar through Oniguruma, as VS Code does.
