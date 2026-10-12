#!/usr/bin/env node
"use strict";

// Credential-free CLI gateway contract smoke test. Run only in a container with
// --network none: the only reachable service is this process's loopback listener.
const http = require("node:http");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawn } = require("node:child_process");

const fixtures = {
  codex: "cc-codex-fixture-token",
  claude: "cc-claude-fixture-token",
};
const observed = { codex: false, claude: false };
const unexpected = [];
const requests = [];

function sendSSE(res, event, payload) {
  res.write(`event: ${event}\ndata: ${JSON.stringify(payload)}\n\n`);
}

function codexStream(res) {
  const responseId = "resp_contract_fixture";
  const itemId = "msg_contract_fixture";
  const response = {
    id: responseId,
    object: "response",
    status: "completed",
    model: "fixture-model",
    output: [{
      id: itemId,
      type: "message",
      status: "completed",
      role: "assistant",
      content: [{ type: "output_text", text: "fixture-ok", annotations: [] }],
    }],
    usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 },
  };
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  sendSSE(res, "response.created", { type: "response.created", response: { ...response, status: "in_progress", output: [] } });
  sendSSE(res, "response.output_item.added", {
    type: "response.output_item.added", output_index: 0,
    item: { id: itemId, type: "message", status: "in_progress", role: "assistant", content: [] },
  });
  sendSSE(res, "response.content_part.added", {
    type: "response.content_part.added", item_id: itemId, output_index: 0,
    content_index: 0, part: { type: "output_text", text: "" },
  });
  sendSSE(res, "response.output_text.delta", {
    type: "response.output_text.delta", item_id: itemId, output_index: 0,
    content_index: 0, delta: "fixture-ok",
  });
  sendSSE(res, "response.output_text.done", {
    type: "response.output_text.done", item_id: itemId, output_index: 0,
    content_index: 0, text: "fixture-ok",
  });
  sendSSE(res, "response.content_part.done", {
    type: "response.content_part.done", item_id: itemId, output_index: 0,
    content_index: 0, part: { type: "output_text", text: "fixture-ok", annotations: [] },
  });
  sendSSE(res, "response.output_item.done", { type: "response.output_item.done", output_index: 0, item: response.output[0] });
  sendSSE(res, "response.completed", { type: "response.completed", response });
  res.end();
}

function claudeStream(res, model) {
  res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  const message = {
    id: "msg_contract_fixture", type: "message", role: "assistant", model,
    content: [{ type: "text", text: "fixture-ok" }], stop_reason: "end_turn",
    stop_sequence: null, usage: { input_tokens: 1, output_tokens: 1 },
  };
  sendSSE(res, "message_start", { type: "message_start", message: { ...message, content: [], stop_reason: null } });
  sendSSE(res, "content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } });
  sendSSE(res, "content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "fixture-ok" } });
  sendSSE(res, "content_block_stop", { type: "content_block_stop", index: 0 });
  sendSSE(res, "message_delta", { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } });
  sendSSE(res, "message_stop", { type: "message_stop" });
  res.end();
}

function json(res, status, value) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(value));
}

const server = http.createServer((req, res) => {
  const auth = req.headers.authorization || "";
  const codexAuth = auth === `Bearer ${fixtures.codex}`;
  const claudeAuth = auth === `Bearer ${fixtures.claude}` || req.headers["x-api-key"] === fixtures.claude;
  const authKind = codexAuth ? "codex-fixture" : claudeAuth ? "claude-fixture" : auth || req.headers["x-api-key"] ? "wrong-credential" : "missing-credential";
  const pathname = new URL(req.url, "http://localhost").pathname;
  requests.push(`${req.method} ${req.url} auth=${authKind}`);
  const parts = [];
  req.on("data", chunk => parts.push(chunk));
  req.on("end", () => {
    let body = {};
    try { body = JSON.parse(Buffer.concat(parts).toString("utf8") || "{}"); } catch {}
    if (req.method === "POST" && pathname === "/v1/responses" && codexAuth) {
      if (body.model !== "fixture-model") unexpected.push("Codex sent an unexpected model");
      observed.codex = true;
      codexStream(res);
      return;
    }
    if (req.method === "POST" && pathname === "/v1/messages/count_tokens" && claudeAuth) {
      observed.claude = true;
      json(res, 200, { input_tokens: 1 });
      return;
    }
    if (req.method === "POST" && pathname === "/v1/messages" && claudeAuth) {
      if (typeof body.model !== "string" || !body.model) unexpected.push("Claude omitted its model");
      observed.claude = true;
      claudeStream(res, body.model);
      return;
    }
    unexpected.push(`unexpected request ${req.method} ${req.url}`);
    json(res, 401, { error: { type: "authentication_error", message: "fixture rejected request" } });
  });
});

