# ADR-0027: Expose scoped analysis publication reads

- Status: Proposed
- Date: 2026-10-30
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Expose the reviewed API 1.1 read with existing bearer request-read permission.
- Use only scoped retained bytes and validate the separate closed publication family.
- Keep reads independent of materialization, artifact fetching and current review.

## Decision and consequences

Pin the integration component to contracts commit
5bbeceec61f0ddd8922f27ceb6d4968cbdfef3ac, bundle 0.26.0 / API 1.1.0.
Its payload schemas/exchanges are unchanged; native, catalog and analysis component
pins stay intact. Inject the existing offline analysis codec into a narrow read
adapter, avoiding a vendor-schema dependency from the HTTP adapter.

Authenticate, require ReadRun and validate encoded path/query identity before
principal-scoped lookup. Validate returned source, principal and full request scope
before writing original bytes. Unknown/invisible/unpublished requests share the
same 404 body/headers; malformed retained evidence is unavailable, never exposed.
No-store precedes authentication; 401 has Bearer challenge and 503 positive retry.
Reject body-bearing/malformed reads and non-GET methods without mutation capability.

Enabled integration runtime requires this read port; disabled runtime is unchanged.
Route using escaped suffixes so encoded /analysis in an opaque lifecycle request ID
does not select the new resource. No migration, credential permission, dependency,
publication writer or artifact access is added. Existing migrated history/ledger
tables remain prerequisites. Roll back the binary while preserving evidence.
