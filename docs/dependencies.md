# Dependency policy

Runtime Go code uses only the standard library. This keeps each service static,
small, and independent of provider or Dapr SDK release cycles.

| Dependency | Scope | Why it exists | License / maintenance note |
| --- | --- | --- | --- |
| Go 1.26 | Build/runtime | Memory-safe static services and strong HTTP/JSON tooling | BSD-3-Clause; supported upstream release line |
| Dapr 1.18 | Platform | mTLS service invocation, state/secret abstractions, ACL and resiliency | Apache-2.0; CNCF project |
| Valkey 9.1.0 | Local state backend | Transactions and ETags through Dapr's Redis-protocol state component | BSD-3-Clause; Linux Foundation project; exact image tag pinned |
| Helm 3 | Packaging | Reproducible k3s/Kubernetes deployment | Apache-2.0 |
| Distroless static Debian 13 | Runtime image | CA certificates and a minimal non-root image with no shell/package manager | Maintained by GoogleContainerTools; image tag is explicit |
| curl and jq | Development only | Provisioning and smoke-test assertions | Not shipped in service images |

Valkey is used through Dapr's tested Redis-compatible component; gwai does not
link a Valkey client. The bundled instance receives a generated, upgrade-stable
password. A production deployment can set `valkey.enabled=false`, configure
`valkey.host`, TLS, and a password Secret, and point the Dapr component at a
managed compatible endpoint.
