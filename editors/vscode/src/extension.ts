// The VS Code client of kcls, the Kotlin language server built on kc.
// It starts the server over stdio and adds commands to restart it and to
// show its log, and kcls.run, which runs what a run code lens names in a
// terminal. The server's own commands, kcls.reimport, kcls.clearCaches
// and kcls.prepareRun, are registered by the language client from the
// server's executeCommandProvider; package.json gives them their titles.

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import {
  ExecuteCommandRequest,
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;
let output: vscode.OutputChannel | undefined;

// The extensions that also serve Kotlin; two servers for one language
// answer every request twice.
const conflicting = ["jetbrains.kotlin-server", "jetbrains.kotlin", "fwcd.kotlin"];

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  output = vscode.window.createOutputChannel("Kotlin (kcls)");
  context.subscriptions.push(output);

  context.subscriptions.push(
    vscode.commands.registerCommand("kcls.restartServer", async () => {
      await stop();
      await start(context);
    }),
    vscode.commands.registerCommand("kcls.showLog", () => output?.show(true)),
    vscode.commands.registerCommand("kcls.run", run),
    vscode.workspace.onDidChangeConfiguration(async (e) => {
      if (e.affectsConfiguration("kcls.path") || e.affectsConfiguration("kcls.args")) {
        await stop();
        await start(context);
      }
    }),
  );

  const others = conflicting.filter((id) => vscode.extensions.getExtension(id));
  if (others.length > 0) {
    vscode.window.showWarningMessage(
      `Kotlin (kcls): ${others.join(", ")} also serves Kotlin. Disable one of them so that requests aren't answered twice.`,
    );
  }
  await start(context);
}

export async function deactivate(): Promise<void> {
  await stop();
}

async function start(context: vscode.ExtensionContext): Promise<void> {
  const config = vscode.workspace.getConfiguration("kcls");
  const command = serverPath(context, config.get<string>("path", ""));
  const args = config.get<string[]>("args", []);
  output?.appendLine(`Starting ${command} ${args.join(" ")}`);

  const serverOptions: ServerOptions = {
    run: { command, args, transport: TransportKind.stdio },
    debug: { command, args: [...args, "-log", path.join(context.logUri.fsPath, "kcls.log")], transport: TransportKind.stdio },
  };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "kotlin" }],
    outputChannel: output,
    traceOutputChannel: output,
    // The kcls.hints settings, at start and as they change.
    initializationOptions: { hints: config.get("hints") },
    synchronize: { configurationSection: "kcls" },
  };
  client = new LanguageClient("kcls", "Kotlin (kcls)", serverOptions, clientOptions);
  try {
    await client.start();
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window
      .showErrorMessage(`Kotlin (kcls) couldn't start ${command}: ${msg}. Set kcls.path to the kcls binary.`, "Open Settings")
      .then((pick) => {
        if (pick) {
          vscode.commands.executeCommand("workbench.action.openSettings", "kcls.path");
        }
      });
    client = undefined;
  }
}

async function stop(): Promise<void> {
  if (!client) {
    return;
  }
  const c = client;
  client = undefined;
  try {
    await c.stop();
  } catch (err) {
    output?.appendLine(`Stopping the server: ${err}`);
  }
}

// RunPlan is the server's answer to kcls.prepareRun: a POSIX shell
// command and the directory to run it in.
interface RunPlan {
  name: string;
  cwd: string;
  command: string;
}

// run runs the main function or tests of a run code lens: it saves the
// open files, which the run compiles from disk, asks the server how to
// run them, and runs that as a task in a terminal.
async function run(args: unknown): Promise<void> {
  if (!client) {
    vscode.window.showErrorMessage("Kotlin (kcls): the language server isn't running.");
    return;
  }
  await vscode.workspace.saveAll(false);
  let plan: RunPlan;
  try {
    plan = await client.sendRequest(ExecuteCommandRequest.type, { command: "kcls.prepareRun", arguments: [args] });
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err);
    vscode.window.showErrorMessage(`Kotlin (kcls): can't run: ${msg}`);
    return;
  }
  const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(plan.cwd));
  const task = new vscode.Task(
    { type: "kcls", name: plan.name },
    folder ?? vscode.TaskScope.Workspace,
    plan.name,
    "kcls",
    new vscode.ProcessExecution("/bin/sh", ["-c", plan.command], { cwd: plan.cwd }),
  );
  task.presentationOptions = {
    reveal: vscode.TaskRevealKind.Always,
    panel: vscode.TaskPanelKind.Dedicated,
    clear: true,
    focus: false,
  };
  await vscode.tasks.executeTask(task);
}

// serverPath returns the server to run: the configured path, else the
// binary bundled in the extension's server directory, else kcls on PATH.
function serverPath(context: vscode.ExtensionContext, configured: string): string {
  if (configured) {
    return configured.replace(/^~(?=$|\/)/, process.env.HOME ?? "~");
  }
  const bundled = context.asAbsolutePath(path.join("server", process.platform === "win32" ? "kcls.exe" : "kcls"));
  if (fs.existsSync(bundled)) {
    return bundled;
  }
  return "kcls";
}
