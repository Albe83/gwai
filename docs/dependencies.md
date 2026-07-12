# Dependency policy

Runtime Go code uses only the standard library. This keeps each service static,
small, and independent of provider or Dapr SDK release cycles.

The administrative WebUI follows the same constraint: `html/template` provides
context-aware escaping, `embed` packages its local CSS/JavaScript, and
`net/http` implements the BFF and session boundary. It loads no CDN resources
and introduces no Node/npm runtime or build dependency. The small local script
only progressively enhances server-rendered forms; lifecycle operations remain
owned by the existing Go control-plane APIs.

| Dependency | Scope | Why it exists | License / maintenance note |
| --- | --- | --- | --- |
| Go 1.26 | Build/runtime | Memory-safe static services and strong HTTP/JSON tooling | BSD-3-Clause; supported upstream release line |
| Dapr 1.18 | Platform | mTLS service invocation, state/secret abstractions, ACL and resiliency | Apache-2.0; CNCF project |
| Valkey 9.1.0 | Local state backend | Transactions and ETags for three scoped Dapr state components | BSD-3-Clause; Linux Foundation project; exact image tag pinned |
| Helm 3 | Packaging | Reproducible k3s/Kubernetes deployment | Apache-2.0 |
| Kubernetes Gateway API v1 | Optional platform API | Expose the administrative WebUI through an existing HTTPS Gateway | Apache-2.0; CRDs and a conformant controller are operator-managed |
| Distroless static Debian 13 | Runtime image | CA certificates and a minimal non-root image with no shell/package manager | Maintained by GoogleContainerTools; image tag is explicit |
| curl and jq | Development only | Provisioning and smoke-test assertions | Not shipped in service images |

Valkey is used through Dapr's tested Redis-compatible component; gwai does not
link a Valkey client. The control, provider and virtual-key components use
separate logical databases and application scopes. The bundled instance
receives a generated, upgrade-stable password and an explicit logical-database
count. A production deployment can set
`valkey.enabled=false`, configure `valkey.host`, TLS, and a password Secret, and
point all three Dapr components at a managed compatible endpoint.
