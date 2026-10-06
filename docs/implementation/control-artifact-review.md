# Control artifact review

Read-only contract: `GET /v1/jobs/{job}/artifacts/{artifact}/review` requires
`jobs:read` plus matching owner/project and an ACCEPTED artifact bound to the Job.
Return artifact SHA256, producing Attempt/generation, current-generation flag and
bounded plain-text members. Verify the stored byte size and full SHA256 before
preview. Reject links, traversal, duplicate entries and malformed bundles. Never
extract files, execute a patch, interpret HTML, or carry credentials in a URL.

Limits: 8 MiB bundle, 64 members, 64 KiB per preview and 256 KiB total text.
Truncation is explicit. Binary and unsupported members expose metadata only.
The existing authenticated raw artifact endpoint remains available for larger
bundles. Preview is read-only and does not require a write lease. Review does not
apply changes or approve Runtime or Goal requests.
