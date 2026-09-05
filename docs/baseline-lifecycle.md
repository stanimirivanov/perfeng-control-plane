# Baseline lifecycle

`internal/baseline` is the control-plane domain model for the `baseline/v1`
contract. A record identifies one immutable software, workload, environment,
dataset and normalized-result combination. It does not copy artifact bytes or
make statistical decisions.

New records start at revision 1 in `CANDIDATE` with pending qualification. Every
transition checks the caller's observed revision and appends one actor, reason
and timestamp to the lifecycle. The permitted paths are:

~~~text
CANDIDATE -> QUALIFIED -> APPROVED -> RETIRED
         \-> RETIRED   \-> RETIRED
~~~

Qualification requires passed evidence with a sample count and maximum observed
coefficient of variation. A candidate may instead be retired with failed
evidence and at least one safe reason. Approval does not change qualification
evidence, and retired records cannot transition again.

The domain also enforces the contract's immutable identities and digest-pinned
software image, binds the normalized artifact to the source Run, rejects
timestamps the JSON contract cannot represent, and preserves lifecycle order.
Copies do not share dataset, qualification or lifecycle storage with their
source.

This package does not decide whether evidence passes. That decision belongs to
the analysis and policy boundary. It also does not select a baseline for a
report.

The PostgreSQL adapter stores baseline versions under the principal that owns
their source Run. Creation requires that Run to be `COMPLETED` and requires the
exact normalized artifact to be present in the immutable artifact registry.
Missing and cross-principal evidence are indistinguishable. A duplicate
`(principal, baseline ID, version)` returns `ErrConflict`; after an uncertain
commit, callers resolve the outcome with `GetBaseline` rather than inventing a
new version.

Lifecycle mutations lock the current baseline row, read the database clock and
apply the expected revision before updating the relational revision, state and
JSON snapshot together. Database constraints also bind the snapshot to its
principal-owned source Run and registered artifact.

`ResolveApprovedBaseline` reads only the policy-pinned ID and version. It returns
a match only while that exact record is `APPROVED` and its test, workload,
environment definition and fingerprint, and dataset all match trusted candidate
dimensions. Missing, cross-principal, unapproved, retired and incompatible
records share the same empty result, so resolution neither leaks lifecycle state
nor invents a comparison anchor. There is no latest-version lookup or automatic
promotion. The [administration API](baseline-administration.md) exposes
exact-version creation, reads and revision-checked transitions with independent
operation permissions.
