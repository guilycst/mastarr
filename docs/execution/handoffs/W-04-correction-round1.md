# W-04 correction round one handoff

## Assignment

- Task: W-04, correction round one for workflow deadline, rejection and clock
  boundaries.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction dispatch checkpoint: `e0b82fae28ee4419188562befa382d1917567ea6`.
- Reviewed source product: `2821b5bf8926849423c93fa7e862fd38b83035bc`.
- Prior review receipt: `docs/execution/handoffs/W-04-review-round1.md`.
- Prior review commit: `1b22197ff8fa81ec4d836c6ad113315ffabf4c9d`.
- Owned product paths: `internal/reviews/`, `internal/workflows/`.
- Handoff path: `docs/execution/handoffs/W-04-correction-round1.md`.
- Checkout: shared `main`; coordinator owns `docs/execution/state.json`.
- Product commit: `4a43bd68c281559c0cd80386415b05db4df9e19d`.

## Corrections

- **R1, workflow deadline claim fence:** the review transaction now reads and
  returns the durable workflow deadline from `ensureWorkflowBinding`. An
  approved linked `action_runs` row receives that exact deadline in the same
  transaction as the review decision, workflow step binding and idempotency
  record. Replay verifies that the existing action still carries the same
  deadline. The regression exercises the real SQLite due and claim queries
  after expiry without calling workflow `Sync`; neither path returns the
  action.
- **R2, rejection durability:** a rejected linked review now persists the
  decision value, optional reason and `StepBlocked` state while the decision
  transaction is open. The workflow adapter no longer performs a second
  post-decision rejection transaction. A fresh workflow/review service reads
  the committed blocked step and rejection metadata after the decision, and a
  later approval cannot reinterpret the rejected plan as a new action.
- **R3, trusted AddStep lifecycle time:** `AddStep` captures one service clock
  instant and uses it for plan expiry, workflow deadline and persisted step
  lifecycle values. A caller-supplied historical timestamp cannot extend an
  expired workflow or revive an expired plan; a future caller timestamp is
  rejected after the durable workflow-open check. Caller time is not used as
  lifecycle authority.

The changes preserve immutable plan/revision/digest binding, ordered step
approval, idempotency, cancellation and read-only workflow projection. No
upstream, filesystem, Arr native or Seerr write was added.

## Synthetic regressions

`internal/workflows/workflows_test.go` adds deterministic coverage for:

- workflow deadline propagation to `action_runs.deadline_at`, exclusion from
  `ListDueActionRuns`, and failed `ClaimActionRun` after expiry;
- atomic rejected review metadata/state, no rejected action run, fresh-service
  projection and rejection conflict on later approval;
- backdated append after a trusted deadline, future append timestamp rejection,
  and expired plan rejection under the trusted service clock.

All fixtures use temporary SQLite state, synthetic plans and fixed timestamps.
No credentials, private coordinates, live services, inventories or media were
used.

## Verification

| Command or scenario | Result |
| --- | --- |
| `gofmt -w internal/reviews/reviews.go internal/workflows/workflows.go internal/workflows/workflows_test.go` | Passed |
| `git diff --check` | Passed |
| `GOWORK=off go test ./internal/reviews ./internal/workflows -count=1` | Passed, exit 0 |
| `GOWORK=off go test -race ./internal/reviews ./internal/workflows -count=3` | Passed, exit 0; all three repetitions |
| `GOWORK=off go test ./... -count=1` | Passed, exit 0 |
| `GOWORK=off go vet ./internal/reviews ./internal/workflows` | Passed, exit 0 |
| `GOWORK=off go mod verify` | Passed, exit 0; all modules verified |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0; generation, staged generation, API/Vacuum, architecture, lint, root/UI/tools/client module tests and race, vet, module verification and Linux amd64/arm64 CGO-free builds completed |
| Product pre-commit hook during `4a43bd6` | Passed, exit 0; fast generation, staged generation, API/Vacuum, architecture, format and targeted tests |

The repository-wide aggregate was run after product commit
`4a43bd68c281559c0cd80386415b05db4df9e19d`; its log ended with
`guardrail checks passed (ci)`.

## Gates and next action

F-05 remains capability-blocked where the reviewed filesystem organize port
cannot provide the required safe primitive. G-01 keeps Arr native
registration/import writes fail-closed. Seerr remains read-only. These workflow
changes only persist review and queue intent and do not bypass those gates.

The product checkpoint is ready for independent correction review. The
coordinator owns state updates, review receipt integration and any later
publication decision.
