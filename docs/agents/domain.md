# Domain docs

## Before exploring

- Read the repository's root `GLOSSARY.md` when it exists. Use its terms in issues, code, tests and design discussions.
- Read relevant accepted ADRs through the [decision index](../decisions/README.md). Surface conflicts explicitly and supersede accepted decisions through the repository's ADR process.
- For shared interchange terms, consult the [contracts repository](https://github.com/stanimirivanov/perfeng-contracts) and its glossary when present. For workspace composition, consult the [environment decisions](https://github.com/stanimirivanov/perfeng-environment/tree/main/docs/decisions).

## Where to record decisions and terms

This repository has one domain context. Add local terms to the root
`GLOSSARY.md` as they are agreed. Shared interchange vocabulary belongs in the
contracts repository. Cross-repo workspace composition decisions belong in the
environment repository. Each other decision belongs with the component that
owns it.

For domain-modeling skills, use `docs/decisions/` as the ADR directory.
Extend the existing ADR sequence and index using the repository's decision
rules. Create the glossary when there is a resolved term to record.
