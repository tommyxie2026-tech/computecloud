# SDD ledger — plan: docs/superpowers/plans/2026-10-12-api-keys-and-worker-mtls.md

P0 is code-complete; external public-edge evidence remains pending. P1 API key lifecycle and Worker mTLS can proceed in code while keeping target-host rollout evidence explicitly open.

Task 1: Ruling: key metadata stores a digest, short prefix, owner, bounded project/scope/credential JSON, and creation/expiry/revocation timestamps; lookup hides revoked and expired keys as not-found — avoids key-state enumeration and preserves one-way secret storage.
Task 1: RED→GREEN: repository and migration tests initially failed to compile because `APIKey` and repository methods were absent; after adding v19 table/methods and updating schema expectations, focused and full `internal/store` tests passed.
Task 1: Verification: `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./internal/store -count=1` passed; migration tests verify v18→v19 job preservation, SQLite integrity, and foreign-key checks.
Task 1: complete — commit `fb6e059` (`feat: persist revocable API key metadata`).

Task 2: Ruling: managed keys may delegate only job/task/goal scopes already held by the manager and only its project/credential subset; `models:invoke`, conversation scopes, and recursive `api_keys:manage` delegation are excluded until that binding has a complete configuration contract. Key expiry is capped at 90 days; credentials are generated as 32 random bytes and stored only as SHA-256 digest plus short prefix.
Task 2: Tests: management scope, plaintext-on-create/list-redaction, auth without restart, immediate revocation, and project/scope escalation rejection pass. `go test -race ./internal/server ./internal/rpcutil -run 'Test(APIKey|CreateAPIKey)' -count=1` passed.
Task 2: Verification: `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./...` passed after v19 migration and dynamic resolver integration; API-focused race tests passed. Complete — commit pending.
