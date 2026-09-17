# C-04 independent review round six

## Decision

**Changes requested.** Product `52f3654cd0a669dbc3dc59f5187bec828616c34e`
closes the ordinary rejected-response, ETag, managed-credential comparison and
constructor-time assembly cases. Two P1 durability gaps remain. A storage
outage that loses both the exact no-effect release and rejected-response
completion leaves the original reservation pending; a fresh process then
misclassifies the rejected stale PATCH as HTTP 200 and duplicate CREATE as HTTP
201. The new recovery-owner check can also be bypassed by attaching persistence
after construction, which is the startup pattern used by `cmd/mastarr`.

Generic tests and the full offline CI guardrail pass, but they do not exercise
either failure boundary. C-04 must remain open.

## Reviewed identity and scope

- Product: `52f3654cd0a669dbc3dc59f5187bec828616c34e`
- Product parent: `01f837a64ba4c48ff89a8a08962e703eababfdd6`
- Product tree: `f65c161cf18f5a91199b1a4cda20817112824e43`
- Handoff: `21c54c7a288eb984fd122f475c41df03fe6c6a23`
- Coordinator state checkpoint, inspected only: `0bf59e203011d03d058d4f25e9682f1c581251ae`
- Product diff SHA-256 for `internal/transport/` and `cmd/mastarr/`:
  `d16a298e43b1852e4326a090b72c8a4e9107c5246013a3a716b9cd00e1c78439`
- Product archive SHA-256 for those paths:
  `177dce4eb3e6e6ab88e4ac55e77d455c9c861b317e3dac84a37c33d8499ad5dd`
- Handoff file: `docs/execution/handoffs/C-04-correction-round5.md`; despite
  the filename, its title and content identify correction round six. File
  SHA-256: `67974d62097fbad9bfeaa98b1475348607f508f53fb3bacf4ce5bd814468f237`.
- Reviewed product files changed only under the assigned paths:
  `cmd/mastarr/main_test.go`, `cmd/mastarr/persistence.go`,
  `internal/transport/idempotency_recovery.go`,
  `internal/transport/routes.go`, `internal/transport/server.go`, and
  `internal/transport/server_test.go`.
- No schema, migration, OpenAPI, generated output, client module, `go.work` or
  local `replace` change was present.

Review used synthetic endpoints, IDs, credentials and temporary SQLite files.
No live service, real media, private coordinate or credential was used. The
temporary probe sources were removed before this receipt.

## Closed parts of the prior findings

### R3f-c closed: ETag survives replay

Transport and SQLite persistence now normalize `ETag` explicitly rather than
letting `net/http` turn it into the non-whitelisted spelling `Etag`. Repeated
ordinary replay and successful recovery tests preserve the expected token.
The fresh-process successful PATCH cases for connection, storage root and path
mapping return the current revision and ETag. No unsafe header was added to the
replay allowlist.

### Managed credential comparison closes its narrow identity case

Connection recovery resolves only the bounded credential vocabulary, compares
the complete requested/current field set, zeroes each temporary byte slice and
keeps plaintext out of durable records and responses. Matching synthetic
credentials recover; missing, extra or mismatched credentials remain pending.
Inspection and repeated tests found no response, error or durable-record
plaintext leak.

### Ordinary rejected responses remain rejected

When SQLite can persist the exact release, built-in configuration `412
precondition_failed` and `409 configuration_conflict` outcomes are released
before completion. Repeating the same digest reserves a new attempt and reaches
the same rejection. Injected configuration handlers are excluded. The public
real-SQLite/fresh-manager regression passes repeatedly for this healthy-marker
path.

### Constructor-time owner gate works

`transport.New` rejects a durable mutating dependency when both persistence is
present and no explicit recovery callback was supplied. Read dependencies and
built-in configuration-only assembly remain accepted. The mutation inventory
matches all 20 mutating operations in the current OpenAPI contract.

## Findings

### R3f-d — P1: losing both no-effect markers still recovers rejection as success

The correction makes safe handling depend on
`releaseCurrentIdempotency(...) == nil` at
`internal/transport/server.go:1573`. If the exact release write fails, policy
falls through to normal completion persistence. When both writes fail during
the same storage outage, the pre-handler reservation remains the only durable
event. A fresh process passes that indistinguishable reservation to the
configuration recovery owner.

Recovery still proves CREATE and PATCH from coincident visible fields rather
than attempt-bound effect evidence:

- connection CREATE: `internal/transport/idempotency_recovery.go:37-52` and
  `:229-237`;
- connection PATCH: `internal/transport/idempotency_recovery.go:53-71` and
  `:239-249`;
- equivalent storage-root and path-mapping branches use the same model.

Independent real-SQLite reproduction:

1. Reserve through the production SQLite persistence, create connection R1,
   and advance it to R2 with label `after`.
2. Submit PATCH label `after` with stale R1 and a new key while wrapping only
   `Release` and `Complete` to return a synthetic storage error. The built-in
   handler rejects the request, but the client sees sanitized HTTP 503 because
   neither terminal marker can be stored.
3. Submit an identical CREATE for the existing R2 resource under another key
   with the same marker outage. It also returns sanitized HTTP 503 and leaves a
   reservation.
