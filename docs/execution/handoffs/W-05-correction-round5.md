# W-05 correction round five handoff

## Assignment

- Task: W-05 correction round five, close R3d and R3e from the independent
  round-five review.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `6c3428bd70e089db705ae8d393f5a8aeb6de5743`.
- State dispatch commit: `d778d5a576ec31d56ddab681937a11b4a5a73718`.
- Source product: `2ba78ca03c1b3f2afa31949782f75af012d8aab6`.
- Prior handoff: `36ad4e0e6f2525994bfa863afe4e8393c7d45b05`.
- Prior review: `docs/execution/handoffs/W-05-review-round5.md`, findings
  R3d P1 and R3e P2.
- Product commit: `73c55183faef67d20286b7656886b3e672cf8ef9`.
- Owned product paths: `internal/trash/`.
- Handoff path: `docs/execution/handoffs/W-05-correction-round5.md`.
- Checkout: shared `main`; execution state remains coordinator-owned.

No state, schema, API, adapter, client, worker, live service, credential or
real media files were changed.

## R3d correction: stale terminal rows hold the entry

`observePlannedTrash` now tracks terminal-item consistency separately from the
pending retry disposition. A row already marked `trashed`, `purged` or
`restored` is terminal only when its exact mapped destination is present, its
original source is absent, and its identity still matches the stored manifest.
Any source-only, both-present, neither-observable, changed-identity or other
non-`trash_only` observation marks the row with `trash_terminal_item_stale` and
sets the aggregate disposition to `terminal_item_stale`.

The stale disposition is persisted as held evidence. `ReconcileTrash` returns
`ErrHeld` with `Retryable` false, and `RetryTrash` repeats that read-only fence
without claiming or dispatching the remaining selected items. This prevents a
pending source-only subset from falsely completing an entry while a terminal
item remains at its original location or otherwise disagrees with its durable
state. Exact terminal trash-only rows continue to be excluded from a retry
manifest and remain visible in the observation evidence.

## R3e correction: read-back appends to the action record

Read-only reconciliation now keeps the prior trash action/reconciliation
envelope under a JSON `priorOutcome` field whenever the purge journal already
contains a trash operation. The prior envelope is inserted as a
`json.RawMessage`, preserving item-bound effects, affected identities,
action evidence, action errors/reasons, timestamps and exact foreign,
duplicate, ambiguous or identity-contradictory scope evidence. The current
read-back remains in the current `items`, `effects`, `evidence` and
`observedAt` fields. Reconciliation therefore adds the filesystem observation
without replacing the mutation report that explains the held state.

The existing unresolved-scope marker remains recursive and continues to block
held retry authorization. Invalid historical JSON is retained opaquely by the
existing outcome path; generated package outcomes are valid JSON and use the
lossless raw envelope path.

## Synthetic regressions

- `TestTrashReconciliationHoldsStaleTerminalItemAcrossRestart` performs an
  identity-valid partial retry, leaves the terminal item source-only, and
  starts a fresh service. It proves the stale terminal disposition is durable,
  the result is held and non-retryable, and neither reconciliation nor retry
  dispatches the remaining item.
- `TestTrashReconciliationRetainsActionAndScopeEvidence` returns an exact plus
  foreign partial effect, then observes the exact trash destination and the
  remaining source. It proves the matched item remains terminal, the entry is
  held, the prior action envelope is present, and action evidence, item IDs,
  foreign path, `scope_unresolved` and current read-back states all remain
  queryable after reconciliation.
- Existing round-one through round-four regressions continue to cover planned
  intent recovery, exact item scope, partial effects, foreign/duplicate
  rejection, directory leaves, approval binding, retention and restart
  behavior.

All fixtures use synthetic manifests, temporary SQLite databases and fake
normalized ports. No private endpoint, credential, cookie, inventory or live
media path is present.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -w internal/trash/trash.go internal/trash/correction_round5_test.go` | Passed |
| `git diff --check` and clean product tree | Passed |
| `GOWORK=off go test ./internal/trash -run 'TestTrashReconciliation(HoldsStaleTerminalItemAcrossRestart\|RetainsActionAndScopeEvidence)$' -count=1 -timeout=180s` | Passed |
| `GOWORK=off go test ./internal/trash -count=1 -timeout=240s` | Passed |
| `GOWORK=off go test -race ./internal/trash -run 'TestTrashReconciliation(HoldsStaleTerminalItemAcrossRestart\|RetainsActionAndScopeEvidence)$' -count=5 -timeout=300s` | Passed; package time 17.060s |
| Product pre-commit hook | Passed; generation, staged generation, API/Vacuum, architecture and fast guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed; final marker `guardrail checks passed (ci)` |

The CI-equivalent aggregate included root and nested module tests/races,
lint/architecture, generation and Vacuum, module verification, and Linux
amd64/arm64 CGO-free cross-builds. No live services or credentials were used.

## Exact checkpoint

- Product full SHA: `73c55183faef67d20286b7656886b3e672cf8ef9`.
- Handoff commit: pending; this file is the next atomic commit.
- Review status: independent round-six review pending.
- Remaining blockers: reviewed F-05 filesystem trash/restore/permanent-delete
  capabilities remain unsupported and fail closed; G-01 Arr native writes
  remain disabled by policy/evidence.
- Coordinator action: record product and handoff SHAs in
  `docs/execution/state.json`, then dispatch independent review against the
  exact product tree. No state file was edited by this worker.
