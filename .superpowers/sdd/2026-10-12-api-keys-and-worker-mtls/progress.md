# SDD ledger — plan: docs/superpowers/plans/2026-10-12-api-keys-and-worker-mtls.md

P0 is code-complete; external public-edge evidence remains pending. P1 API key lifecycle and Worker mTLS can proceed in code while keeping target-host rollout evidence explicitly open.

Task 1: Ruling: key metadata stores a digest, short prefix, owner, bounded project/scope/credential JSON, and creation/expiry/revocation timestamps; lookup hides revoked and expired keys as not-found — avoids key-state enumeration and preserves one-way secret storage.
Task 1: RED→GREEN: repository and migration tests initially failed to compile because `APIKey` and repository methods were absent; after adding v19 table/methods and updating schema expectations, focused and full `internal/store` tests passed.
Task 1: Verification: `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./internal/store -count=1` passed; migration tests verify v18→v19 job preservation, SQLite integrity, and foreign-key checks.
Task 1: complete — commit `fb6e059` (`feat: persist revocable API key metadata`).

Task 2: Ruling: managed keys may delegate only job/task/goal scopes already held by the manager and only its project/credential subset; `models:invoke`, conversation scopes, and recursive `api_keys:manage` delegation are excluded until that binding has a complete configuration contract. Key expiry is capped at 90 days; credentials are generated as 32 random bytes and stored only as SHA-256 digest plus short prefix.
Task 2: Tests: management scope, plaintext-on-create/list-redaction, auth without restart, immediate revocation, and project/scope escalation rejection pass. `go test -race ./internal/server ./internal/rpcutil -run 'Test(APIKey|CreateAPIKey)' -count=1` passed.
Task 2: Verification: `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./...` passed after v19 migration and dynamic resolver integration; API-focused race tests passed. Complete — commit pending.

Task 2: complete — commit `f5709fc` (`feat: manage expiring user API keys`).

Task 3: Ruling: gRPC TLS requests but does not globally require client certificates; the auth interceptor requires a verified leaf only for Worker principals so user gRPC remains compatible. Worker certs must carry exactly one `spiffe://computecloud/workers/{worker_id}` URI and a configured SHA-256 thumbprint. `optional` supports per-Worker migration: no fingerprint means temporary token-only; after pinning a fingerprint, cert becomes mandatory for that Worker. `required` config validation requires every Worker fingerprint. Removing a thumbprint and restarting revokes that certificate; overlapping old/new thumbprints support rotation.
Task 3: RED→GREEN: mTLS tests initially failed to compile because config fields, TLS hooks, and client certificate support were absent. Added valid/invalid SAN, unpinned thumbprint, no certificate, unknown CA, expired cert, and user no-cert/user-cert cases; all focused tests pass.
Task 3: Verification: `go test ./internal/rpcutil ./internal/server ./internal/worker`, `go test -race ./internal/rpcutil ./internal/server`, and `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./...` passed.
Task 3: pending commit `feat: authenticate workers with per-node mTLS`.

Task 3: complete — commit `cfb5403` (`feat: authenticate workers with per-node mTLS`).

Task 4: Ruling: offline bootstrap refuses an existing active manager key, writes only a new mode-0600 key file, persists only its digest/prefix, and prints metadata without the plaintext. It is run while Server is stopped because opening the data directory takes its exclusive lock and may perform v18→v19 migration. Only explicit delegable job/task/goal scopes can be bootstrapped; model/conversation scopes are excluded until they have complete credential bindings.
Task 4: Tests: bootstrap dispatch, no plaintext output, file permissions, duplicate manager and output-file refusal, and unsafe scope/expiry cases pass. Required mTLS/fingerprint and missing client key config checks pass. Four test-only certificate utility cases pass.
Task 4: Verification: `go test ./...`, full Server/rpcutil race tests, 13 Python security tests, edge policy checker, Python bytecode compile, and `git diff --check` pass.
Task 4: pending commit `docs: add API key and worker certificate lifecycle`.
