# Named identity admission

## TL;DR

- Issue #356 / M05-PR58a implements post-signature identity admission under accepted ADR 0046.
- Keep accountable actor and issuer-attested caller separate; return no legacy principal or permissions.
- Recheck trusted subject/caller policy on every invocation and enforce five-minute credentials with thirty-second skew.
- No JWT parser, key retrieval, policy storage, HTTP mode or Keycloak qualification is included. Legacy serving is unchanged.

## Boundary and ownership

`authentication.IdentityAuthenticator.AuthenticateIdentity` accepts an opaque
credential through a required trusted `IdentityCredentialVerifier`, then applies
one exact issuer/audience/client profile. `SignedIdentity` is that verifier's
signature-checked, independently trustworthy result, not an unverified JWT decoder
or a request DTO. Its access-token classification must come from a qualified token
profile. This component does not interpret Keycloak claim names, accept request
headers as identity, fetch keys or provide a signature-verifier implementation.

After profile validation, the required `IdentityPolicy` must check current subject
enrollment/kind and caller delegation. The requested HUMAN/CI kind comes from the
trusted client profile; it is not proof that the subject is enrolled in that kind.
The policy implementation must verify that match and deny missing, disabled or
incompatible enrollment. No identity-policy decision is cached here. Durable policy
storage, administration and revocation are subsequent implementation work, not
provided by a no-op policy or the test doubles in this slice.

Successful output preserves exact issuer/subject as actor, issuer plus namespaced
`client:<authorized-client>` as caller, and the admitted actor kind. It contains
no service grant, legacy principal or baseline-approval permission. Current service
authorization remains PR45's boundary. Issuer-attested caller identity does not
prove the presenting process or constrain bearer replay; ADR 0046's accepted limits
still apply.

## Bounds and failure semantics

- Trusted profile issuer is HTTPS without user info, query or fragment, including
  empty delimiters. Compare the signed issuer exactly without normalization.
- Require exactly the configured audience and an allowlisted client. Reject
  non-access credentials and malformed subject identity. Configuration is copied
  defensively; client profiles and audiences are bounded, with at most 100 clients.
- Credentials are bounded to 16 KiB of non-whitespace ASCII before verifier work.
  This is a private operation's bound, not a change to the legacy HTTP token limit.
- Require issuance and expiration, positive lifetime no greater than five minutes,
  age no greater than five minutes plus the approved thirty-second clock tolerance,
  and expiration/not-before checks with that tolerance. Not-before must precede
  expiration. Resample time after policy work so slow policy I/O cannot admit a
  credential that has become expired outside the tolerance.
- Invalid/missing/revoked identity returns `ErrUnauthenticated` with zero identity.
  Unclassified verifier/policy failures become safe `run.ErrUnavailable`; context
  cancellation/deadline identity is preserved. Raw backend errors and partial
  identity never escape. Cancellation after verification prevents policy work.

Verifier, policy and injected clock must be concurrency-safe. The constructor
performs no I/O. The separate method/result intentionally do not implement the
legacy HTTP authenticator, and no legacy fallback is attempted.

## Qualification and remaining work

The TDD authentication tests use trusted verifier/policy functions to exercise
admission, ownership of configuration, concurrent calls, fresh revocation, clock
bounds, safe errors and cancellation. Existing Static tests run alongside them.
These tests do not establish cryptographic verification, token-type correctness,
key freshness/rotation, real enrollment revocation or Keycloak compatibility.

Next slices must admit JWT/library handling and fixed key origins/freshness,
implement current enrollment/caller policy, qualify real pinned Keycloak tokens,
and only then compose a feature-gated HTTP identity mode with safe error mapping.
Full issue #356 acceptance also requires serving tests and real trust receipts;
public service access/rerun serving remains disabled. No release/library/deployment
pin, database migration, BFF login or infrastructure change is made here.

Run `go test -vet=off -race -count=1 ./internal/authentication` and `make validate`
with the pinned Go toolchain. No PostgreSQL or Kubernetes protocol changed in this
slice, so specialized live gates do not qualify this boundary. Rollback removes
the unused-by-serving operation and documentation without changing legacy behavior,
persisted grants, identity bindings or audit evidence.
