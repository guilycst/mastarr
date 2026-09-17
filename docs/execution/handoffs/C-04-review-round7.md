# C-04 independent review round seven

## Decision

**Changes requested.** Product
`23462380b004586c1e09794c937ae396ee18b394` closes the marker-outage
false-success path and serializes setter changes with in-flight requests.
Attempt-bound configuration effects are written in the same SQLite
transaction, exact and foreign marker evidence is distinguished, and the
injected-dependency setter race is fenced.

One P1 recovery-owner gap remains. A server using the built-in configuration
handlers can attach durable idempotency persistence and then successfully call
`SetIdempotencyRecovery(nil)`. The setter removes the built-in configuration
recovery owner and leaves the readiness flag true because its mutation test
counts only injected route dependencies. Configuration mutations can therefore
continue to dispatch into a durable reservation protocol that has no owner for
a lost completion. The full offline guardrail passes because the product
setter test covers only an injected mutation dependency.

## Reviewed identity and scope

- Product: `23462380b004586c1e09794c937ae396ee18b394`
- Product parent: `720c1c10a9c7cad550e353b8798380664c10b7a7`
- Product tree: `3c9850c596d43fd954ca1acfa1eec24af3c978b4`
- Handoff commit: `911e67ac291f7e096eea46f9210a40232eb3fd21`
- Coordinator/state checkpoint, inspected only:
  `f0153be9cd5d64d266a32a3c7b1d816112589fb5`
- Product diff SHA-256 for `internal/transport/` and `cmd/mastarr/`:
  `6f3e92699d8296582f748deff2167dd5a417790a93fe33f5267b6bc27d9cef78`
- Product archive SHA-256 for those paths:
  `f19e02d08ec042ee7710bddb87a39c5fdf0e9b45b03b72a78b25cb03b7a8634e`
- Handoff file SHA-256:
  `a44e9024e523f2e1e17efe885c1ca1d9710707071366b3ddd0f2a4a17839c691`
- Product changes are confined to the recorded six files under
  `internal/transport/` and `cmd/mastarr/`. No schema, migration, OpenAPI,
  generated output, client module, `go.work`, or local `replace` change is
  present.

Review used synthetic IDs, endpoints, credentials and temporary SQLite
databases. No live service, private coordinate, media data or credential was
used. Independent temporary probe sources were removed before this receipt.

## Closed correction findings

### R3f-d marker-outage safety is closed

Every built-in connection, storage-root and path-mapping create, patch and
retire transaction now appends an attempt-bound effect record in the same
SQLite transaction as the configuration change. The marker binds scope, key,
digest, attempt, operation, resource kind, resource ID and revision. SQLite
sets `RequireRecoveryEvidence`, so a markerless reservation stays pending.
The built-in recovery owner additionally requires exact marker identity and
exact read-back state before reconstructing a terminal HTTP response.

The producer matrix passed 20 ordinary and 10 race repetitions. An independent
real-SQLite probe then replaced only the loaded effect revision with a foreign
value while leaving the request, reservation and current resource unchanged.
The fresh request stayed `409 idempotency_pending`. Restoring the exact
SQLite record allowed one successful read-only recovery with the expected
ETag and no second mutation. That probe passed 20 ordinary and 10 race
repetitions. Probe source SHA-256:
`f5c4ea73f732124b0fddf54a76d528e14ad97643fc620789633a2c7fafd68e7f`.

The SQLite append-only trigger also rejected direct mutation of an existing
idempotency event during probe development. The final probe did not modify
product state and used a load wrapper to supply the foreign observation.

### R3g-b closes injected-owner publication races

`idempotencyAssemblyMu` keeps the persistence/owner pair stable through the
entire HTTP request. A late durable persistence setter rejects an incomplete
protocol and rejects an injected mutation dependency until an explicit owner
is installed. Removing that injected owner while persistence is attached
fails closed. A replacement owner is installed before readiness is published.

