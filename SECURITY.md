# Security policy

## TL;DR

- Do not disclose suspected vulnerabilities in public issues or pull requests.
- Use GitHub private vulnerability reporting when available.
- If no private channel exists, request one without sensitive details.
- The project is pre-release; only the current default branch is supported.
- Test only systems, accounts and data you are authorized to use.

## Supported versions

The control plane has no supported production release. Security fixes target the
current default branch. Replace this section with an explicit supported-version
table and disclosure policy when stable releases begin.

## Reporting a vulnerability

Use the repository Security page's **Report a vulnerability** action when it is
available. If private reporting is unavailable, do not publish the report.
Request a private channel without naming the vulnerable component, exploit,
affected data or reproduction details.

Include the affected revision and configuration, impact and preconditions,
minimal reproduction, sanitized evidence, known mitigations, and prior
disclosure where applicable. Do not retain, alter or disclose accessed data
beyond what is necessary to demonstrate the issue safely.

## Triage and disclosure

Maintainers SHOULD acknowledge a private report, validate severity and scope,
and agree on communication before disclosure. Response times are best-effort
until a staffed process exists. Coordinate disclosure after a fix or effective
mitigation. Good-faith reports SHOULD be handled respectfully.

## Security expectations for contributions

Contributors MUST follow the authentication, authorization, untrusted-input,
secret-handling, dependency and verification requirements in
[CONTRIBUTING.md](CONTRIBUTING.md) and the
[engineering standards](docs/development/engineering-standards.md). Changes to
authentication, authorization, credentials, SQL, artifact access, Kubernetes
permissions, external execution or sensitive data MUST document their threat
and failure model and receive focused security review.
