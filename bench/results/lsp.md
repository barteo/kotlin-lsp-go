# kcls performance

`go run ./bench/lsp -soak 10m -check`, 2026-11-27, with kc's compact
facts (kotlin-compiler-go 81b663e) and occurrences and semantic tokens
reading facts without copying them. On the generated module (201 Kotlin
files, File100.kt opened, 403 lines), linux/amd64, 4 CPUs:

| measurement | median | result | budget |
|---|---|---|---|
| start to first diagnostics | — | 3.46 s **over** | 3.00 s |
| body edit to diagnostics, p95 of 20 | 353 ms | 389 ms | 600 ms |
| completion, p95 of 30 | 234 ms | 298 ms | 600 ms |
| definition, p95 of 30 | 1 ms | 17 ms | 300 ms |
| resident memory | — | 628 MB | 2048 MB |
| memory growth over a 10m0s soak (78 rounds) | 628 → 672 MB, peak 715 MB | +7% | +25% |

A soak's live heap is about 305 MB, of which kc's facts are 92 MB; the
rest is mostly the other files' lowered signatures, parse trees and the
session's incremental state.

Before compact facts, 2026-11-26, with incremental updates
(kotlin-compiler-go 188c92f), on the same machine:

| measurement | median | result | budget |
|---|---|---|---|
| start to first diagnostics | — | 3.46 s **over** | 3.00 s |
| body edit to diagnostics, p95 of 20 | 352 ms | 444 ms | 600 ms |
| completion, p95 of 30 | 223 ms | 283 ms | 600 ms |
| definition, p95 of 30 | 1 ms | 4 ms | 300 ms |
| resident memory | — | 1110 MB | 2048 MB |
| memory growth over a 10m0s soak (80 rounds) | 1110 → 1181 MB, peak 1181 MB | +6% | +25% |

The start budget is the laptop's below; on these 4 CPUs the start took
3.7 s before incremental updates too (bench/results/compare.md).

Before incremental updates, each edit and completion checked the whole
module: 2026-11-26, darwin/arm64, 8 CPUs:

| measurement | median | result | budget then |
|---|---|---|---|
| start to first diagnostics | — | 1.32 s | 3.00 s |
| body edit to diagnostics, p95 of 20 | 1.08 s | 1.17 s | 2.00 s |
| completion, p95 of 30 | 521 ms | 556 ms | 1.00 s |
| definition, p95 of 30 | 0 ms | 3 ms | 300 ms |
| resident memory | — | 1823 MB | 2560 MB |
| memory growth over a 10m0s soak (32 rounds) | 1823 → 1863 MB, peak 1870 MB | +2% | +25% |
