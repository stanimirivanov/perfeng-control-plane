# Managed named identity composition

## TL;DR

- Issue #356 / M05-PR58f assembles the existing identity adapters privately.
- One application-owned instance shares a bounded key cache for one trusted issuer.
- Synthetic JWT, real loopback TLS and PostgreSQL tests prove the adapter chain.
- Public identity serving, real Keycloak admission and BFF login remain separate gates.

## Ownership and behavior

`app.NewNamedIdentityAuthentication` accepts a trusted `NamedIdentityConfig`, a
borrowed `authentication.IdentityPolicyStore` and a concurrent-safe clock. The
configuration holds the exact issuer/audience/client profile, fixed same-origin
HTTPS JWKS endpoint and explicit CA roots. Constructors perform no I/O and copy
root trust/client profiles. Invalid construction returns only safe configuration
errors and retires resources already acquired. There is no environment loader,
issuer discovery, public feature toggle or per-request constructor here.

The owner retains one instance per configured issuer per process, calls its separate
`AuthenticateIdentity` operation, and calls `Stop` when retiring it. Composition
connects fixed-origin fetching → shared bounded refresh/cache → strict RS256 JWT
verification → exact profile/time admission → current enrollment and caller policy.
See [ADR 0046](decisions/0046-select-issuer-and-bff-delegation-trust.md) and
[current policy storage](decisions/0047-retain-current-identity-policy.md).

`Stop` is concurrent-safe and idempotent: prevent new authentication, stop shared
key refresh and close idle transport connections. A successful authentication
rechecks cancellation and shutdown after policy I/O before returning identity.
Stop does not join requests or close the borrowed store; an operation past its
final shutdown check may already have succeeded. Refresh work is independently
bounded to three seconds. Identity admission is an observation, not a transaction
fence for a later service mutation. It grants no service or baseline permission.

## Evidence and admission limits

`TestNamedIdentityCompositionAgainstTLSAndPostgres` runs through `make db-validate`
and therefore the hosted PostgreSQL integration job. It uses a newly generated
RSA key pair and a second rotation key, test-only TLS trust, a fake clock and a
random disposable loopback database. Signed HUMAN/CI credentials retain exact
actor/caller identities; policy disablement denies the same still-valid token on
the next operation. Key retirement, stale-key outage, shared snapshots and shutdown
during policy I/O fail closed. Fixtures never represent real credentials or data.

Construction/rejection tests also run in `make validate`. These receipts do not
establish actual Keycloak claims, client grant restrictions, BFF exchange, real
rotation or public HTTP failure mapping. Those ADR 0046 gates still block serving
activation; #356 stays open. Legacy Static serving is unchanged. There is no new
dependency, migration or deployment. Rollback removes this unused-by-serving
composition while retaining existing enrollment, grants and audit records.
