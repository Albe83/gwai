# ADR 0008: Server-rendered administrative WebUI

- Status: Accepted
- Date: 2026-07-12

## Context

Operators need a browser interface for user, provider and virtual-key lifecycle
management. The domain APIs are deliberately split across two independently
deployed control planes and both accept a powerful bearer token. A browser-only
single-page application would either hold that token or require a separate BFF
anyway. It would also introduce CORS configuration and a second build ecosystem
to a monorepo whose runtime currently uses only the Go standard library.

AngularJS is not a suitable foundation: upstream support ended in January
2022. Adopting an end-of-life framework for a security-sensitive admin
surface would create avoidable maintenance and vulnerability risk. Modern
Angular, React and Vue remain valid choices for interaction-heavy products, but
their Node package graph and client state model are not justified by the
current forms-and-tables workflow.

## Decision

Add an independently deployable `gwai-admin-webui` service implemented with Go
standard-library `net/http`, `html/template` and embedded local assets. It is a
server-rendered backend-for-frontend, not a new domain service:

- users and providers remain owned by `gwai-control-plane`;
- virtual keys remain owned by `gwai-virtual-key-control-plane`;
- the WebUI owns only short-lived browser sessions and CSRF state;
- the WebUI has no State Store scope and is absent from the inference path.

The login form accepts the existing admin token over a trusted TLS or loopback
connection. Before authentication, the backend uses a short-lived signed login
challenge keyed by an independent process-local random secret rather than
allocating server-side state. It verifies the token
without placing it in JavaScript, URLs or browser storage, then issues an
opaque, expiring `HttpOnly` and `SameSite=Strict` session cookie. HTTPS
deployments also enable its `Secure` flag and HSTS; the non-secure chart option
exists only for loopback port-forwarding. State-changing forms require an
unpredictable CSRF token bound to that session. Key creation additionally uses
a single-use operation token to reject a repeated form submission. Logout
invalidates the session.

Templates use contextual auto-escaping. Assets are compiled into the binary
and make no external requests. Responses apply a restrictive Content Security
Policy, deny framing and MIME sniffing, suppress referrers, and set
`Cache-Control: no-store`. A newly generated virtual key is shown only in the
immediate creation result and is not retained or logged by the WebUI.

Edit, status and delete-confirmation forms carry the control-plane resource's
strong `ETag`; the WebUI sends it in `If-Match` so a stale form conflicts
instead of changing or deleting a concurrently updated resource. If delivery
of a virtual-key creation response is ambiguous, the consumed operation token
is not replaced on the error page; the operator must inspect and remove any
matching key before deliberately retrying. The BFF sends every mutating Dapr
invocation with unknown content length, making it a streaming request for which
Dapr does not apply automatic service-invocation retries. Read-only calls
retain their normal retryable bounded requests. If a mutating response is
nevertheless lost after dispatch, the UI renders a non-actionable
"outcome unknown" page and requires inspection of current state before retry.

The Helm chart deploys one non-root, read-only, distroless replica behind a
`ClusterIP` Service. Loopback port-forwarding is the local access path;
production operators must add TLS termination before broader exposure.
Its termination grace exceeds the bounded request lifetime so an in-flight
one-time key response can finish during a rollout.

## Consequences

- The admin bearer token stays out of client-side application state and CORS is
  unnecessary.
- The WebUI builds, tests and ships with the existing Go and OCI toolchain and
  works without Internet access or a JavaScript package registry.
- Server-rendered forms are straightforward to exercise with Go handler tests
  and deterministic `curl` E2E probes.
- A WebUI restart can invalidate in-memory sessions, requiring administrators
  to sign in again; this is an acceptable fail-closed behavior.
- Rich client-side interaction is intentionally limited. If future workflows
  require substantial client state, a maintained framework can be reconsidered
  behind the same BFF boundary without moving the admin credential to the
  browser.
