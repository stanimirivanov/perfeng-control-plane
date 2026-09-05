# Baseline administration

The baseline API creates and administers exact, immutable baseline versions.
It does not choose a latest version, promote automatically, delete evidence or
decide whether measurements are statistically suitable.

All operations require one verified bearer credential. The authentication
adapter derives the stable principal and operation permissions from that
credential. The server records that principal as the lifecycle actor; request
bodies cannot supply or override an actor.

## Operations and permissions

| Operation | Permission | Successful response |
| --- | --- | --- |
| `POST /v1/baselines` | `CreateBaseline` | `201 Created` |
| `GET /v1/baselines/{id}/versions/{version}` | `ReadBaseline` | `200 OK` |
| `POST /v1/baselines/{id}/versions/{version}/transitions` | `TransitionBaseline` | `200 OK` |

Run permissions and baseline permissions are independent. Granting permission
to create a Run does not permit baseline creation or lifecycle decisions.
Missing or invalid credentials return `401 UNAUTHENTICATED`; an authenticated
principal without the required operation permission receives `403 FORBIDDEN`.

Baseline IDs use lowercase hyphen-separated resource identifiers. Versions are
exact three-part numeric versions such as `2.0.0`. Malformed paths return
`400 BAD_REQUEST`. Missing versions and versions owned by another principal are
both reported as `404 NOT_FOUND`.

## Create a candidate

Creation requires a completed Run owned by the principal and the exact
registered `normalized-result/v1` artifact from that Run. The body also fixes
the software, workload, environment fingerprint and dataset represented by the
new version. These values cannot be changed after creation.

The reviewed request example is embedded at
`internal/contract/snapshot/examples/baseline-create.json`. With that document
saved as `baseline-create.json`, a request is:

~~~sh
curl --fail-with-body \
  -H "Authorization: Bearer $PERFENG_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @baseline-create.json \
  https://perfeng.example/v1/baselines
~~~

A successful response contains revision one in `CANDIDATE` and includes a
relative `Location` header for the exact version:

~~~text
/v1/baselines/approved-search-browser/versions/2.0.0
~~~

Baseline creation is intentionally not idempotent. Repeating an ID and version
returns `409 BASELINE_EXISTS`, even when the document is identical. If the
client loses a response after a timeout, disconnect or `5xx`, it must read the
exact `Location` it can derive from the submitted ID and version. A successful
GET proves creation committed; `404` means the version is not visible to that
principal. Clients must not invent a new version merely because the create
response was uncertain.

The API does not accept `Idempotency-Key` as a baseline-creation mechanism.
Version identity and the exact-version recovery read provide the retry
contract.

## Read an exact version

~~~sh
curl --fail-with-body \
  -H "Authorization: Bearer $PERFENG_TOKEN" \
  https://perfeng.example/v1/baselines/approved-search-browser/versions/2.0.0
~~~

The returned record is the current consistent snapshot. `revision`, `state`,
`qualification` and the append-only `lifecycle` reflect every committed
decision. Immutable identity and evidence fields remain unchanged.

There is no collection or latest-version endpoint. Policies and automation must
pin the reviewed baseline ID and version explicitly.

## Qualify evidence

Only a `CANDIDATE` can become `QUALIFIED`. Qualification records evidence that
has already been evaluated by the analysis and policy boundary; this endpoint
does not calculate sample counts or variability.

~~~json
{
  "expectedRevision": 1,
  "state": "QUALIFIED",
  "qualification": {
    "status": "PASSED",
    "reasons": [],
    "sampleCount": 30,
    "maximumCv": 0.05
  },
  "reason": "Repeated measurements passed the reviewed stability policy."
}
~~~

Send the document to the exact version's `/transitions` child resource. On
success, revision becomes two and the server appends a `QUALIFIED` event with
the authenticated principal and the server's authoritative timestamp.

## Approve a qualified version

Approval is a separate human or policy-authorized decision. It preserves the
qualification evidence:

~~~json
{
  "expectedRevision": 2,
  "state": "APPROVED",
  "reason": "Approved as the known-good anchor for this environment class."
}
~~~

Only an exact approved version is eligible for policy selection. Approval does
not make it the implicit latest baseline.

## Retire a version

Any non-retired state can move forward to `RETIRED`. Retirement is terminal:

~~~json
{
  "expectedRevision": 3,
  "state": "RETIRED",
  "reason": "Superseded by a separately reviewed baseline version."
}
~~~

A candidate rejected during qualification may include failed evidence:

~~~json
{
  "expectedRevision": 1,
  "state": "RETIRED",
  "qualification": {
    "status": "FAILED",
    "reasons": ["Observed variability exceeded the reviewed policy."],
    "sampleCount": 30,
    "maximumCv": 0.21
  },
  "reason": "Candidate evidence was not stable enough for approval."
}
~~~

Failed qualification evidence is accepted only when retiring a candidate.

## Concurrency and error handling

Every transition must send the revision last observed by the client. A
concurrent committed decision makes that revision stale and returns
`409 REVISION_CONFLICT`. The client must GET the exact version, present the new
state to the decision maker and submit a new decision deliberately. It must not
silently replay the old decision against the newer revision.

An impossible lifecycle edge returns `409 BASELINE_TRANSITION_CONFLICT`.
Examples include approving a candidate directly, qualifying an approved
version, or changing a retired version. Schema or evidence validation failures
return `422 VALIDATION_FAILED`.

Request bodies must be one UTF-8 JSON document no larger than 64 KiB. Duplicate
keys and malformed JSON return `400 BAD_REQUEST`; unsupported media types or
content encodings return `415 BAD_REQUEST`; oversized bodies return
`413 BAD_REQUEST`. Backend unavailability returns `503 UNAVAILABLE` with a
`Retry-After` header. Internal diagnostics and credentials are never returned.

## Lifecycle ownership

The API is an administration boundary, not the measurement-quality engine.
Callers are responsible for presenting evidence produced by the trusted
analysis workflow and for granting transition permission only to the intended
automation or reviewers. The PostgreSQL repository serializes decisions and
persists the actor, reason, timestamp, state, revision and qualification in one
transaction.
