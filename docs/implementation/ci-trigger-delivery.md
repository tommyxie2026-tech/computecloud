# CI Trigger / Delivery adapter (INT-01 first slice)

`scripts/ci_trigger_delivery.py` adapts an authenticated CI run to one existing
computecloud Job. It is a local CI entrypoint and result-delivery file, not a
new server or workflow engine. The CLI uses the existing `client.token_file` and
TLS configuration. The adapter never receives a token argument or creates a PR.

The caller provides a stable delivery ID, a full source commit, and a frozen Job
spec whose `workspace.base_commit` matches that commit. The adapter derives one
idempotency key from delivery ID and commit, so a transport retry resubmits the
same Job; a changed body with the same key is rejected by the Server. It polls
the Job to a terminal state and writes a mode-0600 JSON record containing the
source/spec hash, Job/trace IDs, terminal state, result hash, manifest hash and
final Artifact IDs/SHA-256 values. Failure and timeout also write a bounded
record and return nonzero. The report omits Job summary text, tokens and raw
CLI errors. It never auto-cancels a Job on CI timeout.

Example on a trusted CI runner with a provisioned client config:

```sh
python3 scripts/ci_trigger_delivery.py \
  --binary ./computecloud --config ./computecloud.yaml \
  --spec ./ci-job.json --delivery-id "$CI_DELIVERY_ID" \
  --source-commit "$CI_SOURCE_SHA" \
  --out ./dist/ci-job-delivery.json --timeout-seconds 3600
```

Use a stable ID for a logical CI Job across retries, not a new ID for every
attempt. Upload `ci-job-delivery.json` as the CI artifact even when the command
returns nonzero. It is provenance for the result reference, not proof that any
Artifact content was safe to publish. A separate authorized reviewer can use
the existing Artifact download and review endpoints.

Current scope is one CI-triggered Job and a local report. GitHub/GitLab webhooks,
remote callbacks, automatic PR/commit creation, and live CI-to-Server acceptance
remain outside this slice. Before INT-01 is marked complete, run a trusted CI
integration with a real Server and service identity, exercise duplicate
delivery and failed response/retry, and attach the immutable report. No new
Server schema or execution fact source is introduced.
