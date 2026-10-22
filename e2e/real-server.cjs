const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const { spawn, spawnSync } = require("node:child_process");

const root = path.resolve(__dirname, "..");
const port = Number(process.env.E2E_REAL_PORT || 4174);
const collectorPort = port + 10;
const workspace = fs.mkdtempSync(path.join(os.tmpdir(), "cpa-real-e2e-"));
const statePath = path.join(os.tmpdir(), `cpa-real-e2e-${port}.json`);
const children = [];
const db = path.join(workspace, "archive.sqlite");
const commands = ["cpa-session-collector", "cpa-session-import"];
const binaries = Object.fromEntries(commands.map((name) => [name, path.join(workspace, name)]));
binaries.adapter = path.join(workspace, "adapter");

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: "utf8", ...options });
  if (result.status !== 0) throw Error(`${command} failed: ${result.stderr || result.stdout}`);
  return result.stdout;
}

function start(command, args, env = {}) {
  const child = spawn(command, args, { cwd: root, stdio: "inherit", env: { ...process.env, ...env } });
  children.push(child);
  child.on("exit", (code, signal) => {
    if (!stopping && signal !== "SIGTERM" && (code !== 0 || signal)) {
      console.error(`Real E2E child exited: ${command} (${code || signal})`);
      stop(1);
    }
  });
  return child;
}

let stopping = false;
function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  for (const child of children) child.kill("SIGTERM");
  fs.rmSync(statePath, { force: true });
  setTimeout(() => {
    fs.rmSync(workspace, { recursive: true, force: true });
    process.exit(code);
  }, 300);
}
process.on("SIGINT", () => stop());
process.on("SIGTERM", () => stop());

async function main() {
  for (const name of commands) run("go", ["build", "-o", binaries[name], `./cmd/${name}`]);
  run("go", ["build", "-o", binaries.adapter, "./e2e/real-server"]);
  const timestamp = "2026-10-22T10:00:00Z";
  const codex = [
    { timestamp, type: "session_meta", payload: { id: "safe-codex", cwd: "/fixture/project", timestamp } },
    { timestamp, type: "response_item", payload: { id: "msg-1", type: "message", role: "user", content: [{ type: "input_text", text: "Archive fixture request" }] } },
    { timestamp: "2026-10-22T10:00:01Z", type: "response_item", payload: { id: "call-1", type: "function_call", name: "shell_command", arguments: '{"command":"printf fixture"}', call_id: "fixture-call" } },
    { timestamp: "2026-10-22T10:00:02Z", type: "response_item", payload: { id: "out-1", type: "function_call_output", output: "fixture tool output", call_id: "fixture-call" } },
    { timestamp: "2026-10-22T10:00:03Z", type: "response_item", payload: { id: "msg-2", type: "message", role: "assistant", content: [{ type: "output_text", text: "Archive fixture complete" }], encrypted_content: "fixture-opaque-exact-bytes" } },
  ];
  const claude = [
    { uuid: "claude-user", sessionId: "safe-claude", timestamp, type: "user", cwd: "/fixture/claude", message: { role: "user", content: [{ type: "text", text: "Claude fixture request" }] } },
    { uuid: "claude-assistant", parentUuid: "claude-user", sessionId: "safe-claude", timestamp: "2026-10-22T10:00:04Z", type: "assistant", message: { role: "assistant", content: [{ type: "text", text: "Claude fixture complete" }] } },
  ];
  const sources = [];
  for (const [tool, account, events] of [["codex", "clawdshome", codex], ["claude", "minoo", claude]]) {
    const source = path.join(workspace, `${tool}.jsonl`);
    const raw = events.map((event) => JSON.stringify(event)).join("\n") + "\n";
    fs.writeFileSync(source, raw);
    const args = ["--db", db, "--source", source, "--host", "m1", "--account", account, "--tool", tool];
    const first = JSON.parse(run(binaries["cpa-session-import"], args));
    const replay = JSON.parse(run(binaries["cpa-session-import"], args));
    sources.push({ tool, account, source, sha256: crypto.createHash("sha256").update(raw).digest("hex"), bytes: Buffer.byteLength(raw), first, replay });
  }
  fs.writeFileSync(statePath, JSON.stringify({ db, importer: binaries["cpa-session-import"], sources }));
  start(binaries["cpa-session-collector"], [], { ARCHIVE_DB: db, LISTEN_ADDR: `127.0.0.1:${collectorPort}` });
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      if ((await fetch(`http://127.0.0.1:${collectorPort}/healthz`)).ok) break;
    } catch {}
    if (attempt === 99) throw Error("Real collector did not become healthy");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  start(binaries.adapter, ["--listen", `127.0.0.1:${port}`, "--collector", `http://127.0.0.1:${collectorPort}`]);
}
main().catch((error) => {
  console.error(error);
  stop(1);
});
