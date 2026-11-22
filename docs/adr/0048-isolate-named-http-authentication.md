# ADR-0048: Isolate named HTTP authentication from legacy authority

- Status: Proposed
- Date: 2026-11-14
- Milestone: M05 — Operator Visibility and Diagnostics
- Deciders: Repository owner
- Supersedes: None
- Superseded by: None

## TL;DR

- Bind the qualified verifier to a separate, explicitly gated HTTP boundary.
- Pass verified actor/caller/kind; never manufacture a legacy principal or permissions.
- Keep credential denial distinct from unavailable trust; neither permits fallback.
- Register no route or runtime switch until the remaining ADR 0046 gates pass.

## Decision and consequences

M05-PR58i / issue #356 adds an unmounted foundation for service reads, comparisons
and reruns (PR45/48/51). `httpapi` owns its narrow identity/callback representation
and credential-rejection sentinel. `app` explicitly translates the qualified
verifier's value/errors to that boundary. Authentication retains its own result;
no new adapter-to-adapter import, core abstraction, dependency or wire schema is
needed. The HTTP value has no legacy Principal or permission flags.

A trusted boolean gates construction/use of this boundary. Disabled mode returns
safe NOT_FOUND without credential verification or operation work. Enabled mode
requires a constructed verifier and operation. Require one bounded ASCII Bearer
credential. Map explicit rejection to 401 and unavailable, cancelled, unknown or
invalid-success trust to 503, using the existing safe error envelope. The callback
receives only verified identity, and its request clone omits the consumed
Authorization header. Other request metadata remains untrusted.

The operation still owns route/body validation, current service grants, revision
checks and mutation fencing. Authentication grants no service right; successful
admission is an observation, not a transferable permission. Caller remains
issuer attestation, not proof of the network presenter. Incoming actor/service
headers, body fields and token roles cannot substitute for the verified argument.

Future routing must select the authority boundary from trusted configuration and
reviewed routes. Never infer a legacy principal, OR service grants with legacy
ownership, or retry legacy authentication after named rejection. Existing Static
serving and current application startup remain unchanged in this slice.

## Verification, rollout and rollback

TDD checks disabled isolation, malformed/duplicate/oversized credentials, malformed
trusted output, forged metadata, safe errors and cancellation. The explicit real
Keycloak/PostgreSQL gate exercises successful HUMAN/CI authentication, policy
revocation and unavailable trust through a private test callback.

This is handler-level qualification, not a mounted endpoint, ingress/browser
receipt or public activation. No runtime environment flag, BFF session, public
operation or deployment is introduced. Accepted ADR 0046 still requires provider
and BFF conformance together; #356 stays open. Rollback removes this unserved
boundary without changing credentials, persisted grants, identity or audit.