4. Load API state from the real SQLite database, construct a fresh manager and
   fresh server, restore the production SQLite persistence, and repeat both
   exact requests.
5. The rejected stale PATCH returns **200** with the current resource and ETag;
   the rejected duplicate CREATE returns **201** with current resource, ETag
   and Location. No mutation caused either current state.

The initial safety assertion failed with both responses. An inverted regression
detector then reproduced both outcomes 20/20 ordinary and 10/10 race
iterations. Probe source SHA-256 was
`66a2dd4d87e8431ac59eddea4487e0ed2eecb1ab717b1f354639a2e7dac0bcec`.

This is the same false-success class as prior R3f-a/R3f-b under the realistic
case where the durable store becomes unavailable after Reserve. A release is a
valid terminal representation only after it is durable. Without an
attempt-bound effect/result marker, fresh recovery must keep this reservation
pending rather than infer success from current fields.

### R3g-b — P1: mutable startup setters bypass the recovery-owner invariant

The owner check exists only in `transport.New` at
`internal/transport/server.go:209-211`. Production startup deliberately creates
the HTTP server before storage is ready, then calls
`SetIdempotencyPersistence` at `cmd/mastarr/main.go:255`. The setter at
`internal/transport/server.go:385-391` neither validates the full reservation
protocol nor checks `hasDurableMutationDependency` against the active recovery
owner.

Independent probe:

1. Construct a server with an assembled `CreateActionPlan` dependency and no
   persistence or recovery callback. Construction succeeds.
2. Attach a complete durable test persistence through
   `SetIdempotencyPersistence`.
3. The store is accepted although the only callback is the built-in
   configuration owner, which cannot reconcile this action-plan mutation.

The bypass reproduced 20/20 ordinary and 10/10 race iterations. Probe source
SHA-256 was
`d43257c6c06508857ee59d469dc1756fecca304d5ec86f826a5df66adb67bb47`.
`SetIdempotencyRecovery(nil)` can likewise remove an owner after the
constructor check. A non-nil callback that always returns unresolved also
satisfies the constructor check; the product test uses that shape as its
positive case. The current command happens to leave nonconfiguration mutation
dependencies unassembled, so it does not dispatch them today, but the public
assembly boundary does not enforce the claimed invariant and will admit a
permanently pending route when those services are wired.

Persistence and recovery ownership need one atomic, validated assembly
transition. Late persistence must fail closed or keep the server unready until
every assembled mutation has a real terminal/reconciliation owner; subsequent
setter calls must not remove that invariant.

## Regression checks

- Focused correction matrix, ordinary `-count=20`: passed; transport `0.494s`,
  process package `3.953s`.
- Focused correction matrix with `-race -count=10`: passed; transport `1.521s`,
  process package `36.972s`.
- Full owned packages `-count=5`: passed; transport `0.322s`, process package
  `9.754s`.
- Full owned packages with `-race -count=3`: passed; transport `1.697s`,
  process package `39.376s`.
- Independent real-SQLite marker-outage probe: safety assertion failed with
  recovered 200/201; inverted detector passed 20 ordinary iterations
  (`cmd/mastarr 1.990s`) and 10 race iterations (`16.210s`).
- Independent late-persistence owner probe: safety assertion failed; inverted
  detector passed 20 ordinary iterations (`transport 0.436s`) and 10 race
  iterations (`1.435s`).
- `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport ./cmd/mastarr`:
  passed.
- `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify`: passed, `all modules
  verified`.
- Linux amd64 and arm64 CGO-free test-binary compilation for `./cmd/mastarr`:
  passed.
- `python3 scripts/check-architecture.py`: passed.
- `python3 scripts/check_planning.py`: passed; `53 tasks, 60 acceptance cases`,
  local links resolved.
- Product and handoff `git show --check`: passed. Review worktree was clean
  before writing this receipt.
- Full offline
  `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci`:
  exit 0 with final marker `guardrail checks passed (ci)`. Generation, staged
  generation, root and standalone Vacuum, lint, architecture, ordinary/race
  tests, vet, all module verification and Linux cross-build matrix passed.
  Root storage race completed in `209.982s`; trash race completed in `68.391s`.

## Acceptance disposition

- A-38: accepted for rollback/source-ownership and validated configuration
  behavior; no regression found.
- A-39: blocked by R3f-d. A rejected conditional mutation can still become a
  recovered success when both no-effect markers are lost.
- A-42: accepted for credential identity, redaction and manager ownership in
  this correction. No plaintext leak was found.
- A-46: accepted. No application authentication or trusted actor claim was
  introduced.
- A-47: accepted. Origin and CORS behavior remained green.
- A-56: blocked by R3f-d and R3g-b. Restart recovery still permits false
  terminal success, and the startup assembly invariant is bypassable.

## Integration recommendation

Do not mark C-04 complete. Preserve this product checkpoint and receipt. Make
the no-effect result attempt-bound and durable, or leave the reservation
pending whenever neither release nor completion can be stored; current-state
field equality cannot prove which attempt caused it. Enforce persistence
protocol and recovery-owner coverage in the same atomic startup/setter path,
including removal/replacement of either callback. Rerun both independent
real-SQLite outage and late-assembly probes, managed-credential mismatch,
ordinary/recovered ETag replay, then the full offline guardrail.
