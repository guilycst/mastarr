# C-04 correction round six handoff

## Assignment

- Task ID and title: C-04, generated REST transport and process startup;
  correction round six
- Owner: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Prior dispatch commit: `ecea16b62844083d06c7a491d7ccbfad12dcd89f`
- Clean product base/state pin: `01f837a64ba4c48ff89a8a08962e703eababfdd6`
- Prior review receipt: `d9e36c574bb03991cb5607e12cd0255ddd940fe4`
- Branch/worktree: shared `main` checkout
- Owned product paths: `internal/transport/`, `cmd/mastarr/`
- Handoff path: `docs/execution/handoffs/C-04-correction-round5.md`
- Product commit: `52f3654cd0a669dbc3dc59f5187bec828616c34e`
- Coordinator-owned state was not edited

## Correction scope

This checkpoint closes the four independent round-five findings while keeping
the strict generated boundary, request-local idempotency state, sanitized
errors, configuration gate and read-only recovery rules.

### R3f-a: rejected PATCH cannot recover as success

The transport now classifies only built-in configuration `412
precondition_failed` and `409 configuration_conflict` responses as
attempt-specific no-effect outcomes. It releases the exact durable
scope/key/digest/attempt before a completion write can be mistaken for a
successful read-back. Injected dependencies are excluded because a service may
have performed an effect before returning the same status. The correction
regressions cover stale connection PATCH responses and fresh-manager SQLite
replay; both remain rejected and never become durable `200` responses.

### R3f-b: duplicate CREATE cannot recover as success

The SQLite connection persistence seam checks the exact durable connection ID
before inserting its pending parent row and returns the typed configuration
conflict. This keeps a duplicate CREATE deterministic instead of converting a
SQLite uniqueness error into generic persistence failure. The transport then
releases the exact no-effect attempt, and fresh-manager replay remains `409`
rather than `201`. Built-in in-memory and SQLite tests cover duplicate create
replay. Managed connection recovery additionally resolves the exact requested
credential fields through the configured manager, compares only bounded
credential names, zeros every read-back copy, and never stores plaintext;
mismatched managed credentials cannot recover as a created response.

### R3f-c: replay preserves ETag

Both transport replay-header sanitization and SQLite idempotency-header
sanitization use an explicit case-insensitive `ETag` spelling before applying
the replay allowlist. Values are appended when equivalent header keys are
encountered, so ordinary durable replay and recovered responses retain the
current conditional-mutation token. Startup restart coverage asserts the
ordinary replay header; the fresh-manager PATCH recovery matrix asserts the
post-mutation ETag for connection, storage-root and path-mapping responses.

### R3g-a: assembled mutation owner boundary

`transport.New` now rejects an assembled durable mutating dependency when no
`IdempotencyRecovery` owner is supplied. This prevents action, scan, workflow,
connection-check and other injected mutation services from being assembled
behind a durable reservation that would remain pending forever after lost
completion. The built-in configuration handlers retain their own read-only
recovery owner, including exact managed-credential PATCH read-back. In the
current `cmd/mastarr` assembly, nonconfiguration mutation callbacks remain
unassembled; their strict `not_ready`/`service_unavailable` response is
released only before dispatch. Injected unavailable services remain pending.
No generic recovery or blind redispatch was introduced.

## Verification

All scenarios used synthetic IDs, endpoints, credentials and temporary SQLite
roots. No live services, media, private coordinates or plaintext credential
material were used.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `gofmt -w internal/transport/*.go cmd/mastarr/*.go` | passed, exit 0 | Owned Go files formatted |
| `git diff --check` | passed, exit 0 | Product diff hygiene |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -run 'Test(DurableMutationsRequireAnExplicitRecoveryOwner\|ConfigurationRecoveryAuthenticatesManagedCredentialIdentity\|BuiltInRejectedConfigurationOutcomeReleasesExactAttempt\|BuiltInDuplicateCreateDoesNotRecoverAsCreated\|DefaultPatchRecoveryRecognizesPostMutationRevision\|SQLiteRejectedConfigurationOutcomesStayRejectedAcrossFreshManager\|RunPersistsAPIConfigurationAndIdempotencyAcrossRestart)' -count=5 -timeout=180s` | passed, exit 0 | Focused correction and restart regressions |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./internal/transport ./cmd/mastarr -run 'Test(DurableMutationsRequireAnExplicitRecoveryOwner\|ConfigurationRecoveryAuthenticatesManagedCredentialIdentity\|BuiltInRejectedConfigurationOutcomeReleasesExactAttempt\|BuiltInDuplicateCreateDoesNotRecoverAsCreated\|DefaultPatchRecoveryRecognizesPostMutationRevision\|SQLiteRejectedConfigurationOutcomesStayRejectedAcrossFreshManager\|RunPersistsAPIConfigurationAndIdempotencyAcrossRestart)' -count=3 -timeout=300s` | passed, exit 0; transport 1.446s, cmd/mastarr 16.092s | Repeated race probes |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/transport ./cmd/mastarr -count=2 -timeout=240s` | passed, exit 0; transport 0.239s, cmd/mastarr 7.423s | Full owned package tests |
| `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./internal/transport ./cmd/mastarr` | passed, exit 0 | Owned package vet |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | passed, exit 0; `all modules verified` | Root module verification |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/mastarr` | passed, exit 0 | Linux amd64 compile |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/mastarr` | passed, exit 0 | Linux arm64 compile |
| Product pre-commit hook | passed, exit 0 | Generation, staged generation, API/Vacuum, architecture and fast checks |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | passed, exit 0; final marker `guardrail checks passed (ci)`; observed wall time about 92.4s | Full offline generation, staged generation, six-contract Vacuum, lint/architecture, root/UI/tools/client tests, race, vet, module verification and cross-build checks |

## Boundaries and blockers

- A durable injected mutation dependency must provide an explicit
  `IdempotencyRecovery` owner at construction. The current process assembly
  leaves nonconfiguration services unassembled, so their pre-dispatch route
  failures are safely released and no native operation is attempted.
- G-01 Arr native registration/import writes remain fail-closed, F-05
  unsupported filesystem operations remain fail-closed, and Seerr remains
  read-only. This correction did not enable native or live writes.
- No schema, API contract, client module, migration, `go.work` or local
  `replace` directive changed.
- Darwin-specific runtime testing was not run in this Linux-compatible
  checkout; no Darwin behavior was changed by this transport-only correction.

## Resume checkpoint

- Product tree: `52f3654cd0a669dbc3dc59f5187bec828616c34e`
- Product checks are green and the exact tree is ready for independent review.
- Next action: commit this handoff separately, then coordinator records both
  SHAs in `docs/execution/state.json` and dispatches independent review.
- No state files or unrelated files were edited.
