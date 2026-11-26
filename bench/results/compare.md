# kcls against other Kotlin language servers

`go run ./bench/compare`, 2026-11-26, linux/amd64, 4 CPUs, 15 GB. The
generated module of kc's `bench/gen -n 200` (201 files, File100.kt opened,
403 lines); median of 3 runs, each 20 body edits and 30 member accesses.

- **kcls**: this repository at 6aa7614, folder model.
- **kc-lsp**: `cmd/kc-lsp` of barteo/kotlin-compiler-go `work/kc-lsp` at 3dd19dd, folder model.
- **kotlin-lsp**: JetBrains' Kotlin LSP ILS-271.2085.0 (bundled JBR 25, default `-Xmx2g`),
  pull diagnostics (it has no push), on a copy of the module with a
  `workspace.json`: one module, JDK 21, kotlin-stdlib 2.2.21 and annotations 13.0.

Cold: caches wiped before each run (`KCLS_CACHE`, `~/.cache/kc-lsp`,
`~/.cache/JetBrains`). Warm: caches from the previous run kept.

| measurement | kcls | kc-lsp | kotlin-lsp |
|---|---|---|---|
| initialize response | 12 ms | 5 ms | 3.58 s |
| start to first semantic diagnostics, cold | 3.72 s | 3.59 s | 24.5 s |
| start to first semantic diagnostics, warm | 3.46 s | 3.50 s | 14.0 s |
| body edit to its diagnostics, p50 / p95 | 2.84 / 3.02 s | 399 / 446 ms | 1.09 / 1.88 s |
| completion after a dot, p50 / p95 | 1.28 / 1.49 s | 271 / 374 ms | 251 ms / 1.01 s |
| completion items, average | 88 | 114 | 353 |
| definition, p50 / p95 | 1 / 13 ms | 1 / 16 ms | 5 / 125 ms |
| definitions resolved, of 30 | 5 | 22 | 28 |
| hover, p50 / p95 | 0 / 10 ms | 0 / 9 ms | 35 / 247 ms |
| hovers with content, of 30 | 22 | 22 | 30 |
| resident memory after first diagnostics, cold | 817 MB | 802 MB | 1627 MB |
| resident memory at the end, cold | 1483 MB | 1092 MB | 1826 MB |
| other errors / warnings in the opened file | 0 / 1 | 0 / 1 | 0 / 18 |

Times wait for semantic results: the opened file gets an unresolved
reference and start is timed to the diagnostics that report it; each edit
adds a new unresolved reference and is timed to the diagnostics naming it.
kotlin-lsp's diagnostics include IntelliJ's inspections (the 18 warnings).
The three runs of each cell were within 10% of each other.
