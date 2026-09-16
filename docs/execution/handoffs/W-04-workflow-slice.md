# W-04 workflow-composition slice handoff

## Assignment

- Task: W-04, ordered workflow composition after the review-decision slice
- Owner: `/root` coordinator takeover
- Independent reviewer: `/root/x05_reviewer`
- Dispatch checkpoint: `3579ced` (workflow-composition retry)
- Review-decision dependency: product `2d4712df7679a67d5f054f0c614558f812e7dea5`, handoff `0bc36d3229334f18388dd2c609ab21b040801cdd`
- Owned product paths: `internal/workflows/`, `internal/workflows/workflows_test.go`
- Handoff path: `docs/execution/handoffs/W-04-workflow-slice.md`
- Coordinator-owned state remains outside this slice

The initial workflow-composition implementer attempt produced no files after a
bounded retry. The coordinator took over the same paths and preserved the
completed `internal/reviews` slice. No other module, adapter, client, storage
schema, API contract or state file was changed by the product commit.

## Product

Product commit: `2821b5b`

The workflow service provides a bounded ordered recipe over immutable saved
plans. It validates every referenced plan before publication and rechecks the
exact revision and digest in the SQLite transaction. Each step keeps its plan,
approval gate, decision/action links and projected effects.

`Create` persists a workflow and ordered steps with deterministic idempotency.
`ApproveStep` enforces the current step and prior-step completion before it
delegates the exact plan binding to `reviews.Service`. Registration approval
does not approve a later import plan. `Sync` projects action state and effect
evidence into step and workflow views, including failed, blocked,
reconciling and unresolved states. `Cancel` records a durable cancellation,
requests cooperative cancellation for linked action runs, and prevents later
approval or dispatch. Deadline sync closes the workflow and cancels pending
steps. `AddStep` revalidates the plan, updates the stored recipe, rejects
duplicates or invalid order, and uses a replayable idempotency record.

The implementation keeps filesystem capability failures and Arr native writes
fail-closed. It makes no upstream or filesystem calls and adds no Seerr write.

## Synthetic verification

- `TestOrderedRegistrationThenImportNeedsSeparateApprovals` proves ordered
  registration then import approval, current-step gating, action linkage and
  read-back advancement after the first action succeeds.
- `TestCancelClosesWorkflowAndPreventsLaterDispatch` proves cancellation,
  linked action cancellation markers, cancelled step projection and rejection
  of later approval or step append.
- `TestAddStepUpdatesRecipeAndReplaysIdempotently` proves recipe persistence,
  same-key replay and changed-payload conflict.
- `TestSyncDeadlineCancelsUnapprovedSteps` proves deadline closure and pending
  step cancellation.
- `TestDecodeRejectsTrailingJSON` proves durable evidence parsing rejects
  trailing JSON.

All fixtures use temporary SQLite state, synthetic plans and fake timestamps.
No live service, credential, private coordinate, inventory or media data was
used.

## Checks

All commands ran with `GOWORK=off` where applicable.

| Check | Result |
| --- | --- |
| `gofmt` on workflow product/tests | Passed |
| `go test ./internal/workflows -count=1` | Passed |
| `go test -race ./internal/workflows -count=3` | Passed |
| `go vet ./internal/workflows` | Passed |
| `go test ./... -count=1` | Passed |
| `go mod verify` | Passed |
| Product pre-commit hook | Passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0; generation, Vacuum, lint, architecture, module tests/race, verification and Linux amd64/arm64 CGO-free builds |

## Gates and next step

F-05 remains capability-blocked for unsupported Linux move, rename,
permanent-delete and cross-device source removal. G-01 keeps Arr native
registration/import writes fail-closed pending X-05 evidence. Seerr remains
read-only. The workflow slice is ready for final independent W-04 review
against the review-decision product and this handoff. The coordinator will
record the review receipt and integrate W-04 only after the reviewer clears all
findings.
