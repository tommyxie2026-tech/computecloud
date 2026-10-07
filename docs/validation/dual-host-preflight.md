# Dual-host preflight for the v0.4.x Production Baseline

Run this on the **actual Linux Server and each of two independent Linux Worker
hosts** before the [multi-node acceptance cases](multi-node-poc.md). Freeze one
40-character source commit and one run ID for the entire test. If Server shares
Worker A's host, run the collector twice with different roles and data
directories; the comparison reports `server_host_independent=false`.

The collector records a SHA-256 hash of `/etc/machine-id` (or the DBus
machine-id), OS/kernel/architecture, binary hashes, CLI `--version` output,
operator-supplied MCP version, and absolute data directory. It runs no model
calls and reads no credentials. Keep raw configuration and secrets out of the
evidence bundle. Check that the MCP version string matches the deployed MCP
server; it is a declared value, not an independently probed version.

On the Server host:

```sh
python3 scripts/dual_host_preflight.py collect \
  --run-id RUN_ID --source-sha FULL_COMMIT_SHA --role server \
  --node-id server --data-dir /var/lib/computecloud/server \
  --binary /usr/local/bin/computecloud --out server-preflight.json
```

On **each** Worker host, using distinct node IDs and output names:

```sh
python3 scripts/dual_host_preflight.py collect \
  --run-id RUN_ID --source-sha FULL_COMMIT_SHA --role worker \
  --node-id worker-a --data-dir /var/lib/computecloud/worker \
  --binary /usr/local/bin/computecloud \
  --runtime codex=/usr/local/bin/codex \
  --runtime claude=/usr/local/bin/claude \
  --mcp-version PINNED_MCP_VERSION --out worker-a-preflight.json
```

After securely copying the three JSON files into the evidence directory:

```sh
python3 scripts/dual_host_preflight.py verify \
  server-preflight.json worker-a-preflight.json worker-b-preflight.json
```

Verification rejects mismatched commits, duplicate Worker machine IDs,
duplicate node IDs, missing runtime/MCP version records, and shared Server and
Worker data directories on one host. Machine-id checks cannot prove physical
independence on their own: record VM/hypervisor placement or host inventory
separately. The output certifies **only preflight metadata**, never actual
Jobs, fault recovery, real upgrade, or cache performance.

Attach the three files and the comparison output to the run evidence. Continue
with case-level results, timestamps, Task/Attempt IDs, artifact hashes, fault
timeline, SQLite checks, capacity samples, and upgrade/rollback records from
the [multi-node plan](multi-node-poc.md). Mark unrun cases `NOT_RUN`; do not
replace them with CI fixture results.
