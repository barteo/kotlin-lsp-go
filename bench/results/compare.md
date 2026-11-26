# kcls against other Kotlin language servers

`go run ./bench/compare`, 2026-11-26, linux/amd64, 4 CPUs, 15 GB. The
generated module of kc's `bench/gen -n 200` (201 files, File100.kt opened,
403 lines); median of 3 runs, each 20 body edits and 30 member accesses.

- **kcls**: this repository at 69c391d (incremental updates, kotlin-compiler-go
  188c92f), folder model; **kcls before**: at 6aa7614, each edit and
  completion checking the whole module.
- **kc-lsp**: `cmd/kc-lsp` of barteo/kotlin-compiler-go `work/kc-lsp` at 3dd19dd, folder model.
- **kotlin-lsp**: JetBrains' Kotlin LSP ILS-271.2085.0 (bundled JBR 25, default `-Xmx2g`),
  pull diagnostics (it has no push), on a copy of the module with a
  `workspace.json`: one module, JDK 21, kotlin-stdlib 2.2.21 and annotations 13.0.

Cold: caches wiped before each run (`KCLS_CACHE`, `~/.cache/kc-lsp`,
`~/.cache/JetBrains`). Warm: caches from the previous run kept.

| measurement | kcls | kcls before | kc-lsp | kotlin-lsp |
|---|---|---|---|---|
| initialize response | 10 ms | 12 ms | 5 ms | 3.58 s |
| start to first semantic diagnostics, cold | 3.50 s | 3.72 s | 3.59 s | 24.5 s |
| start to first semantic diagnostics, warm | 3.54 s | 3.46 s | 3.50 s | 14.0 s |
| body edit to its diagnostics, p50 / p95 | 350 / 425 ms | 2.84 / 3.02 s | 399 / 446 ms | 1.09 / 1.88 s |
| completion after a dot, p50 / p95 | 205 / 287 ms | 1.28 / 1.49 s | 271 / 374 ms | 251 ms / 1.01 s |
| completion items, average | 88 | 88 | 114 | 353 |
| definition, p50 / p95 | 1 / 22 ms | 1 / 13 ms | 1 / 16 ms | 5 / 125 ms |
| definitions resolved, of 30 | 15* | 5 | 22 | 28 |
| hover, p50 / p95 | 0 / 8 ms | 0 / 10 ms | 0 / 9 ms | 35 / 247 ms |
| hovers with content, of 30 | 22 | 22 | 22 | 30 |
| resident memory after first diagnostics, cold | 844 MB | 817 MB | 802 MB | 1627 MB |
| resident memory at the end, cold | 1113 MB | 1483 MB | 1092 MB | 1826 MB |
| other errors / warnings in the opened file | 0 / 1 | 0 / 1 | 0 / 1 | 0 / 18 |

\* With the JDK's `src.zip` installed for this run (see below), which
the "kcls before" run didn't have: the 10 more are JDK members, not a
change in kcls. The kcls column was measured later the same day, on the
same machine; the other columns are from the first runs.

Times wait for semantic results: the opened file gets an unresolved
reference and start is timed to the diagnostics that report it; each edit
adds a new unresolved reference and is timed to the diagnostics naming it.
kotlin-lsp's diagnostics include IntelliJ's inspections (the 18 warnings).
The three runs of each cell were within 10% of each other.

**Why kcls resolved 5 definitions.** kcls goes to a library declaration
only in that library's sources (a sources jar next to the jar, or the
JDK's `src.zip`), and this machine had none: `KOTLIN_HOME/lib` held only
`kotlin-stdlib.jar`, and the JDK's `lib/src.zip` was a dangling link
(Ubuntu's `openjdk-21-source` not installed). So every stdlib and JDK
member returned null. kc-lsp generates stub sources from the class files
instead, and kotlin-lsp reads the classes. With
`kotlin-stdlib-sources.jar` next to the stdlib and `src.zip` installed,
kcls resolves 22 of 30, as kc-lsp does (`COMPARE_SHOW_DEFS=1` prints each
site's result). Of the other 8, kotlin-lsp resolves the 6 names in import
directives (`import p1.helper101`), which kcls and kc-lsp don't; none of
the three resolves a package segment (`java.util` in `java.util.Random`).
