# C-04 correction round three handoff

## Assignment

- Task ID and title: C-04, integrate generated REST transport and process startup; correction round three
- Owner/agent and independent reviewer: `/root/x05_implementer`; independent review by `/root/x05_reviewer`
- Base commit: `69d0165155982a333c645695c210e42a48f14cac`
- Prior review receipt: `be8683bb8b9dd5031585478c480441674aa65c1f`
- Branch/worktree: shared `main` checkout; coordinator owns execution state
- Owned files: `internal/transport/`, `cmd/mastarr/`, and this handoff
- Product commit: `14b692080f763f36a800fda021374729a00d4d7f`
- State ownership: `docs/execution/state.json` was not edited

## Contract and work

This correction closes the review's R3c completion-recovery and R2b
credential-manager ownership findings while preserving the existing C-04
HTTP, ETag, request-boundary, and fail-closed route behavior.

### R3c: durable completion recovery

The immutable `idempotency_records` schema remains unchanged. The SQLite
implementation now uses scoped, immutable event rows in that existing table:

- each new reservation carries an opaque, validated `AttemptID`;
- a reservation after a prior known no-effect release is recorded under an
  owner/state event scope, preserving the original digest binding;
- a successful handler result is completed under the same owner event scope;
- a proven pre-dispatch/no-effect configuration failure records a released
  event, allowing an exact same-digest key to retry without blind dispatch;
- event resolution reads the latest row for the route/key and checks digest,
  state, status, body, and attempt identity before accepting it.

Pending reservations and non-replayable outcomes remain held. A fresh process
therefore never dispatches a second time merely because completion was lost.
The transport exposes `IdempotencyRecovery` as the production reconciliation
owner. Pending requests pass bounded method/path/If-Match/body input and the
durable pending record to that owner; only a read-back-proven, replayable
terminal response can be completed and replayed. The built-in owner covers
configuration connection and storage-root create/patch/retire routes. It
leaves path-mapping DELETE and nonconfiguration application routes pending
when no route-specific owner can prove the effect, preserving the explicit
bootstrap/reconciliation boundary rather than guessing.

Configuration persistence failures reload the durable manager before the
configuration gate is released. After a successful reload, an authoritative
read-back of absence releases the exact reservation; a present, changed, or
unreadable resource stays pending for reconciliation. This keeps an uncertain
effect from being retried as if it were absent.

### R2b: credential-manager ownership

`cmd/mastarr` now keeps each configuration manager paired with the credential
manager it owns. Reload opens a fresh credential manager from the configured
environment, secret file, or persistent generated key, rebuilds API state,
verifies managed envelopes, swaps the pair while the configuration write gate
is held, and closes the replaced manager only after the swap. Shutdown detaches
and closes the active pair once; repeated shutdown calls are harmless. A
replacement credential manager remains usable after the old manager is
closed, and the active manager is zeroed on shutdown.

The existing exact JSON-number canonicalization, durable digest conflict,
strong If-Match, bounded body/manifest validation, origin policy, generated
route assembly, and sanitized error behavior remain unchanged. No schema,
OpenAPI contract, client module, local replace, `go.work`, upstream write
policy, or live service behavior changed.

## Verification

All commands below ran from the product tree with `GOWORK=off`; offline gates
also set `GOPROXY=off GOSUMDB=off`.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `go test ./internal/transport ./cmd/mastarr -count=1` | passed, exit 0 | Product tests |
| `go test -race ./internal/transport ./cmd/mastarr -count=3 -timeout=240s` | passed, exit 0 | Product race tests |
| `go test ./... -count=1` | passed, exit 0 | Root package matrix |
| `go vet ./...` | passed, exit 0 | Root vet |
| `go mod verify` | passed: `all modules verified` | Root module verification |
| `TestDurableIdempotencyRecoveryOwnerCompletesWithoutRedispatch` | passed, including repeated race runs | Injected owner proves durable pending completion without a second dispatch |
| `TestDefaultConfigurationRecoveryOwnerReadsBackWithoutRedispatch` | passed | Built-in configuration read-back owner completes a lost SQLite-style reservation without a second handler dispatch |
| `TestConfigurationRollbackReleasesExactDurableReservation` | passed | Proven absent rolled-back configuration releases the exact owner-bound reservation and permits same-key retry |
| `TestSQLiteIdempotencyReleaseAllowsExactRetryAndPreservesConflict` | passed | SQLite release event survives lookup, permits exact retry, and preserves changed-digest conflict |
| `TestRuntimeConfigurationOwnerSwapsAndClosesCredentialManagers` | passed | Replaced manager closes its own key; active replacement remains usable until one shutdown close |
| `env GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | passed, exit 0; final marker `guardrail checks passed (ci)` | Full offline CI-equivalent gate: generation, staged generation, Vacuum, architecture, lint, root/UI/tools/client tests, race, vet, mod verification and Linux cross-builds |
| `git diff --check` and `gofmt -l internal/transport cmd/mastarr` | passed, exit 0 / no output | Product tree hygiene |

The pre-commit fast gate for product commit `14b692080f763f36a800fda021374729a00d4d7f`
also passed generation, staged generation, API/Vacuum, architecture and
targeted tests. Fixtures use only synthetic IDs, endpoints, bodies and
credential values; no secret, private coordinate, live service or media data
was used.

## Review and integration

- Review source: `be8683bb8b9dd5031585478c480441674aa65c1f`, which requested a
  production recovery owner for lost completion/known no-effect failures and
  ownership-safe credential-manager reload/shutdown.
- R3c disposition: fixed for configuration routes with durable reservation,
  release, owner-bound completion events and read-only recovery. Other route
  families require their assembled `IdempotencyRecovery` owner; absent proof
  remains a durable pending conflict and cannot redispatch.
- R2b disposition: fixed with paired manager/credential ownership, fresh key
  manager per reload, replaced-manager close, and exactly-once active shutdown.
- Existing policy gates remain: G-01 Arr native writes are fail-closed,
  F-05 unsupported filesystem capabilities are fail-closed, and Seerr remains
  read-only.
- Final independent review and coordinator integration are pending; this
  handoff does not edit or claim `state.json`.

## Resume checkpoint

- Current state: product commit `14b692080f763f36a800fda021374729a00d4d7f`
  is clean and all listed checks pass. Handoff is the next separate commit.
- Outstanding uncertainty: route-specific reconciliation owners still need
  to be assembled for nonconfiguration mutations; the transport deliberately
  leaves those durable attempts pending when no owner is supplied.
- Next safe action: independent review of the exact product commit, followed by
  coordinator state recording and integration if the review clears it.
- No conflicting writes or unknown files were removed.
