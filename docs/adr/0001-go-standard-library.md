# ADR 0001: Go services with a standard-library runtime

- Status: accepted
- Date: 2026-07-11

## Decision

Implement independently deployable Go services in one module. Use the standard
library for HTTP, JSON, cryptography, logging, and shutdown. Access Dapr through
its HTTP building-block APIs instead of importing an SDK.

## Consequences

Service images are static and the dependency/vulnerability surface is small.
Wire behavior remains visible in code. We own HTTP retry, error normalization,
and union-type decoding that an SDK might otherwise provide.

