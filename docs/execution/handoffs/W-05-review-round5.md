# W-05 independent review, round five

## Decision

`changes_requested`.

The correction now persists identity-valid partial retry effects per item and
retries only selected items when every terminal item still agrees with the
filesystem. One P1 and one P2 remain. A terminal `trashed` item observed back
at its original source path is treated as ordinary source-only pending scope,
so another retry can terminalize the whole entry while that item is not in
trash. Read-only reconciliation also overwrites the exact action outcome and
scope evidence it was meant to preserve.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Correction dispatch base:
  `03d06218a36d19b2f504731664c35b8d9fd5b06f`.
- State dispatch commit:
  `cd6fb94e5496d2cb0c91a78ac0fe496df7148b85`.
- Exact product: `2ba78ca03c1b3f2afa31949782f75af012d8aab6`,
  whose direct parent is the state dispatch commit.
- Product tree: `b91913a48b6de71c8fda4a04d3cb5eac95780e44`.
- Exact handoff: `36ad4e0e6f2525994bfa863afe4e8393c7d45b05`,
  whose direct parent is the product.
- Handoff tree: `61d4a5a87885b533b8d23ff3a082febe6702de0a`.
- Exact review checkpoint:
  `009d5b266452a19ffa2785a93aabbf7f2f539a15`, whose direct parent is the
  handoff.
- Scoped correction diff SHA-256 from round-four product
  `985be793b184f090599b2ee7aea3860993091139`:
  `520f6529fea87a2be18edbb35bc61528125d28efcc986dcd07a810c5d4c866eb`.
- Product-scope archive SHA-256:
  `d01795a80e1a991b6837e6e59b1511ee1f60c02c418901783b3df82ab6e7ceb3`.
- Reviewed product paths: `internal/trash/` only. There is no product-path
  drift from the exact product to the review checkpoint.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30 and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used a temporary archive with synthetic SQLite state, manifests and
normalized ports. Final probe source SHA-256:
`2e853208c4bd6ebbc78ac69a888f4cba6d18767c45a28925ac9f84c2749e788a`.
No product, state, schema, API, adapter, client, worker, live service or real
media data was changed.

## Findings

### R3d - P1 - A stale terminal item authorizes false aggregate completion

After an explicit retry reports the first of two exact files applied, the
correction correctly marks that item `trashed` and leaves the second item
`selected`. If read-back then observes the terminal first item as
`source_only` rather than `trash_only`, its durable state is stale and the
operation must hold.

`observePlannedTrash` says this should block finalization, but lines 958-965
append every non-`trash_only` terminal observation to the same aggregate state
slice used for pending items. When the terminal first item and pending second
item are both source-only, `classifyTrashReconciliation` returns
`TrashSourceOnly` at lines 1070-1071. `ReconcileTrash` then sets `Retryable` at
lines 785-796 because at least one selected item remains. It does not
distinguish the stale terminal row.

Independent reproduction:

1. Seed two exact source files and execute an explicit retry.
2. Return an identity-valid `OutcomeApplied` effect for the first file and a
   partial error. The first item becomes `trashed`; the second stays selected.
3. Leave the first exact object at its original path and its mapped trash path
   absent, modeling a contradictory response or an external restore before
   read-back.
4. From a fresh service, call `ReconcileTrash`. It returns `ErrUncertain` with
   `Retryable=true` instead of holding the stale terminal item.
5. `RetryTrash` sends only the second item, which is the correct narrow request,
   but then marks the entry and both item rows `trashed`.
6. The first exact source still exists and its trash path is still absent.

The false completion reproduced 20/20 ordinary runs and 10/10 race runs. No
completed item is resubmitted, but the durable aggregate state contradicts the
filesystem. This violates A-27's changed-object and recoverable-state boundary
and A-57's honest reconciliation requirement.

Track terminal-item inconsistency separately from pending-item disposition.
Any terminal item that is not exact `trash_only` must block retry and aggregate
completion, with its observed state retained for review.

### R3e - P2 - Reconciliation overwrites exact action and scope evidence

