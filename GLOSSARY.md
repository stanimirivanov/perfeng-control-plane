# Control-plane language

Terms for execution admission, durable state and authorized reads. Shared terms are defined in the [Perfeng glossary](https://github.com/stanimirivanov/perfeng-contracts/blob/main/GLOSSARY.md).

## Language

**Reviewed Registry Binding**
An explicitly reviewed pairing of a Test ID and exact Performance Policy with the eligible catalogue, workload, environment, producers and principals.

**Run Revision**
The sequence of durable lifecycle snapshots for one Run. A state change is conditional on its expected revision.

**Execution Claim**
Fenced authority to perform work for a Run phase under a lease.

**Artifact Registration**
A durable association of an Artifact Reference with its Run and authorized producer.

**Report-Time Binding**
The retained identities and artifacts actually used to produce a Run’s Analysis Report, including the exact baseline selection where applicable.

**Baseline Approval**
An attributable lifecycle decision admitting a Baseline Version for eligible new selection. Historical reads retain the original selection after retirement.

**Service Grant**
Current scoped authority over a Service. The grant applies independently of the caller’s possession of a Run link or artifact identifier.

**Sealed Rerun Selection**
The frozen source-run inputs retained for an accepted Rerun. Dispatch requires the applicable current eligibility and execution claim.
