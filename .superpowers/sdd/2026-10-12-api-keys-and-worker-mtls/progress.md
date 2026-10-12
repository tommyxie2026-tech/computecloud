# SDD ledger — plan: docs/superpowers/plans/2026-10-12-api-keys-and-worker-mtls.md

P0 is code-complete; external public-edge evidence remains pending. P1 API key lifecycle and Worker mTLS can proceed in code while keeping target-host rollout evidence explicitly open.

Task 1: Ruling: key metadata stores a digest, short prefix, owner, bounded project/scope/credential JSON, and creation/expiry/revocation timestamps; lookup hides revoked and expired keys as not-found — avoids key-state enumeration and preserves one-way secret storage.
Task 1: RED→GREEN: repository and migration tests initially failed to compile because `APIKey` and repository methods were absent; after adding v19 table/methods and updating schema expectations, focused and full `internal/store` tests passed.
Task 1: Verification: `GOCACHE=/private/tmp/computecloud-public-security-gocache go test ./internal/store -count=1` passed; migration tests verify v18→v19 job preservation, SQLite integrity, and foreign-key checks.
Task 1: complete — commit pending.
