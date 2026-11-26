# kcls performance

`go run ./bench/lsp -soak 10m -check`, 2026-11-26. On the generated module (201 Kotlin files, File100.kt opened, 403 lines), darwin/arm64, 8 CPUs:

| measurement | median | result | budget |
|---|---|---|---|
| start to first diagnostics | — | 1.32 s | 3.00 s |
| body edit to diagnostics, p95 of 20 | 1.08 s | 1.17 s | 2.00 s |
| completion, p95 of 30 | 521 ms | 556 ms | 1.00 s |
| definition, p95 of 30 | 0 ms | 3 ms | 300 ms |
| resident memory | — | 1823 MB | 2560 MB |
| memory growth over a 10m0s soak (32 rounds) | 1823 → 1863 MB, peak 1870 MB | +2% | +25% |
