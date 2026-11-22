# ADR-0004: Separate the native upload signing endpoint from artifact reads

- Status: Proposed
- Date: 2026-10-08
- Milestone: M03 — Native Browser Decision Loop
- Deciders: Repository owner and affected environment maintainers
- Supersedes: None
- Superseded by: None

## TL;DR

- Keep artifact reads and verification on the internal S3 endpoint.
- Optionally sign Native Worker uploads for a distinct, worker-reachable HTTPS S3 origin.
- Both origins must route to the same bucket and objects; the server never proxies the upload.
- An absent signing origin preserves existing behavior, including rejection of HTTP upload URLs.

## Context

The local control plane reads and verifies artifacts through an HTTP-only,
cluster-internal SeaweedFS Service. A native Windows worker cannot reach that
Service, and the Native Worker contract requires an HTTPS presigned PUT URL.
The current single-client composition signs for the internal endpoint, so
native upload grants fail closed. Replacing the read endpoint would force all
server-side artifact traffic through an external gateway and its TLS trust
boundary merely to make a worker URL reachable.

## Decision

Add an optional `PERFENG_S3_NATIVE_UPLOAD_ENDPOINT` origin. It is accepted only
as HTTPS with no credentials, path, query or fragment. The server continues to
read and verify through `PERFENG_S3_ENDPOINT`. When the new origin is set, a
separate S3 client using the same region, credentials and path-style policy
signs Native Worker PUTs for that origin. Presigning makes no network request;
the worker uploads directly to the gateway, and registration verifies the
object through the internal client. The configured origins must route to the
same bucket and exact object keys. An absent value retains the old client and
fail-closed HTTP behavior.

## Alternatives considered

### Route all server S3 traffic through the worker-facing gateway

- Benefits: One endpoint and client.
- Costs and risks: Existing reads depend on gateway availability and TLS trust;
  local failure and attack surfaces increase.
- Reason not selected: Reads need not leave the cluster to support native uploads.

### Rewrite a presigned URL after signing

- Benefits: No second client.
- Costs and risks: SigV4 signs the host; rewriting it invalidates authorization
  or tempts unsafe proxy behavior.
- Reason not selected: The delivered host must be the host that was signed.

## Consequences

### Positive

- Native grants can use a Windows-reachable HTTPS host without changing
  existing artifact access or the durable object URI.

### Negative

- Deployment must operate and trust an HTTPS gateway and prove both origins
  address the same storage namespace.

### Neutral or follow-up

- No data, schema, API or lease migration is needed. The environment repository
  owns the gateway, certificate and selected endpoint value.
- This decision does not enable native acquisition or prove live connectivity.

## Compatibility and migration

The setting is optional. Existing deployments keep their current behavior.
For a local rollout, deploy and verify the worker-facing gateway, select its
HTTPS origin in the runtime configuration, restart with the new control-plane
image, then test a real signed PUT and internal read-back before enabling
acquisition. Rollback removes the setting and restores the prior image and
runtime configuration; no stored artifact identity changes.

## Security and operations

The gateway must preserve the signed host, path, query, method and headers,
provide a certificate trusted by the worker, restrict exposure to the intended
host network, and impose upload-size and request-time limits. Presigned URLs
and headers remain secret delivery material and must not enter logs or
persistent configuration. Endpoint validation is structural, not a live
reachability or certificate check. Failure to sign or verify remains a safe
unavailable outcome; an unverified object is never registered.

## Validation

Unit tests prove endpoint validation, redacted failures, separate client
composition and actual SDK presigning for the external HTTPS host without
network access. Environment acceptance must prove TLS trust, exact signed PUT,
object read-back and failure when the gateway is unavailable.
