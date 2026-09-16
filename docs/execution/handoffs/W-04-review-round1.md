# W-04 independent review, round one

## Decision

`changes_requested`.

Two P1 findings and one P2 finding remain. A queued action created through a
workflow does not inherit the workflow deadline and remains eligible for an
executor claim after that deadline. A rejected review decision and its workflow
projection are committed in separate transactions, so a crash leaves a durable
decision with a queued/running workflow step that cannot be approved. Finally,
step append trusts a caller-supplied historical timestamp, allowing a new step
to be committed after the workflow's actual deadline.

The ordinary tests and the complete offline guardrail matrix pass. Those checks
do not exercise these crash/deadline boundaries, and therefore do not clear
A-32, A-35, or A-37 for W-04.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of the product authors.
- Review-decision product:
  `2d4712df7679a67d5f054f0c614558f812e7dea5`.
- Review-decision parent:
  `fc73bef5167f2cf7848858b2a02110fbc851c683`.
- Review-decision tree:
  `6b2058f1c3ca9da9e3e6bc6f248640eaaf52df0b`.
- Workflow-composition product:
  `2821b5bf8926849423c93fa7e862fd38b83035bc`.
- Workflow-composition parent:
  `3579ced3c745d9a4a791b1c51638e63141154752`.
- Workflow-composition tree:
  `64dce80ecfc77fd78e770682cca758f099e11239`.
- Final handoff:
  `704ec47524cef4108a3e947c0c0166f6894ed610`.
- Handoff parent:
  `a435e9cc7f1559cf88ad2d88a027955d1fb5af38`.
- Handoff tree:
  `5a6502d0662c2065ec180eb82f17b98813e377a9`.
- Exact review-dispatch checkpoint:
  `3b87fc788fb815e44e31072c58e5194aabbad734`.
- Checkpoint tree:
  `de4cd1c2e9fa937ab8506c9896634b658dff45ac`.
- Reviewed product scope: `internal/reviews/`, `internal/workflows/`, and
  `docs/execution/handoffs/W-04.md` only.
- Acceptance reviewed: A-12, A-15, A-32, A-35, A-37, and A-56.

The shared checkout was clean at the exact checkpoint before review. Review ran
in a clean detached worktree at that checkpoint. Both product commits are
ancestors of the checkpoint. There is no scoped drift from the review-decision
product to the checkpoint under `internal/reviews/`, or from the workflow
product to the checkpoint under `internal/workflows/`. Product, execution state,
task definitions, schemas, generated output, and unrelated files were not
edited. Independent probes ran in a temporary archive of the exact checkpoint
using synthetic plans and temporary SQLite databases. No live service,
credential, private coordinate, inventory, or media was used.

## Findings

### R1 — P1: workflow deadlines do not fence executor claims

`reviews.Service.Approve` creates the linked action at
`internal/reviews/reviews.go:456-460`, but does not copy the workflow's durable
`deadline_at` into `action_runs.deadline_at`. The executor's due and claim SQL
checks only the action row's deadline at `internal/storage/query.sql:566-595`.
`workflows.Sync` is the only code that converts an elapsed workflow deadline
into action cancellation, at `internal/workflows/workflows.go:437-478`.

The independent probe created a workflow with deadline `12:01`, approved its
first step at `12:00`, and queried the ordinary W-02 due-work path at `12:02`
without first calling `Sync`. The linked action had `deadline_at = NULL` and
`ListDueActionRuns` returned one dispatchable action:

```text
queued workflow action has NULL deadline_at; deadline 2026-09-16 12:01:00 +0000 UTC is not attached to executor claim
1 workflow action(s) remain dispatchable after deadline
```

This contradicts A-35 and the HTTP contract that a workflow deadline prevents
further mutation dispatch. Correctness currently depends on an unrecorded
scheduler ordering `Sync` ahead of every executor claim. Bind the workflow
deadline into the action row in the same approval transaction, or make the due
and claim boundary atomically consult the linked workflow deadline. Add a real
executor/SQLite regression that proves no claim after expiry without an earlier
workflow sync.

### R2 — P1: a rejected decision can commit without a durable rejected step

