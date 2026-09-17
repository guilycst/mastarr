# C-04 correction round eight handoff

## Assignment

- Task ID and title: C-04, generated REST transport and process startup;
  correction round eight
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Current shared-main dispatch checkpoint: `a68e396d8d15923dd0bac5e3fa35d6446063d314`
- Coordinator-recorded correction base/dispatch: `d646d967b8375528ce707debbe461a815c38b29e`
- Prior review source: `docs/execution/handoffs/C-04-review-round7.md`
- Prior review commit: `0770b38dc318c776ffb33ad4063c51c5b5416a58`
- Prior review integration: `03cd48e81aabd5279bb9ea701e6cd6d027856624`
- Branch/worktree: shared `main` checkout
- Owned product paths: `internal/transport/`, `cmd/mastarr/`
- Handoff path: `docs/execution/handoffs/C-04-correction-round8.md`
- Product commit: `b031416f27e6819d454d8ec419e7a4ffa4e8eb12`
- Product parent: `a68e396d8d15923dd0bac5e3fa35d6446063d314`
- Coordinator-owned state was not edited

## Correction

### R3g-c: built-in configuration recovery owner is durable

`requiresDurableMutationRecovery` now counts the built-in connection,
storage-root and path-mapping handlers whenever an idempotency persistence
repository is attached, even when `RouteDependencies` is nil. Therefore
`SetIdempotencyRecovery(nil)` fails with
`ErrIdempotencyRecoveryRequired` and leaves
`recoverConfigurationIdempotency` installed. When durable persistence is
attached, the readiness flag remains true only while an effective callback is
present.

Late `SetIdempotencyPersistence` also rejects a complete repository when the
effective recovery callback is nil, preserving the previous repository and
preventing a startup sequence from attaching durable mutation state to an
ownerless server. The HTTP policy checks both readiness and callback presence
under the assembly read lock before a mutation can dispatch.

The regressions cover a no-injected-dependency server with durable
persistence, failed built-in owner removal, owner removal before storage
attachment followed by rejected late persistence, callback retention, and
readiness. A repeated concurrent setter probe exercises the same invariant
under the race detector.

## Verification

All tests used synthetic IDs, endpoints and temporary stores. No live service,
media, private coordinate or credential was used.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `gofmt -w internal/transport/server.go internal/transport/server_test.go` | passed, exit 0 | Owned Go files formatted |
| `git diff --check` and staged product diff check | passed, exit 0 | Product diff hygiene |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport -run 'TestBuiltInConfigurationRecoveryOwnerCannotBeRemovedWithDurablePersistence\|TestBuiltInConfigurationRecoveryOwnerRemovalIsRaceSafe\|TestDurableMutationSettersKeepRecoveryAssemblyAtomic' -count=1 -v` | passed, exit 0; 0.470s | No-injected owner and setter regressions |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./internal/transport -run 'TestBuiltInConfigurationRecoveryOwnerCannotBeRemovedWithDurablePersistence\|TestBuiltInConfigurationRecoveryOwnerRemovalIsRaceSafe\|TestDurableMutationSettersKeepRecoveryAssemblyAtomic' -count=3` | passed, exit 0; 1.465s | Repeated owner-removal race coverage |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport -count=1` | passed, exit 0; 0.231s | Full transport tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -count=1` | passed, exit 0; transport 0.229s, cmd/mastarr 1.000s | Full owned ordinary tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./internal/transport ./cmd/mastarr -count=1` | passed, exit 0; transport 1.337s, cmd/mastarr 18.755s | Full owned race tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport` | passed, exit 0 | Transport vet |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | passed, exit 0; `all modules verified` | Root module verification |
| `python3 scripts/check-architecture.py` | passed, exit 0 | Architecture import boundaries |
| `python3 scripts/check_planning.py` | passed, exit 0 | 53 tasks, 60 acceptance cases; local links resolve |
| Product pre-commit hook | passed, exit 0 | Generation, staged generation, API/Vacuum, architecture and fast checks |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | passed, exit 0; final marker `guardrail checks passed (ci)` | Full offline generation, six-contract Vacuum, lint/architecture, ordinary/race tests, vet, module verification and Linux cross-build matrix |

## Boundaries

- No schema, migration, OpenAPI, generated output, client module, `go.work`
  or local `replace` directive changed.
- G-01 Arr native registration/import writes remain fail-closed, F-05
  unsupported filesystem operations remain fail-closed, and Seerr remains
  read-only. This correction added no native or live writes.
- Effect markers and owner state contain no request body, credentials, URL,
  endpoint or response bytes beyond existing replay data.

## Resume checkpoint

- Product tree: `b031416f27e6819d454d8ec419e7a4ffa4e8eb12`
- Product checks are green and the exact tree is ready for independent review.
- Next action: commit this handoff separately; coordinator records both SHAs
  in `docs/execution/state.json` and dispatches independent review.
- No state files or unrelated files were edited.