An independent concurrency probe blocked inside an injected mutation and
called `SetIdempotencyRecovery(nil)` concurrently. The setter could not
return while the request held the assembly read lock; after dispatch finished,
the setter returned `ErrIdempotencyRecoveryRequired` and retained the owner.
The probe passed 20 ordinary and 10 race repetitions with one dispatch.
Probe source SHA-256:
`329c1c3c38c63500ff1a1fc43a6c929481c7306ac76ba772c1c2a944fcfab64c`.

## Finding

### R3g-c — P1: built-in configuration recovery owner can still be removed

`SetIdempotencyRecovery` protects owner removal only when
`hasDurableMutationDependency(server.dependencies)` is true
(`internal/transport/server.go:289-318`). That predicate returns false for a
nil dependency table and inventories only injected handlers
(`internal/transport/routes.go:247-275`). Built-in connection, storage-root
and path-mapping mutations are deliberately omitted because their owner is
`recoverConfigurationIdempotency`.

The nil-owner branch consequently reaches
`internal/transport/server.go:312-317`: it stores
`recoveryOwnerReady=true`, replaces the callback with nil and returns
success. The policy check at `internal/transport/server.go:1516-1519` sees a
durable store and a true readiness bit, so it does not block subsequent
configuration mutations.

Independent reproduction:

1. Construct a server with no injected dependencies, a complete durable
   idempotency persistence, and readiness enabled. Construction installs the
   built-in configuration recovery owner.
2. Confirm `idempotencyRecoveryCallback()` is non-nil.
3. Call `SetIdempotencyRecovery(nil)`.
4. Require `ErrIdempotencyRecoveryRequired` and require the callback to
   remain installed.

The safety assertion failed 20/20 ordinary and 10/10 race repetitions:

```text
removing built-in recovery owner = <nil>, want durable mutation recovery owner is required
```

Probe source SHA-256:
`4f2204ad3a28826f12a47ab5acd4d4715086c086958608c696bfed3c60188a63`.

This is a durable liveness and restart-recovery failure, not a blind
redispatch: after a mutation commits and response completion is lost, the
attempt-bound marker remains pending but no production owner can reconcile it.
The setter contract must count the built-in configuration mutations whenever
durable persistence is attached, or preserve/restore the built-in owner when a
nil custom owner is requested. The persistence/owner readiness bit must never
remain true while the effective callback is nil.

## Checks

- Exact ancestry, product tree, owned diff and handoff identity: passed.
- Product and handoff `git show --check`: passed.
- Focused correction matrix, ordinary `-count=20`: passed; transport
  `0.517s`, process package `3.355s`.
- Focused correction matrix, `-race -count=10`: passed; transport
  `1.457s`, process package `30.450s`.
- Full owned packages, `-count=5`: passed; transport `0.309s`, process
  package `3.318s`.
- Full owned packages, `-race -count=3`: passed; transport `1.376s`,
  process package `36.376s`.
- Independent exact/foreign marker probe: passed 20 ordinary and 10 race
  repetitions.
- Independent setter/in-flight request probe: passed 20 ordinary and 10 race
  repetitions.
- Independent built-in owner-removal safety probe: failed 20/20 ordinary and
  10/10 race repetitions as described in R3g-c.
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
  passed. Root storage race completed in `198.657s`.

## Acceptance disposition

- A-38: accepted for the corrected transactional configuration and startup
  paths.
- A-39: accepted for exact conditional mutation and marker-bound replay.
- A-42: accepted. Effect markers contain identifiers and revisions only; no
  credential, request body, response body or endpoint leak was found.
- A-46 and A-47: accepted; no authentication, origin or CORS behavior changed.
- A-56: blocked by R3g-c. The mutable startup API can publish durable
  persistence without an effective production recovery owner.

## Integration recommendation

Do not mark C-04 complete. Preserve the exact marker-outage correction. Make
owner removal fail closed whenever durable persistence protects built-in
configuration mutations, retain the previous callback on rejection, and keep
the readiness bit derived from the effective callback. Add the no-injected-
dependency regression alongside the injected-dependency setter matrix, then
rerun focused ordinary/race tests and the full offline guardrail.