function run(executable, args, env, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(executable, args, { cwd, env, stdio: ["pipe", "ignore", "pipe"] });
    let stderr = "";
    child.stderr.on("data", chunk => { stderr += chunk; });
    const timer = setTimeout(() => child.kill("SIGKILL"), 45000);
    child.stdin.end("Reply with the literal text fixture-ok.");
    child.once("error", error => { clearTimeout(timer); reject(error); });
    child.once("close", code => {
      clearTimeout(timer);
      if (code === 0) return resolve();
      reject(new Error(`${executable} exited ${code}: ${stderr.replaceAll(fixtures.codex, "[fixture]").replaceAll(fixtures.claude, "[fixture]").slice(-1200)}; requests=${requests.join(" | ")}; unexpected=${unexpected.join(" | ")}`));
    });
  });
}

async function main() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "computecloud-cli-contract-"));
  const codexHome = path.join(root, "codex-home");
  const claudeHome = path.join(root, "claude-home");
  fs.mkdirSync(codexHome, { recursive: true, mode: 0o700 });
  fs.mkdirSync(claudeHome, { recursive: true, mode: 0o700 });
  const git = spawn("git", ["init", "--quiet"], { cwd: codexHome, env: { PATH: process.env.PATH, HOME: codexHome }, stdio: "ignore" });
  await new Promise((resolve, reject) => {
    git.once("error", reject);
    git.once("close", code => code === 0 ? resolve() : reject(new Error(`git init failed: ${code}`)));
  });
  server.listen(0, "127.0.0.1");
  await new Promise(resolve => server.once("listening", resolve));
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const common = { PATH: process.env.PATH, CI: "1", HOME: root, NO_PROXY: "127.0.0.1,localhost", no_proxy: "127.0.0.1,localhost" };
  try {
    const codexEnv = {
      ...common, HOME: codexHome, CODEX_HOME: codexHome,
      COMPUTECLOUD_MODEL_TOKEN: fixtures.codex,
      CODEX_DISABLE_UPDATE_CHECK: "1",
    };
    await run("codex", [
      "exec", "--json", "--sandbox", "read-only", "--model", "fixture-model",
      "-c", 'model_provider="computecloud"',
      "-c", 'model_providers.computecloud.name="computecloud"',
      "-c", `model_providers.computecloud.base_url="${baseURL}/v1"`,
      "-c", 'model_providers.computecloud.env_key="COMPUTECLOUD_MODEL_TOKEN"',
      "-c", 'model_providers.computecloud.wire_api="responses"',
      "-c", "model_providers.computecloud.requires_openai_auth=false",
      "-c", "model_providers.computecloud.request_max_retries=0",
      "-c", "model_providers.computecloud.stream_max_retries=0",
      "-",
    ], codexEnv, codexHome);

    const claudeEnv = {
      ...common, HOME: claudeHome, CLAUDE_CONFIG_DIR: claudeHome,
      ANTHROPIC_BASE_URL: baseURL,
      ANTHROPIC_AUTH_TOKEN: fixtures.claude,
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
      DISABLE_AUTOUPDATER: "1",
    };
    await run("claude", [
      "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose",
      "--include-partial-messages", "--permission-mode", "dontAsk", "--model", "sonnet",
    ], claudeEnv, claudeHome);

    if (!observed.codex || !observed.claude || unexpected.length) {
      throw new Error(`contract not satisfied: Codex=${observed.codex}, Claude=${observed.claude}, unexpected=${unexpected.join("; ")}`);
    }
    console.log("PASS: pinned Codex and Claude CLIs used the local fixture endpoint and fixture-only auth; no provider credentials were supplied.");
  } finally {
    server.close();
    fs.rmSync(root, { recursive: true, force: true });
  }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
