# W-04 independent review, round four

## Decision

`approved`.

Correction round three closes R2c. The durable decision-marker boundary now
has three explicit outcomes: absence may resume an ordinary blocked step, the
exact rejection string remains blocked and projects `needs_review`, and every
other present value returns `ErrWorkflowConflict`. The error path leaves the
workflow and every step row unchanged, including when an earlier action-linked
step was projected before a later contradictory marker was encountered in the
same transaction.

All earlier W-04 findings remain closed. No P1 or P2 findings remain in the
reviewed scope.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `780c00e92498660c08b6ac67259bbb4147c58531`.
- Product parent:
  `52bda9ca5c2b820c39c84b8a4f54695315937fae`.
- Product tree:
  `9399c2a5176129d3649d4741fa42c67518142166`.
- Exact correction handoff:
  `83429eac6e73575a4d6642dcd29b105c6d218285`.
- Handoff parent: the exact correction product.
- Handoff tree:
  `09875c2161c34f64e84d42177006b6aa4b2f0b12`.
- Exact review-dispatch checkpoint:
  `540d917feb61b98ab503787c0a4c148c955838c9`.
- Checkpoint parent: the exact correction handoff.
- Checkpoint tree:
  `02ca8eb215a814fb3177da13785bc78de030bfbc`.
- Prior review receipt:
  `128674e1de36944720a8dbccd010fa110c2b9f4c`.
- Reviewed scope: `internal/reviews/`, `internal/workflows/`, and
  `docs/execution/handoffs/W-04-correction-round3.md` only.
- Acceptance reviewed: A-12, A-15, A-32, A-35, A-37, and A-56.

Review ran in a clean detached worktree at the exact checkpoint. The product
is the handoff parent, the handoff is the checkpoint parent, and no product
drift exists under either owned package between product and checkpoint. The
correction changes only `internal/workflows/workflows.go` and its synthetic
test file. Independent probes ran from a temporary archive of the checkpoint
with temporary SQLite databases and synthetic plans. Product files, execution
state, task definitions, schemas, generated output, and unrelated files were
not edited. No live service, credential, private coordinate, inventory, or
media was used.

## R2c closure

`rejectedStepDecision` at `internal/workflows/workflows.go:1342-1354` now:

- returns `(false, nil)` only when the `decision` key is absent;
- returns `(true, nil)` only when the decoded JSON string is `"reject"`;
- returns an error wrapping `ErrWorkflowConflict` for every other present
  value or any value that cannot decode as a string.

`projectAggregate` evaluates that result before it can requeue the current
blocked step. `Sync` performs the complete projection in one SQLite
transaction, so an error also rolls back projections attempted earlier in the
step loop.

The producer regression covers absent, reject, approve, unknown, empty, null,
and numeric decision values and compares workflow and step state, evidence,
current index, and timestamps before and after every rejected call. It passed
25 consecutive focused runs and five focused race runs.

The independent marker matrix broadened this to fourteen cases:

- absent marker queues an ordinary blocked step and leaves the workflow
  running;
- `"reject"` and the equivalent escaped JSON string `"\u0072eject"` retain
  `StepBlocked`, project `WorkflowNeedsReview`, and create no action;
- `"approve"`, unknown, empty, null, number, boolean, array, object,
  case-changed, whitespace-prefixed, and unpaired-surrogate values all return
  `ErrWorkflowConflict` with byte-for-byte equivalent durable row snapshots.

That independent matrix passed 25 ordinary runs and ten race runs. A separate
two-step probe made the first action terminal, placed a contradictory marker
on the second step, then called `Sync`. The first step's tentative projection
was rolled back with the later error; workflow and both step rows matched their
pre-call snapshots. It passed 25 ordinary and ten race runs.

## Earlier finding regression audit

- R1 remains closed: workflow deadlines are copied to the queued action and
  fence both due-list and claim SQL without requiring an earlier workflow
  `Sync`.
- R2 remains closed: rejection decision, step state/evidence, workflow link,
  action decision path, and idempotency state share the required transaction.
- R3 remains closed: `AddStep` uses the service clock for lifecycle and plan
  expiry authority and rejects a future caller timestamp.
- R2a remains closed: a fresh service projects a rejected current step as
  `needs_review/blocked`, creates no action, and uses a valid workflow state
  transition.
- R2b remains closed: matching fresh-key replay preserves immutable actor,
  caller label, reason, and workflow evidence; conflicting attribution fails
  without mutation.
- Cancellation/deadline closure, ordered prerequisite approval, separate
  registration/import gates, exact plan/revision/digest binding, bounded
  validation, durable effects, and trailing JSON rejection remain covered and
  passed their package and aggregate checks.

No direct upstream transport, filesystem mutation, generated DTO, local
`replace`, or tracked `go.work` was introduced. Arr native writes remain behind
G-01, and unsupported filesystem operations remain behind F-05.

## Independent checks

All Go commands used `GOWORK=off GOPROXY=off GOSUMDB=off` unless noted.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint ancestry, trees, scoped drift, clean review worktree, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| `go test ./internal/workflows ./internal/reviews -count=1 -timeout=180s` | Passed; workflows `1.776s`, reviews `1.496s`. |
| `go vet ./internal/workflows ./internal/reviews` | Passed. |
| Producer marker and prior-control focused suite, `-count=25` | Passed; workflows `17.645s`. |
| Focused marker/restart/attribution/prior-control race suite, `-count=5` | Passed; workflows `81.583s`, reviews `18.262s`. |
| Independent fourteen-case marker matrix | Passed 25 ordinary runs (`23.444s`) and ten race runs (`223.665s`). |
| Independent two-step rollback probe | Passed 25 ordinary runs (`2.071s`) and ten race runs (`17.232s`). |
| `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, every Vacuum contract, architecture, lint, root and nested module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Acceptance disposition

- A-12: accepted for W-04. Registration and import retain independent plans,
  gates, decisions, and ordered workflow steps.
- A-15: accepted for W-04. Approval remains bound to exact current immutable
  plan identity and expiry; changed or forged intent creates no dispatch.
- A-32: accepted for W-04. Decision, queued action, workflow linkage, and
  idempotency state preserve all-or-nothing transaction and dedupe behavior.
- A-35: accepted for W-04. Workflow deadlines fence SQLite dispatch claims;
  cancellation/deadline closure prevents later workflow mutation while
  preserving action evidence.
- A-37: accepted for W-04. Restart projection is honest for rejection,
  failure, blocking, cancellation, and uncertainty; contradictory durable
  evidence now fails closed without partial projection.
- A-56: accepted for W-04. Bounds, required prerequisites, idempotency
  conflicts, and malformed durable JSON paths remain fail-closed.

W-04 is ready for coordinator integration and execution-state closure. F-05
and G-01 remain explicit capability gates for their owning action/adapter
lanes. This review does not authorize release, deployment, upstream writes, or
live filesystem or media mutation.
