# ADR 0007: Control-plane and state decomposition

- Status: accepted
- Date: 2026-07-12

## Context

The first runtime registry placed users, virtual keys and providers in one Dapr
state component and exposed all three lifecycles from one process. This removed
the control plane from inference, but every data-plane identity could see a
broader state domain than it needed. Virtual-key lifecycle also scaled and
failed together with unrelated provider administration.

Separating private users from runtime authorization creates one cross-domain
invariant: disabling or deleting a user must revoke its keys even though a
gateway cannot read the private user record. User deletion must also be atomic
with respect to concurrent key creation or owner reassignment.

## Decision

Decompose administration into two independently deployable services:

- `gwai-control-plane` owns users and providers;
- `gwai-virtual-key-control-plane` owns virtual keys and key-owner subjects.

Use three Dapr state components:

- `gwai-control-state`, scoped only to the resource control plane, stores users;
- `gwai-provider-state`, scoped to both control planes, gateways and adapters,
  stores provider routing/runtime configuration;
- `gwai-virtual-key-state`, scoped to the virtual-key service and gateways,
  stores virtual keys, hash lookups, per-user key indexes and minimal user
  subjects.

Each store has a single application writer. Gateways and adapters use narrower
runtime types and perform only direct reads. They never invoke a control-plane
service during inference.

The resource service sends revisioned `KeySubject` values to the virtual-key
service through Dapr `POST /internal/v1/subjects/sync`. Missing subjects and all
disabled or deleted subjects fail authorization closed. Disablement is
synchronized before changing private state; activation is synchronized after
it. Equal revisions are idempotent only for identical user/status/deletion
state; observation timestamps do not change semantic identity. Stale revisions
conflict.

User deletion calls `POST /internal/v1/subjects/fence`. The virtual-key store
transaction checks the per-user index, writes a permanent deleted subject, and
uses the subject ETag to serialize against key mutation. Only then is the private
user removed. Dapr ACLs and a virtual-key-specific application token, separate
from adapter tokens, restrict both internal paths to the resource control plane.

The public URL ownership is split without changing resource payloads: users and
providers remain on the resource service, while `/v1/virtual-keys` moves to the
virtual-key service.

## Consequences

Private user records are unavailable to gateways and adapters. Adapters receive
only provider state; gateways receive provider plus key authorization state.
Both administrative deployments can be absent without interrupting inference.
Provider CRUD remains available without the key service, and virtual-key CRUD
for already synchronized subjects remains available without the resource
service. User writes deliberately depend on subject synchronization.

No transaction crosses state components. User lifecycle is a synchronous,
fail-closed coordination flow, so a partial failure can leave authorization
more restrictive than private state but cannot grant access that private state
intended to revoke.

The chart keeps both writer services at one replica and uses `Recreate` rollouts
until uniqueness checks use a distributed lock or backend-native constraints. Dapr component scopes do not
express read-versus-write permission; application code supplies that narrower
runtime boundary.

This is a breaking 0.x state-layout change. There is no automatic migration
from `gwai-state`; upgrades require a fresh installation or an explicit reset
and reprovisioning of pre-release resources.
