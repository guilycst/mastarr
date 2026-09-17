# C-04 correction round seven handoff

## Assignment

- Task ID and title: C-04, generated REST transport and process startup;
  correction round seven
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Dispatch checkpoint: `720c1c10a9c7cad550e353b8798380664c10b7a7`
- Prior review receipt: `8598d5619509706cac383697d22fe93345de1573`
- Branch/worktree: shared `main` checkout
- Owned product paths: `internal/transport/`, `cmd/mastarr/`
- Handoff path: `docs/execution/handoffs/C-04-correction-round6.md`
- Product commit: `23462380b004586c1e09794c937ae396ee18b394`
- Product parent: `720c1c10a9c7cad550e353b8798380664c10b7a7`
- Coordinator-owned state was not edited

## Corrections

### R3f-d: no-effect outcomes stay pending when both terminal writes fail

Configuration transactions now append an attempt-bound `idempotency_effect`
marker in the same SQLite transaction as the manager mutation. The marker
contains only the route scope, key, request digest, attempt ID, operation,
resource kind/ID and committed revision. It is written for connection,
storage-root and path-mapping create, patch and retire operations; direct
manager calls without an HTTP attempt do not create an unrelated marker.

The built-in recovery owner receives the persistence's
`RequireRecoveryEvidence` policy. SQLite enables that policy, so a markerless
reserved record can never be promoted from coincident current configuration
fields to a successful `200` or `201`. It remains `idempotency_pending` until
an exact durable release, completion, or attempt-bound effect is available.
Successful mutations whose ordinary completion is lost retain the marker and
recover only when every marker field and the read-back resource/revision agree.
Marker decoding is strict and rejects malformed or mismatched effect identity.

### R3g-b: durable persistence and recovery-owner assembly is atomic

The transport now serializes idempotency persistence/owner publication with
mutation handling through an assembly read/write gate. `SetIdempotencyPersistence`
validates the complete reservation protocol and rejects a late durable store
when an assembled mutating dependency has no explicit recovery owner.
`SetIdempotencyRecovery` returns an error for owner removal while such a store
is attached and preserves the previous owner; non-nil replacement is installed
before readiness is published. A request cannot pass the readiness check and
then dispatch while a setter changes the persistence/owner pair.

## Verification

All tests used synthetic IDs, endpoints, credentials and temporary SQLite
databases. No live service, media, private coordinate or plaintext
credential was used.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `gofmt -w internal/transport/routes.go internal/transport/server.go internal/transport/idempotency_recovery.go internal/transport/server_test.go cmd/mastarr/persistence.go cmd/mastarr/main_test.go` | passed, exit 0 | Owned Go files formatted |
| `git diff --cached --check` | passed, exit 0 | Product diff hygiene |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -run 'TestDurableMutationSettersKeepRecoveryAssemblyAtomic\|TestSQLiteConfigurationEffectMarkersBlockFalseRecoveryDuringMarkerOutage\|TestSQLiteRejectedConfigurationOutcomesStayRejectedAcrossFreshManager' -count=1 -v` | passed, exit 0 | Setter, marker-outage and prior fresh-manager regressions |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./internal/transport ./cmd/mastarr -run 'TestDurableMutationSettersKeepRecoveryAssemblyAtomic\|TestSQLiteConfigurationEffectMarkersBlockFalseRecoveryDuringMarkerOutage\|TestSQLiteRejectedConfigurationOutcomesStayRejectedAcrossFreshManager' -count=3` | passed, exit 0; transport 1.442s, cmd/mastarr 10.228s | Repeated real SQLite and assembly race probes |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -count=1` | passed, exit 0 | Full owned package tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./internal/transport ./cmd/mastarr -count=1` | passed, exit 0; transport 1.314s, cmd/mastarr 13.187s | Full owned package race tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport ./cmd/mastarr` | passed, exit 0 | Owned package vet |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | passed, exit 0; `all modules verified` | Root module verification |
| Product pre-commit hook | passed, exit 0 | Generation, staged generation, API/Vacuum, architecture and fast checks |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | passed, exit 0; final marker `guardrail checks passed (ci)` | Full offline generation, six-contract Vacuum, lint/architecture, root/UI/tools/client tests, race, vet, module verification and Linux cross-build matrix |

## Boundaries

- No schema, migration, OpenAPI, generated output, client module, `go.work`
  or local `replace` directive changed.
- G-01 Arr native registration/import writes remain fail-closed, F-05
  unsupported filesystem operations remain fail-closed, and Seerr remains
  read-only. This correction added no native or live writes.
- The marker is metadata-only and contains no request body, credentials, URL,
  endpoint or response bytes beyond existing replay data.

## Resume checkpoint

- Product tree: `23462380b004586c1e09794c937ae396ee18b394`
- Product checks are green and the exact tree is ready for independent review.
- Next action: commit this handoff separately; coordinator records both SHAs
  in `docs/execution/state.json` and dispatches independent review.
- No state files or unrelated files were edited.
