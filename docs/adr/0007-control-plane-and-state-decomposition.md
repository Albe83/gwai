# ADR 0007: Control-plane and state decomposition

- Status: accepted; amended by ADR 0009
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

- `gwai-control-plane` owns users, Models and Providers;
- `gwai-virtual-key-control-plane` owns virtual keys plus user/Model subjects
  and reference indexes.

Use three Dapr state components:

- `gwai-control-state`, scoped only to the resource control plane, stores users;
- `gwai-provider-state`, scoped to the resource control plane, gateways and
  adapters, stores the Model/Provider routing and runtime catalog;
- `gwai-virtual-key-state`, scoped to the virtual-key service and gateways,
  stores virtual keys, hash lookups, per-user/per-Model key indexes and minimal
  user/Model subjects.

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

ADR 0009 applies the same ordered projection and ETag fencing protocol to
Models. Virtual keys contain a required non-empty set of Model IDs. Every key
mutation touches the referenced Model subjects, and Model deletion fences its
per-Model key index before removing the canonical provider-state record.

User deletion calls `POST /internal/v1/subjects/fence`. The virtual-key store
transaction checks the per-user index, writes a permanent deleted subject, and
uses the subject ETag to serialize against key mutation. Only then is the private
user removed. Dapr ACLs and a virtual-key-specific application token, separate
from adapter tokens, restrict all user/Model projection paths to the resource
control plane.

The public URL ownership is split: users, Models and Providers remain on the
resource service, while `/v1/virtual-keys` belongs to the virtual-key service.

## Consequences

Private user records are unavailable to gateways and adapters. Adapters receive
provider-state scope but construct only the narrow Provider runtime view;
gateways receive provider plus key authorization state.
Both administrative deployments can be absent without interrupting inference.
Provider CRUD that does not violate the local Provider-to-Model dependency
remains available without the key service, and virtual-key CRUD for already
synchronized subjects remains available without the resource service. User and
Model writes deliberately depend on subject synchronization/fencing.

No transaction crosses state components. User and Model lifecycle coordination
is a synchronous, fail-closed flow, so a partial failure can leave authorization
more restrictive than canonical state but cannot grant access that lifecycle
state intended to revoke.

The chart keeps both writer services at one replica and uses `Recreate` rollouts
until uniqueness checks use a distributed lock or backend-native constraints. Dapr component scopes do not
express read-versus-write permission; application code supplies that narrower
runtime boundary.

These are breaking 0.x state-layout changes. There is no automatic migration
from `gwai-state` or qualified-string virtual-key allowlists; upgrades require
a fresh installation or an explicit reset and reprovisioning of pre-release
users, Providers, Models and virtual keys.
