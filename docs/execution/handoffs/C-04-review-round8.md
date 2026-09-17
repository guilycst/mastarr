# C-04 independent review round eight

## Decision

**Approved.** Product `b031416f27e6819d454d8ec419e7a4ffa4e8eb12`
closes the remaining R3g-c recovery-owner gap. With durable idempotency
persistence attached, the built-in configuration recovery owner cannot be
removed, the effective callback and readiness flag remain consistent, and
late persistence cannot attach after an owner was removed. Concurrent owner
removal and persistence attachment expose only the two safe outcomes.

The prior attempt-bound SQLite marker protection remains intact. Markerless or
foreign evidence stays pending, exact evidence can recover the bound mutation,
and the correction introduces no alternate dispatch or recovery path. No P1 or
P2 finding remains in the reviewed scope.

## Reviewed identity and scope

- Product: `b031416f27e6819d454d8ec419e7a4ffa4e8eb12`
- Product parent: `a68e396d8d15923dd0bac5e3fa35d6446063d314`
- Product tree: `f87aca3cbdf80a6145e2b0c655dd8aa616f3041b`
- Handoff commit: `87d49e7cfee20fab62128b4f19e2d5a4fde76102`
- Coordinator/state checkpoint, inspected only:
  `42e287cb8187616d88d012c126882a5251cd769b`
- Product diff SHA-256 for `internal/transport/` and `cmd/mastarr/`:
  `a101b8945b75db9b15d951810be3ffaaf85fc2d61a7997f824f9c998e45cc358`
- Product archive SHA-256 for those paths:
  `21de2ce4f8308f68df7612dc3b29559146fe34dbe331a76d45fabb5cd3c6ae57`
- Handoff file SHA-256:
  `c6404a2ce69015393387d125b202f0250ad1e3593dc25229f577f322e82b1c03`
- Product changes are confined to `internal/transport/server.go` and
  `internal/transport/server_test.go`. No schema, migration, OpenAPI,
  generated output, client module, `go.work`, or local `replace` change is
  present.

Review used synthetic IDs, endpoints and temporary stores. No live service,
private coordinate, media data or credential was used. Independent temporary
probe sources were removed before this receipt.

## Correction review

### Built-in owner removal now fails closed

`SetIdempotencyRecovery(nil)` evaluates
`requiresDurableMutationRecovery` while holding the assembly write lock. The
predicate counts either an injected mutation dependency or attached durable
persistence. A configuration-only server with persistence therefore returns
`ErrIdempotencyRecoveryRequired` before changing the callback or readiness
state.

The exact round-seven probe now passes: a server constructed with complete
durable persistence starts with `recoverConfigurationIdempotency`, rejects
nil owner removal, retains a non-nil callback, and keeps the recovery-ready bit
true. The product test covers the same no-injected-dependency boundary and a
repeated concurrent removal matrix.

### Ownerless late persistence is rejected atomically

A server may remove its owner before storage is attached because no durable
reservation can then outlive the process. `SetIdempotencyPersistence` now
checks the effective callback under the same assembly write lock before
publishing a non-nil store. An ownerless server returns
`ErrIdempotencyRecoveryRequired` and retains the previous nil persistence.
Installing a callback first allows the store to attach; a later removal is
then rejected without changing either half of the pair.

The independent lifecycle probe exercised this complete sequence, including
detaching persistence and returning to an ownerless non-durable state. It
passed 20 ordinary and 10 race repetitions. Probe source SHA-256:
`b0084ef0650db162dfab6a40dfa9e2dc7113c98ccee9659eccb56fd8ab1509e9`.

### Concurrent transitions expose no ownerless durable state

An independent probe raced `SetIdempotencyRecovery(nil)` against
`SetIdempotencyPersistence(store)` from a configuration-only server with its
built-in owner. Across 100 inner races per test iteration, only these outcomes
were accepted:

1. Persistence attaches first; owner removal fails and the durable store plus
   non-nil owner remain ready.
2. Owner removal completes first; persistence attachment fails and the server
   remains ownerless only while no durable store is attached.

The probe passed 20 ordinary and 10 race repetitions. Probe source SHA-256:
`d1ca14ecdba158323c7cb8a8e2a31f50efec684a2ea2cc1b07c3d9bae3111bf6`.
The assembly read lock also continues to keep the pair stable for the full HTTP
request. The policy now checks both readiness and callback presence before a
durable mutation can dispatch.

### Prior marker and startup invariants remain closed

The focused matrix retained the real-SQLite tests for rejected stale PATCH,
duplicate CREATE, marker outage, exact versus foreign effect evidence and
fresh-manager recovery. Markerless or mismatched attempts cannot recover as
success, exact attempt-bound evidence remains required, and no blind
redispatch path was introduced.

Inspection and the full regression suite found no change to configuration
rollback/reload, managed credential comparison and zeroization, strict
idempotency digest binding, ETag replay, origin/CORS handling, generated route
delegation, bounded decoding, error sanitization or readiness behavior.

## Checks

- Exact ancestry, product tree, owned diff and handoff identity: passed.
- Product and handoff `git show --check`: passed.
- Focused owner, setter and marker-outage matrix, ordinary `-count=20`:
  passed; transport `0.473s`, process package `3.404s`.
- Focused owner, setter and marker-outage matrix, `-race -count=10`:
  passed; transport `1.549s`, process package `30.804s`.
- Independent complete owner lifecycle probe: passed in the same 20 ordinary
  and 10 race repetitions.
- Independent concurrent owner-removal/persistence-attachment probe: passed
  20 ordinary repetitions in `0.433s` and 10 race repetitions in `1.598s`.
- Full owned packages, ordinary `-count=5`: passed; transport `0.447s`,
  process package `11.144s`.
- Full owned packages, `-race -count=3`: passed; transport `1.754s`,
  process package `39.382s`.
- `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport ./cmd/mastarr`:
  passed.
- `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify`: passed; all modules
  verified.
- Linux amd64 and arm64 CGO-free test-binary compilation for
  `./cmd/mastarr`: passed.
- `python3 scripts/check-architecture.py`: passed.
- `python3 scripts/check_planning.py`: passed; 53 tasks, 60 acceptance cases,
  local links resolved.
- Full offline
  `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci`:
  exit 0 with final marker `guardrail checks passed (ci)`. Generation,
  staged generation, all OpenAPI/Vacuum checks, lint, architecture,
  ordinary/race tests, vet, module verification and Linux cross-builds
  passed. Root storage race completed in `199.284s`.

## Acceptance disposition

- A-38 and A-39: accepted. Configuration mutation, revision preconditions,
  replay and exact read-back behavior remain green.
- A-42: accepted. No secret, private coordinate or request material was added
  to owner state or persisted effect markers.
- A-46 and A-47: accepted. Authentication scope, origin and CORS behavior did
  not change.
- A-56: accepted for C-04. Durable owner assembly, restart reconciliation,
  exact effect evidence and no-blind-redispatch behavior now fail closed across
  constructor, setter and concurrent-transition paths.

## Integration recommendation

Integrate this receipt and mark C-04 complete for its v0.0.1 contribution. Keep
the built-in owner lifecycle and concurrent setter probes in the transport
regression suite. Release, deployment and live-stack verification remain
separate gates.
