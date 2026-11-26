# kcls: a Kotlin language server in Go

`kcls` is a [Language Server Protocol](https://microsoft.github.io/language-server-protocol/)
server for Kotlin, built on [kc](https://github.com/barteo/kotlin-compiler-go),
a Kotlin compiler written in Go: its lossless `psi` parser and its
checker, through kc's public `analysis` package. It aims to match
JetBrains' [kotlin-lsp](https://github.com/Kotlin/kotlin-lsp) feature for
feature, for VS Code and for coding agents such as Claude Code, as one Go
binary that starts in milliseconds and needs no JVM.

```sh
go install github.com/barteo/kotlin-lsp-go/cmd/kcls@latest
kcls                  # LSP on stdin and stdout
kcls -listen :9999    # LSP on TCP
```

- **VS Code:** the *Kotlin (kcls)* extension, from the VS Code
  Marketplace, Open VSX or a release: [editors/vscode](editors/vscode/README.md).
- **Claude Code:** `/plugin marketplace add barteo/kotlin-lsp-go`, then
  `/plugin install kcls@kotlin-lsp-go`: [editors/claude-code](editors/claude-code/README.md).
- **Other editors:** run `kcls` as a stdio language server for `.kt` and
  `.kts` files.

[docs/lsp.md](docs/lsp.md) lists what it does, how projects are
imported (Gradle, Maven, `kcls.json`), how fast it is, and what comes
next.

## Development

kcls requires kc's module at the version in go.mod. To work on both at
once, put a `go.work` (ignored by git) next to go.mod:

```sh
go work init . ../kotlin-compiler-go
```

The tests need a JDK (`JAVA_HOME`, or `java` on `PATH`) and the Kotlin
standard library (`KOTLIN_HOME`, or `kotlinc` on `PATH`):

```sh
go test ./...
cd editors/vscode && npm ci && npm test
```

`go run ./bench/lsp` measures kcls against its performance budgets.

## License

Apache License 2.0; see [LICENSE](LICENSE).
