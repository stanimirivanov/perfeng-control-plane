# ADR-0046: Select issuer and BFF delegation trust

- Status: Accepted
- Date: 2026-11-13
- Milestone: M05 — Operator Visibility and Diagnostics; prerequisite from M06
- Deciders: Repository owner
- Accepted: 2026-11-13; owner approved all four recommended defaults
- Issue: [#356](https://github.com/stanimirivanov/perfeng-control-plane/issues/356)
- Supersedes: None
- Superseded by: None

## TL;DR

- Use **Keycloak**, with server-side OIDC login and supported same-realm token exchange for the Console BFF.
- Keep the person, calling client and service permissions separate. Perfeng owns current service grants and delegation policy.
- Start CI with a separate Keycloak machine client; qualify GitHub workload federation separately rather than treating a workflow initiator as the human actor.
- Use stable issuer/subject identities and short-lived, audience-restricted credentials. Browser JavaScript receives no access/refresh token.
- Owner approval accepts this architecture; implementation waits for review and merge of the documentation PR. Release pins, deployment, libraries and passing admission receipts remain separate gates.

## Context and fixed constraints

[Confirmed UI intent](https://github.com/stanimirivanov/perfeng-console/blob/aa8f78c9ae6d34ac37a77195da26e01c1d4e70a0/docs/intent/perfeng-ui.md) requires Maya to follow a CI link, sign in as herself, inspect her service's baseline comparison and request an attributable rerun. Baseline approval is a separate permission. Local operation comes first; shared access follows. The existing Angular/BFF/public-API boundaries remain fixed.

[Plan PR58](https://github.com/stanimirivanov/perfeng-console/blob/aa8f78c9ae6d34ac37a77195da26e01c1d4e70a0/tasks/plan.md#m05-pr58-verify-named-human-and-ci-identities-at-the-provider) requires issuer/trust selection before implementation. PR45 adds service-authorized reads; PR51 adds public rerun serving. The [service-identity contract](https://github.com/stanimirivanov/perfeng-contracts/blob/900f5e97f8056fa9972080458583e8064b4d225e/docs/service-identity.md#identity-and-authority) separates accountable actor, caller and original creator, prohibits human impersonation from a BFF bearer alone, and requires current actor grants and caller delegation on every operation. Existing [opaque credentials](../runtime-configuration.md) provide legacy principal-scoped permissions, not named federated identities.

This ADR concerns authentication and delegation. It does not reopen the UI architecture, select a pilot hostname, implement an IdP, or activate unfinished public operations. A login is neither a service grant nor baseline approval.

## Provider comparison

Official documentation and release metadata were checked on 2026-11-13. Versions below identify the research baseline, **not admitted deployment pins**. Operational burden is an architectural judgment; no performance, hosting-cost or availability measurements were run. Documentation pages are rolling; observed releases do not prove every described capability in those exact binaries.

| Provider | Fit and advantages | Costs, limits and recommendation |
|:--|:--|:--|
| **Keycloak — 26.8.2 observed** | Full identity service; supported same-realm standard token exchange. Apache-2.0. Can serve the local pilot without an existing corporate IdP. | Requires operating an identity service, TLS and a database. **Recommend** for an independently reproducible pilot. Its preview delegation-chain feature and deprecated exchange implementation are excluded. [Exchange](https://www.keycloak.org/securing-apps/token-exchange), [release](https://www.keycloak.org/2026/11/keycloak-2682-released), [license](https://github.com/keycloak/keycloak/blob/main/LICENSE.txt). |
| **Dex — v2.46.1 observed** | Apache-2.0 OIDC federation option with documented upstream-token exchange, including a GitHub Actions example. | Attractive when an upstream identity system already exists. Its documented machine/cross-client flow does not by itself establish the BFF caller/delegation profile Perfeng needs. That gap needs a separate qualification, and local identity lifecycle still needs an owner. **Do not select for this pilot.** [Flow](https://dexidp.io/docs/guides/token-exchange/), [release](https://github.com/dexidp/dex/releases/tag/v2.46.1), [license](https://github.com/dexidp/dex/blob/master/LICENSE). |
| **ZITADEL — v4.20.3 observed** | Documents token exchange with subject/actor tokens and JWT output; plausible when explicit actor chains or its organization model are priorities. | Current repository licensing is AGPL-3.0-only with exceptions; this requires separate distribution/operational review, not assuming Apache licensing or changing Perfeng's allowlist. Access-token exchange defaults to opaque output; JWT output needs explicit selection. **Credible alternative, not default** given admission and integration cost. [Exchange](https://zitadel.com/docs/guides/integrate/token-exchange), [release](https://github.com/zitadel/zitadel/releases/tag/v4.20.3), [licensing](https://github.com/zitadel/zitadel/blob/main/LICENSING.md). |

An existing corporate OIDC service could avoid another operated identity system. None was identified in the confirmed intent or workspace. Its exact access-token, client-attestation and delegation behavior is **unverified**; do not treat generic OIDC login support as equivalent to this trust profile. Reconsider before implementation if the owner already operates one.

Keycloak is a separate service, not a Java runtime embedded in the Go control plane or an SDK placed in Angular. Its [production guide](https://www.keycloak.org/server/configuration-production) makes TLS, hostname and administration isolation operational concerns. Those obligations remain even for a small shared pilot.

## Delegation models considered

| Model | Benefit | Tradeoff / decision |
|:--|:--|:--|
| **Shared BFF bearer plus actor headers** | Small apparent change. | BFF could select any person without that person's credential. Violates the existing contract. **Reject.** |
| **Relay a per-user access token through the BFF** | Few moving parts; browser can still use a server session. | A generic user token does not establish an approved BFF caller. Could work with a separately qualified client/presenter binding, but transfers more audience/issuance policy into that profile. **Alternative, not default.** |
| **Issuer-mediated audience-restricted exchange** | BFF supplies a real user credential and authenticates its own client to obtain a control-plane credential. | Additional registration, exchange availability and claim-profile tests. **Recommend supported standard exchange**, with the explicit limits below. RFC 8693 is the protocol reference, not proof that every provider implements identical delegation semantics. |
| **Explicit actor chain or Perfeng-issued assertion** | Can represent richer delegation or bind an exact request. | Keycloak's richer delegation feature is preview; a custom assertion would make Perfeng operate another signing/issuance boundary. **Defer** unless the pilot requires that stronger model. |

[RFC 8693](https://www.rfc-editor.org/rfc/rfc8693.html) distinguishes subject and actor. Perfeng's accountable HUMAN actor maps to the user's subject; an OAuth `act` claim, when present in a future admitted profile, normally identifies an intermediary. Do not equate the two vocabularies mechanically.

## Accepted trust profile

### Human and BFF path

1. Browser talks to the Console BFF using its protected same-origin session. BFF performs authorization-code login with PKCE S256, state and nonce validation, fixed redirect URIs and confidential-client authentication. Access/refresh tokens stay server-side. The browser necessarily receives a short-lived authorization response; it receives no OAuth bearer credential.
2. Use separate registrations: `perfeng-console-login` for interactive login and `perfeng-console-delegate` for exchange. Initial access tokens target the delegate, not the control plane. Only the delegate may exchange an authenticated user's token for `perfeng-control-plane` audience. Disable unrelated grant types on these clients; no password grant, arbitrary subject override, impersonation or admin credential.
3. Request a single control-plane audience and least necessary scopes. The BFF authenticates to the issuer with a separately managed private key; use a qualified confidential-client authentication method. Do not return an exchanged refresh token to the browser or create an offline impersonation capability.
4. Provider verifies the signed access-token profile and maps exact `(iss, sub)` to actor identity. Map the signed, allowlisted authorized-client claim to a namespaced caller ID such as `client:perfeng-console-delegate`. Keycloak examples show `azp` for the requesting client; this provider-specific mapping needs a real-token receipt before admission. It is **not** an RFC `act` chain or proof of the network presenter.
5. A provider-controlled enrollment binds subjects to HUMAN/CI kinds. The enrollment and current caller-delegation policy are operator-owned, default-deny and revocable; no user-editable profile field, email, display name or submitted header determines kind. Exact storage/administration changes need bounded implementation slices. Every service operation reads current policy and service grants rather than accepting token roles as authority.

This is issuer-attested client identity **at issuance**. A stolen exchanged bearer can be replayed until expiry or provider refusal. TLS and server-only custody are baseline controls, not sender binding. If the owner's risk bar requires cryptographic proof of the presenting BFF, this recommendation must add separately qualified mTLS/DPoP before admission; do not claim `azp` supplies it.

The two-client topology and inability of other grants to mint accepted human API tokens are qualification requirements, not facts established by reading documentation. Failure blocks enablement rather than permitting weaker checks.

### CI path

For the initial reproducible qualification, use a **separate Keycloak machine client per admitted CI trust boundary** with its own credentials and control-plane audience. Its service-account subject is the CI actor; the authenticated registered client is the caller. Human login/delegation and CI profiles must be disjoint. CI cannot assert HUMAN kind, choose a human `sub`, or inherit baseline-approval rights. Private signing keys stay in the CI secret store; short-lived access tokens do not eliminate that credential's rotation burden.

Prefer GitHub workload identity over a stored CI key as a subsequent, separately admitted profile. [GitHub's OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) exposes issuer, audience and repository/workflow claims. Require exact audience, immutable repository/owner IDs and reviewed workflow/ref/environment conditions; the workflow initiator is not the accountable HUMAN actor. Stable subject mapping must be reviewed, not inferred from repository names. Standard same-realm exchange is not external JWT federation; Keycloak's [JWT authorization grant](https://www.keycloak.org/securing-apps/jwt-authorization-grant) or a direct provider CI verifier would need their own qualification. Neither is implicitly enabled here.

### Verifier, authorization and revocation rules

- Pin allowed issuers, audiences, client profiles, algorithms and HTTPS key endpoints in trusted configuration. Never discover a destination from an unverified token, `jku`, `x5u`, actor header or service ID. Bound response sizes, timeouts, per-issuer refresh rates, negative-cache size and refresh concurrency; no redirects or arbitrary issuer fetching.
- Require valid signature, nonempty subject, expiration and the admitted access-token type. Validate `nbf` when present, issuance age and a bounded clock skew. Reject ID tokens on the human API profile, algorithm confusion, unknown keys and malformed/duplicate claims. Never fall back to legacy auth after JWT-profile rejection. See [JWT BCP](https://www.rfc-editor.org/rfc/rfc8725.html).
- Accepted policy bounds: maximum API token age/lifetime **5 minutes**, maximum clock skew **30 seconds**, and maximum signing-key cache freshness **5 minutes**. Refresh missing keys once within a bounded request budget; valid cached keys may serve only within that freshness bound. Afterward unavailable trust returns a safe unavailable result. These are owner-approved design bounds, not measured capacities.
- Verify current subject enrollment/caller delegation and service grants on every operation. Baseline approval remains independent. Logout destroys the BFF session and server token state. Offline signature validation cannot promise instant IdP account/client-disable or exchanged-token revocation: Keycloak documents that access-token exchange does not propagate a revocation chain. Use provider-side deny/revocation controls for immediate authorization refusal, and test them independently from token expiry.
- Audit actor, caller, service, target, outcome and correlation with the original CI creator. Never retain bearer/refresh tokens or raw assertions in audit. An ID-token/email change cannot silently relink an identity or recover another user's grants.

Local JWT verification avoids per-request issuer introspection, at the cost of bounded rather than instant upstream token/account revocation. Introspection is an alternative with a hot-path availability/latency cost; it does not by itself establish revocation of a previously exchanged token when its parent is revoked. Provider-side current authorization remains mandatory in either model.

Login, session and token lifecycle controls follow [OAuth security BCP](https://www.rfc-editor.org/rfc/rfc9700.html). Invalid credentials, insufficient service access and unavailable trust must preserve the existing API's appropriate denial/unavailability semantics; do not leak resource existence or fabricate user data.

## Repository ownership, rollout and rollback

| Owner | Follow-up after acceptance; none applied here |
|:--|:--|
| `perfeng-control-plane` | PR58 verifier, actor/caller model and revocable trust policy; preserve legacy principal isolation. PR45 consumes verified identity for current grants; PR48/51 expose qualified comparison/rerun paths later. Do not OR legacy ownership with new service rights. |
| `perfeng-console` | PR52 server-side login/session/token lifecycle and exchange, then the authenticated CI-link journey. Keep existing transport admission, CSP, CSRF and independent product/verifier boundaries. |
| `perfeng-environment` | Own the digest-pinned IdP/realm/client composition, isolated database, TLS/hostname, secret delivery, backup/restore and pilot connectivity. No infrastructure or secret is provisioned by this ADR. |
| `perfeng-contracts` | Existing actor/caller model already leaves concrete trust protocol to the provider. No contract change assumed; a discovered public-wire gap gets a separate reviewed contract change before consumer use. |

Admit a specific supported release and immutable image digest under the environment's policy; admit Go/Node libraries under their owning repositories' policies. Research versions are not pins or security clearance. ZITADEL licensing would require explicit review; do not widen license gates. No production host, certificate, realm name or issuer URL is silently selected. The eventual external issuer URL is an identity namespace: changing it or recreating users/clients requires reviewed migration, not email matching.

Keep new mode off until provider and BFF conformance pass together. Implementation must wait for owner review and merge of the documentation-only PR. Subsequent implementation requires its scoped workflow; acceptance does not authorize deployment or public rerun activation. Rollback disables new entry points and preserves grants/audit/lineage; it must not convert denied service requests into legacy owner access. Identity rollback may require users to sign in again; it must not relabel actors.

## Admission and refutation gates

No provider was deployed and no real credential was minted for this ADR. Before accepting implementation as qualified:

1. Obtain redacted real-token receipts from the exact pinned realm/client configuration. Prove `sub`, client identity, token kind, audience narrowing and subject-kind enrollment for human, delegate and CI. Prove disabled alternative grants cannot mint an accepted profile. Failure means **not admitted**.
2. Reject forged actor/service headers, wrong issuer/audience/client/type/subject kind, expired/future tokens, unknown key, malicious key URL and claim substitution. Test real key rotation, stale cache, IdP outage, refresh failure and concurrent refresh bounds.
3. Demonstrate provider-side subject/caller and service-grant revocation on the next operation, separately from the bounded lifetime of an offline-verified IdP token. Test that Maya's rerun permission never grants baseline approval.
4. Verify real-browser login/callback/session fixation/CSRF/logout; search browser storage, responses, assets, URLs and logs for token canaries. Session recovery and unavailable backends must not resurrect authority or sample evidence.
5. Preserve legacy behavior and prove failed new-mode verification cannot downgrade. Then qualify Maya's real CI link → comparison → audited rerun, including response loss and retry. Existing synthetic PR59j evidence is not this receipt.

**Strongest argument against this recommendation:** two clients, a stateful IdP, caller policy and CI keys increase maintenance for a single operator. An already-operated corporate issuer with an adequate delegated-access profile could be cheaper. Unknown hosting, capacity and real-token behavior prevent claiming that Keycloak is operationally qualified. The rationale for choosing it now is an independently reproducible identity boundary with no preview-feature dependency, not minimum component count.

## Accepted owner decisions

The repository owner approved all four recommended defaults on 2026-11-13:

1. **Keycloak and supported two-client exchange** are the pilot default, subject to the admission gates above. No preview delegation feature is selected.
2. **Issuer-attested bearer caller identity plus current provider policy** is accepted for the pilot, including its documented replay and bounded upstream-revocation risk. Sender-constrained mTLS/DPoP is not an initial pilot requirement; a stronger risk bar requires separate qualification before claiming that protection.
3. **Separate machine-client credentials for initial CI qualification**, followed by separately qualified secretless GitHub identity. Static CI credentials are not the final workload-identity design.
4. **Maximum API token age/lifetime 5 minutes, clock skew 30 seconds and signing-key cache freshness 5 minutes**. Validate these bounds under load and outage tests; do not silently raise them.

This records the architectural decision, not completion of issue #356. Exact hosting/issuer URL, release/library pins, runtime conformance and provider implementation remain follow-up gates. No application or infrastructure implementation begins until the documentation PR is reviewed and merged by the owner.