`reviews.Service.Approve` commits the review decision and workflow link at
`internal/reviews/reviews.go:446-465`. For a rejection,
`ensureWorkflowBinding` writes the step back to `queued` and records only the
decision ID at `internal/reviews/reviews.go:649-665`. After that transaction has
returned, `workflows.ApproveStep` starts a second transaction through
`markRejected` at `internal/workflows/workflows.go:590-603,882-898` to add the
rejection value and mark the step blocked.

The independent crash-boundary probe stopped after the first committed
transaction, then called `Sync`. The durable state projected as:

```text
workflow=running step=queued
```

No action exists, and a later approval conflicts with the already durable
rejection. `Sync` cannot recover the decision because it only projects linked
actions. Recovery therefore depends on the original client repeating the exact
reject request. Browser/BFF closure or a process crash in this window leaves a
misleading, non-progressing workflow, contrary to A-32/A-37 and the handoff's
failure-preservation claim. Persist the rejection metadata and blocked/needs
review projection in the same transaction as the decision, or add durable
reconciliation that derives it from `review_decisions` after restart.

### R3 — P2: append can be backdated across the workflow deadline

`AddStep` accepts `request.At` as authority at
`internal/workflows/workflows.go:637-642`. It uses that timestamp both to decide
whether the referenced plan is currently ready and to call `ensureOpen` at
`internal/workflows/workflows.go:673-681`. `ensureOpen` compares the deadline
against that caller value at `internal/workflows/workflows.go:1412-1425`, not
against the service clock.

The independent probe advanced the service clock two hours beyond a workflow's
deadline, then submitted an append with the original pre-deadline `At`. The
append returned success instead of `ErrWorkflowClosed`. The same caller time is
persisted as the new step's creation/update time. Although later approval still
has an additional current-time deadline fence, the expired workflow's immutable
recipe and durable scope can be extended after closure. Use the trusted service
clock for lifecycle and plan-expiry checks; retain caller time only as bounded,
non-authoritative metadata if required. Add backdated/future append and
create-at-deadline regressions.

## Accepted behavior retained

- Plan validation recomputes the planning digest, checks the current revision,
  verifies exact manifest rows, rejects stale/forged/expired approval, and
  preserves separate registration/import approval kinds.
- Approval creates the decision, action run, workflow link, and idempotency
  record in one SQLite transaction for the approval path.
- Same-key replay and changed-payload conflict behavior pass, including the
  concurrent same-intent test.
- Ordered approval blocks the import step until registration succeeds. A later
  step receives a distinct decision and action.
- Cancellation commits workflow closure, marks linked actions cooperatively,
  cancels undispatched steps, and rejects later ordinary approval/append calls.
- Recipe and step metadata decoders reject trailing JSON. Recipe size, step
  count, identifiers, actor/reason fields, desired intent, and required
  preconditions have explicit bounds or validation in the reviewed paths.
- Workflow reads project durable action state, effects, unresolved counts, and
  late terminal evidence independently of the caller/BFF lifetime.
- The reviewed packages contain no upstream/native transport or filesystem
  mutation code and expose no generated DTOs. F-05 and G-01 capability blocks
  remain in their owning action/adapter lanes; no bypass or live write was
  introduced.
- No tracked `go.work` or local `replace` directive is present.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, exact HEAD, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| `go test ./internal/reviews ./internal/workflows -count=1` | Passed. |
| `go test -race ./internal/reviews ./internal/workflows -count=3` | Passed; reviews `40.318s`, workflows `24.715s`. |
| `go vet ./internal/reviews ./internal/workflows` | Passed. |
| Independent deadline-claim, reject crash-boundary, and backdated-append probes | Failed as expected, exit 1, with all three findings reproduced. A focused deadline rerun separately confirmed both `deadline_at = NULL` and one due action after expiry. |
| Package/import inspection for generated DTO, adapter/client, native transport and filesystem-write leakage | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible/staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not integrate W-04 as complete. Correct R1 and R2 before approval; correct R3
in the same lane because it is an authority-boundary defect in recipe mutation.
Retain A-12/A-15/A-56 evidence, but keep A-32/A-35/A-37 open. This receipt does
not authorize release, deployment, live upstream writes, or live filesystem or
media mutation.
