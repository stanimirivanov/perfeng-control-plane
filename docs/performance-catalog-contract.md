# Pinned performance catalog conformance

## TL;DR

- Pin performance-catalog/v1 from contracts candidate 0.24.0 independently of existing APIs.
- Validate pages/resources offline and prove resource digest interoperability.
- This foundation adds no endpoint, permission, registry publication or deployment.

## Source and compatibility

The schema and producer fixtures are copied byte-for-byte from perfeng-contracts
commit `041273947703542f7fe22eab838c176df5517974`. The snapshot checksum ledger
records that independent source. Existing API source commit, bundle 0.21.0,
API versions and file bytes remain unchanged. Registry-selected evidence versions
are not upgraded or inferred from this catalog pin.

The existing schema adapter gains anyOf, null and exclusive upper bounds. The
catalog loader translates local definition reference syntax and exact singleton
numeric/Boolean enum literals into the adapter's equivalent const form; string
and multi-value enums are unchanged. Published schema bytes are never rewritten.
Closed bounded resources reject private fields. Issuer URIs must be absolute and
contain no user information, query parameters or fragments.

## Digest and verification

Internal identity/projection types and validation helpers are consumed by the
subsequent catalog publication and request-admission slices. CatalogDigest encodes
the contract's restricted canonical resource array: sorted ASCII object keys,
compact UTF-8 JSON, standard control escaping, no unnecessary HTML/non-ASCII or
line-separator escaping, and no Unicode normalization. Numeric/Boolean resource
values are rejected. The producer fixtures prove the same digest across languages;
a separate Unicode preimage checks Go's ordinary JSON escaping differences.

`go test ./internal/contract` checks provenance, exact file hashes, producer pages,
closed fields, literal constraints and digest compatibility. `make validate`
includes the existing API regression, architecture, race, fuzz and supply-chain
gates. Shape validation cannot authorize a principal, verify external artifact
provenance, or replace complete/fresh snapshot checks in consumers.

## Rollout and remaining scope

This is M09-PR3a, split under the agreed size constraint. M09-PR3b (issue #264)
retains the prepared endpoint, registry/baseline projection, permission and
snapshot-cache work. M09-PR6 also consumes the shared identity/schema foundation.
No runtime behavior is enabled here. Reverting this additive adapter before
adoption requires no data migration. See proposed
[ADR-0009](decisions/0009-pin-performance-catalog-contracts.md).
