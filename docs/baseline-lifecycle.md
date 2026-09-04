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
the analysis and policy boundary. It also does not persist records or select a
baseline for a report. A later storage adapter must serialize mutations, enforce
expected revisions transactionally, and preserve the append-only lifecycle.
