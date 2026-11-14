# Real Keycloak token conformance

## TL;DR

- Issue #356 / M05-PR58g/h qualifies real tokens, rotation and unavailable-key trust through Go.
- The fixture is pinned by commit plus per-file SHA-256, not the sibling working tree.
- Tokens move only over a private process pipe; retain only a redacted receipt.
- Current PostgreSQL policy is rechecked; public identity serving remains disabled.

## Explicit gate and prerequisites

Use Linux amd64, UID 1000, local Unix-socket Docker, Git, uv, the pinned Go
version and an authorized disposable loopback PostgreSQL instance. The official
Keycloak image must already be acquired at the digest in the pinned environment
fixture lock. The exact Python 3.12/uv.lock graph must be cached for offline use.
The test performs no downloads and never uses an ambient cluster context.

```sh
PERFENG_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55456/postgres?sslmode=disable' \
PERFENG_ENVIRONMENT_SOURCE=/path/to/perfeng-environment \
GOTOOLCHAIN=go1.26.11 make identity-qualification
```

Use only a database created for this task; never supply a shared/production URL.
The existing test helper creates and removes a random child database, leaving
its supplied admin database intact. Ordinary tests and `make db-validate` do not
launch Keycloak; only this explicit target selects the separate live test.
Missing required inputs are errors, not successful skipped qualification.

The [fixture pin](../internal/postgres/testdata/keycloak-fixture.json) selects
[environment commit dbf12dc](https://github.com/stanimirivanov/perfeng-environment/commit/dbf12dcdfda9d2a42497aeba9aeae8d34a779ee8).
Only seven checksum-verified Git blobs are staged into a private temporary directory.
If the Python cache is cold, explicitly bootstrap that pinned dependency graph
first: extract `.python-version`, `pyproject.toml` and `uv.lock` from that commit
to a private temporary directory, run `uv sync --locked --project` against it,
then remove the directory. This acquires the lock's exact hashed packages without
changing a repository virtual environment. Do not remove `--offline` from the gate
or substitute a newer lock/source revision to repair a cache failure.

## Trust, lifecycle and proof

The test-only observer calls every original fixture verifier and protocol check.
It pauses at successful delegated-token verification, before destructive code
replay. Live CI, initial human, ID and delegated tokens plus public TLS trust
travel directly to Go in a bounded private pipe. They never enter argv, stderr,
files or retained test output. Expectations are independently declared in the Go
test: exact configured actor, caller, kind, issuer and API audience.

Go composes the existing fixed-origin JWKS fetcher, bounded key cache, JWT verifier,
admission and current PostgreSQL policy. It tests positive identities, rejected
initial audience/ID token/tampering, enrollment and delegation disable/restore,
kind mismatch, no inherited baseline authority and stopped-verifier refusal.
Instances are retired before a different profile is constructed for the same issuer.
The bridge preserves every original provider/protocol check. Private checkpoints
pause the real provider after overlap and after retirement. Go keeps the same
managed cache across those checkpoints: the new kid is refused within the
cooldown, admitted after a permitted refresh, and the old key is retained only
until a later qualified refresh observes its retirement. A deliberately invalid
unknown-kid probe triggers that refresh; no signed payload or expected identity
is rewritten. Invalid phase ordering and missing checkpoint tokens refuse the gate.

After retirement, stop that instance before constructing an independently fresh
outage verifier. It obtains actual current keys and verifies a fresh surviving CI
token. Only then does the bridge stop the exact owned issuer after checking its
label. Fresh known keys continue within the approved window. An unknown-key
refresh fails unavailable without evicting fresh known keys; trust older than
five minutes fails unavailable. The source fixture still performs final cleanup
before the redacted receipt is emitted.

The Go clock is controlled for the existing ten-second attempt interval and
five-minute freshness bound. It is not a five-minute wall-clock endurance test.
All token/key material and provider mutations remain real. Before interpreting
retirement/stale-trust refusal, assert the real token is still inside the
unchanged five-minute age/lifetime plus thirty-second tolerance. No production
limits are raised, timers slept through or token claims adjusted.

The child receives cancellation, and the parent has a fallback that removes only
the recorded random-name/label container and a strictly validated owned private
directory. Temporary staged source/venv and the random database are removed too.
The cached immutable image and downloaded package cache are retained. On failures,
no raw child diagnostics or token envelope are printed. A failed bridge may mean
missing offline dependencies, provider refusal, timeout or Go conformance failure;
fix prerequisites rather than weakening checks or logging credentials.

The receipt remains `NOT_ADMITTED`. Real-token conformance, real-key rotation
and unavailable-key freshness checks pass; BFF/browser/shared-pilot gates remain. This is not HTTP
serving authorization, an IdP account-revocation guarantee or transaction fencing
for a later service operation. Legacy credentials and public routes are unchanged.
Rollback removes this opt-in test/pin/bridge; no dependency, schema or production
runtime change is made. See accepted [ADR 0046](decisions/0046-select-issuer-and-bff-delegation-trust.md).
