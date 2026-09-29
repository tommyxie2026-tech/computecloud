#!/usr/bin/env node
import fs from "node:fs";

const args = Object.fromEntries(process.argv.slice(2).map((value, index, all) => {
  if (!value.startsWith("--")) return [value, true];
  return [value.slice(2), all[index + 1] && !all[index + 1].startsWith("--") ? all[index + 1] : true];
}));
const baseURL = String(args["base-url"] || "").replace(/\/+$/, "");
const token = args["token-file"] ? fs.readFileSync(String(args["token-file"]), "utf8").trim() : String(args.token || "");
if (!baseURL || !token) throw new Error("--base-url and --token/--token-file are required");
const writeJob = String(args["write-job"] || "");

async function request(path, expected = 200, init = {}) {
  const headers = new Headers(init.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  headers.set("Accept", "application/json, text/event-stream");
  const response = await fetch(baseURL + path, { ...init, headers });
  const text = await response.text();
  if (response.status !== expected) throw new Error(`${path}: expected ${expected}, got ${response.status}: ${text}`);
  return { response, text, json: () => JSON.parse(text) };
}

const bootstrap = (await request("/v1/control/bootstrap")).json();
if (!bootstrap.server_epoch || !bootstrap.protocol_max) throw new Error("bootstrap missing protocol/epoch");

const first = (await request("/v1/jobs?limit=2&epoch=" + encodeURIComponent(bootstrap.server_epoch))).json();
if (!first.snapshot_ms || !first.snapshot_id || first.jobs.length !== 2 || !first.has_more || !first.next) {
  throw new Error("job collection does not expose stable pagination");
}
const secondParams = new URLSearchParams({
  limit: "2",
  epoch: first.server_epoch,
  snapshot_ms: first.snapshot_ms,
  snapshot_id: first.snapshot_id,
  before_created_ms: first.next.created_at_ms,
  before_id: first.next.job_id,
});
const second = (await request("/v1/jobs?" + secondParams)).json();
const allJobs = [...first.jobs, ...second.jobs];
if (new Set(allJobs.map((job) => job.job_id)).size < 3) throw new Error("job pagination lost or duplicated fixture jobs");

const workers = (await request("/v1/workers?limit=10&epoch=" + encodeURIComponent(bootstrap.server_epoch))).json();
if (!workers.workers.length || !workers.workers.every((worker) => worker.worker_id && Array.isArray(worker.runtimes))) {
  throw new Error("worker collection missing runtime projection");
}

const target = allJobs[0].job_id;
const [job, tasks, sessions, approvals, artifacts, events] = await Promise.all([
  request("/v1/jobs/" + target).then((r) => r.json()),
  request("/v1/jobs/" + target + "/tasks?limit=100").then((r) => r.json()),
  request("/v1/jobs/" + target + "/sessions").then((r) => r.json()),
  request("/v1/jobs/" + target + "/approvals").then((r) => r.json()),
  request("/v1/jobs/" + target + "/artifacts?limit=100").then((r) => r.json()),
  request("/v1/jobs/" + target + "/events?after_seq=0&limit=500").then((r) => r.json()),
]);
if (job.state !== "SUCCEEDED") throw new Error("fixture job not terminal-success");
const detailCounts = {
  tasks: Array.isArray(tasks.tasks) ? tasks.tasks.length : -1,
  sessions: Array.isArray(sessions.sessions) ? sessions.sessions.length : -1,
  approvals: Array.isArray(approvals.approvals) ? approvals.approvals.length : -1,
  artifacts: Array.isArray(artifacts.artifacts) ? artifacts.artifacts.length : -1,
  events: Array.isArray(events.events) ? events.events.length : -1,
};
if (!tasks.tasks.length || !sessions.sessions.length || !artifacts.artifacts.length || !Array.isArray(approvals.approvals)) {
  throw new Error("job detail projections incomplete: " + JSON.stringify(detailCounts));
}
if (!events.events.length) throw new Error("durable events missing");

const stream = await request("/v1/jobs/" + target + "/events/stream?after_seq=0");
const ids = [...stream.text.matchAll(/^id:\s*(\d+)$/gm)].map((match) => Number(match[1]));
if (!ids.length) throw new Error("SSE replay returned no event IDs");
for (let i = 1; i < ids.length; i++) {
  if (ids[i] !== ids[i - 1] + 1) throw new Error(`SSE gap ${ids[i - 1]} -> ${ids[i]}`);
}
if (ids.at(-1) !== Number(job.last_seq)) throw new Error("SSE terminal watermark mismatch");

const stale = await request("/v1/jobs?epoch=stale&limit=1", 409);
const staleBody = stale.json();
if (staleBody.error?.code !== "SNAPSHOT_EPOCH_CHANGED") throw new Error("stale epoch did not force snapshot reset");

if (writeJob) {
  const holder = "e2e-device-a";
  const body = JSON.stringify({ control_id: "e2e-control-cancel", reason: "UI-03a E2E" });
  const noLease = await request("/v1/jobs/" + writeJob + "/control/cancel", 400, {
    method: "POST", headers: { "Content-Type": "application/json" }, body,
  });
  if (noLease.json().error?.code !== "WRITE_LEASE_REQUIRED") throw new Error("control cancel did not require write lease");

  const lease = (await request("/v1/jobs/" + writeJob + "/control-lease", 201, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ holder_id: holder }),
  })).json();
  if (!lease.lease_token || lease.holder_id !== holder) throw new Error("write lease response incomplete");

  const held = await request("/v1/jobs/" + writeJob + "/control-lease", 409, {
    method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ holder_id: "e2e-device-b" }),
  });
  if (held.json().error?.code !== "WRITE_LEASE_HELD") throw new Error("second write holder was not fenced");

  const renewed = (await request("/v1/jobs/" + writeJob + "/control-lease", 200, {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Control-Lease": lease.lease_token },
    body: JSON.stringify({ holder_id: holder }),
  })).json();
  if (renewed.expires_at_ms < lease.expires_at_ms) throw new Error("write lease did not renew");

  await request("/v1/jobs/" + writeJob + "/control/cancel", 202, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Control-Lease": lease.lease_token },
    body,
  });
  await request("/v1/jobs/" + writeJob + "/control-lease", 204, {
    method: "DELETE",
    headers: { "Content-Type": "application/json", "X-Control-Lease": lease.lease_token },
    body: JSON.stringify({ holder_id: holder }),
  });
}

console.log(JSON.stringify({
  status: "PASSED",
  protocol: bootstrap.protocol_max,
  jobs: allJobs.length,
  workers: workers.workers.length,
  target_job: target,
  events: ids.length,
  token_in_url: false,
}));
