// Tokenizes a sample with the TextMate grammar through vscode-textmate and
// Oniguruma, as VS Code does, and checks the scopes of a few tokens. It
// fails on a pattern Oniguruma rejects.
const fs = require("fs");
const path = require("path");
const assert = require("assert");
const vsctm = require("vscode-textmate");
const oniguruma = require("vscode-oniguruma");

async function main() {
  const wasm = fs.readFileSync(path.join(require.resolve("vscode-oniguruma"), "..", "onig.wasm")).buffer;
  await oniguruma.loadWASM(wasm);
  const registry = new vsctm.Registry({
    onigLib: Promise.resolve({
      createOnigScanner: (p) => new oniguruma.OnigScanner(p),
      createOnigString: (s) => new oniguruma.OnigString(s),
    }),
    loadGrammar: async () =>
      vsctm.parseRawGrammar(fs.readFileSync(path.join(__dirname, "..", "syntaxes", "kotlin.tmLanguage.json"), "utf8"), "kotlin.tmLanguage.json"),
  });
  const grammar = await registry.loadGrammar("source.kotlin");
  const lines = [
    "package a.b",
    "import kotlin.math.*",
    "/** Doc with @param x and [Ref]. */",
    "@Deprecated(\"old\") data class Point(val x: Int, var y: Double = 1.5e3)",
    "fun <T> List<T>.second(): T = this[1] // comment",
    "val s = \"a\\n${x + 1} $name\" + \"\"\"raw $y\"\"\"",
    "loop@ for (i in 0..10) { if (i is Int) break@loop else continue }",
    "val c = 'x'; val h = 0xFF_FFL; val n: String? = null ?: \"\"",
  ];
  const scopes = {};
  let rule = vsctm.INITIAL;
  for (const line of lines) {
    const r = grammar.tokenizeLine(line, rule);
    for (const t of r.tokens) {
      const text = line.substring(t.startIndex, t.endIndex);
      (scopes[text] ??= []).push(t.scopes[t.scopes.length - 1]);
    }
    rule = r.ruleStack;
  }
  const has = (text, scope) =>
    assert.ok((scopes[text] ?? []).some((s) => s.startsWith(scope)), `${JSON.stringify(text)}: ${scopes[text]} lacks ${scope}`);
  has("package", "keyword.other.package");
  has("a.b", "entity.name.namespace");
  has("@param", "keyword.other.documentation");
  has("Deprecated", "storage.type.annotation");
  has("data", "storage.modifier");
  has("Point", "entity.name.type.class");
  has("1.5e3", "constant.numeric.float");
  has("second", "entity.name.function");
  has("// comment", "comment.line");
  has("\\n", "constant.character.escape");
  has("name", "variable.other.template");
  has("loop@", "entity.name.label");
  has("@loop", "entity.name.label");
  has("is", "keyword.operator.type");
  has("0xFF_FFL", "constant.numeric.hex");
  has("'x'", "string.quoted.single");
  has("null", "constant.language");
  has("?:", "keyword.operator.elvis");
  console.log("grammar ok");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