`persistTrashReconciliation` reads the current purge journal at lines
1108-1115, but preserves only a Boolean `scope_unresolved` marker. It then
replaces `OutcomeJson` with a new observation envelope at lines 1167-1180.

For an ordinary identity-valid partial result, the durable first-item
`OutcomeApplied`, action evidence and partial error are replaced after read-back
by `OutcomeAlreadySatisfied` plus filesystem observation evidence. For an
exact-plus-foreign result, the generic `scope_unresolved` marker survives, but
the foreign path, `affected_foreign` reason and original action evidence do
not. Both losses reproduced 20/20 ordinary and 10/10 race runs.

Current observation should be appended or merged without replacing the prior
item-bound action effect and exact unresolved-scope evidence. A fresh process
needs both the mutation report and later read-back to explain why an item is
terminal, pending or blocked.

## Closed correction behavior and preserved boundaries

- An identity-valid partial retry now atomically marks matched items `trashed`,
  leaves unmatched items selected, and returns item-bound `trashed` and
  `pending` effects.
- With consistent read-back, a fresh service reconstructs a leaf-only manifest
  containing the remaining selected item. It does not resubmit a terminal item
  or a directory root.
- Foreign and duplicate reports hold the entry, keep a generic unresolved
  marker and prevent a second filesystem dispatch. R3e concerns loss of the
  exact evidence, not a bypass of that gate.
- R3b planned source/trash/both/neither/changed-identity, directory-leaf,
  restart and Tick regressions pass. Trash-only finalizes without an action;
  source-only requires explicit retry.
- R1a approval-bound early purge, R2a invalid affected scope, R2b directory
  leaf accounting and R3a no-blind-replay regressions remain passing.
- Default/custom retention, qBittorrent stop-before-payload, metadata-only
  removal and restore-without-re-add/resume remain behind normalized ports.
- F-05 filesystem trash/restore/permanent-delete remains unsupported and
  fail-closed. G-01 Arr writes remain disabled. X-14 metadata removal remains
  separate and preserves `deleteFiles=false`.
- No generated DTO, direct upstream transport, credential, private
  coordinate, live service, local `replace` or tracked `go.work` entered the
  reviewed scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| `go test ./internal/trash -count=10 -timeout=300s` | Passed; package time `21.154s`. |
| `go test -race ./internal/trash -count=3 -timeout=420s` | Passed; package time `164.964s`. |
| Focused RetryTrash/planned/approved/partial/directory regressions, `-count=10` | Passed; package time `18.977s`. |
| Initial independent assertions requiring stale terminal hold and exact evidence retention | Failed as expected. Reconciliation returned retryable source-only; the journal replaced applied and exact foreign evidence. |
| Independent R3d/R3e deterministic reproductions, `-count=20` | Passed; package time `4.096s`; all defects reproduced every run. |
| Independent R3d/R3e reproductions, `-race -count=10` | Passed; package time `48.676s`; all defects reproduced every run. |
| `go test ./... -count=1 -timeout=300s` | Passed. |
| `go vet ./internal/trash` and root `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| Full offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in `293.30s`; final marker `guardrail checks passed (ci)`. Aggregate race, storage race (`200.379s`), generation, staged generation, Vacuum, lint, architecture, module tests/vet/verification and Linux amd64/arm64 cross-builds passed. |

## Acceptance disposition

- A-25: accepted for W-05 scope. Retention and janitor regressions pass.
- A-26: accepted for W-05 scope. Exact approval-bound early purge and direct
  hard-delete behavior retain passing evidence.
- A-27: blocked by R3d. A changed terminal item can authorize a retry and false
  aggregate completion.
- A-29: accepted for W-05 scope. Whole-client stop and metadata-only removal
  remain separate and ordered around selected payload handling.
- A-30: accepted for W-05 scope. Restore performs no automatic re-add or
  resume and preserves association evidence.
- A-57: blocked by R3d and R3e. Read-back can claim false completion and
  replaces the exact prior action/pending-effect explanation.

This receipt does not approve integration, release, deployment, live
filesystem mutation or live client control.
