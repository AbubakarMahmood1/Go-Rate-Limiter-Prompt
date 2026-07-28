# Security and trust boundaries

## Intended placement

The service is an admission-control component, not an authentication or authorization system. A trusted caller must derive `identifier` and `resource` from authenticated application context. Allowing an untrusted client to invent those values lets it choose a fresh budget.

## Policy selection

`api.allow_policy_overrides` is false by default. Keep it false for direct untrusted traffic. Enabling it permits callers to select configured algorithms and tiers, so it belongs only behind a trusted gateway or in demonstrations.

## Reset

`/v1/reset` is an operational control:

- absent `RESET_TOKEN`: endpoint behaves as disabled (`404`);
- configured token: requires `Authorization: Bearer <token>`;
- token must contain at least 16 bytes;
- token is accepted only from the environment, never YAML;
- comparison uses SHA-256 values and constant-time comparison.

Use a high-entropy secret, transport it over TLS at the deployment boundary, restrict network access, and rotate it like any other operational credential. The repository does not implement per-tenant reset authorization or an audit log.

## Input and resource bounds

JSON bodies are limited to 8 KiB. Identity components are valid UTF-8, control-free, trimmed, and capped at 256 bytes (`identifier`) and 512 bytes (`resource`). Unknown fields and additional JSON values are rejected. Store configuration limits counters to Redis's exact integer range and validates duration/retention arithmetic.

These controls reduce accidental or hostile state/cardinality growth but do not replace upstream request-size, connection, authentication, or network rate controls.

## Dependency failure

The service fails closed on store errors and decision timeouts. This protects the limited resource but means Redis availability is part of service availability. The supported Redis contract is one standalone endpoint; no automatic failover or Cluster behavior is claimed.

## Reporting

For a non-public assessment copy, report security findings privately to the repository owner rather than opening an issue containing exploit details. Include the affected commit, reproduction, impact, and whether the issue applies to memory, Redis, or both.
