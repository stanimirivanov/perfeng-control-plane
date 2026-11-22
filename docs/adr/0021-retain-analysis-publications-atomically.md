# ADR-0021: Retain analysis publications atomically

- Status: Proposed
- Date: 2026-10-29
- Milestone: M01 — Local Performance Decision Loop
- Deciders: Repository owner and integration consumers
- Supersedes: None
- Superseded by: None

## TL;DR

- Retain one immutable, verified publication per authenticated scoped request.
- Check admission/native/report associations and preserve exact canonical bytes.
- Replay and reads use historical evidence without mutable approval lookups.

## Decision and consequences

The integration core owns a publication storage port and trusted offline verifier
callback, avoiding a PostgreSQL-to-integrationcontract dependency. The caller must
fetch exact inputs before entering storage; the verifier uses the existing builder
and codec against transaction-supplied admission and returns the exact proposed
canonical bytes. Schema-valid claims alone grant no publication authority.

Migration 0021 adds a separate ledger with request/event uniqueness, payload digest,
registered report foreign key and immutable database guards. Writes take scoped
event then request advisory locks and verify retained admission and report identity.
Exact retry returns original bytes without re-verification; conflicting reuse is
rejected. Reads return owned bytes and recheck digest and retained associations;
unknown, invisible and unpublished requests share not-found behavior.

Legacy unbound requests cannot publish or acquire a reconstructed native binding.
No HTTP route, worker, delivery or current-policy lookup is added. Runtime trusted
policy/provenance retention remains a prerequisite to activating publication.
Apply the additive migration and grants before a consumer; retain the ledger on
application rollback. See the PostgreSQL and publication guides for operations.
