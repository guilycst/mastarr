# W-04 independent review, round two

## Decision

`changes_requested`.

Correction round one closes the prior R1 deadline-claim failure and R3 trusted
clock failure. It also closes R2's split-transaction crash window by committing
the rejected step state and decision metadata with the review decision.

Two P2 findings remain in the rejected-decision projection. A fresh `Sync`
reports a workflow with a permanently rejected current step as `running`
instead of `needs_review`. A semantic replay under a fresh idempotency key can
also rewrite the step's rejection reason while the immutable review decision
retains the original reason. There are no remaining P1 findings.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `4a43bd68c281559c0cd80386415b05db4df9e19d`.
- Product parent:
  `e0b82fae28ee4419188562befa382d1917567ea6`.
- Product tree:
  `ae61006fcee73201e14cc653e1064c01b1e5013e`.
- Exact correction handoff:
  `c0b3824aa52c3e0001614417c71bf96002a6e2c3`.
- Handoff parent: the exact correction product.
- Handoff tree:
  `fc77fd8e1c848575af09e73e182f543c22d8bf65`.
- Exact review-dispatch checkpoint:
  `e4cf0d0c68b07d0510e7a00c6f269c4a43a87978`.
- Checkpoint parent: the exact correction handoff.
- Checkpoint tree:
  `f29a972debd197b7aa8dda8fdca64a184aeb471d`.
- Prior receipt:
  `1b22197ff8fa81ec4d836c6ad113315ffabf4c9d`.
- Reviewed scope: `internal/reviews/`, `internal/workflows/`, and
  `docs/execution/handoffs/W-04-correction-round1.md` only.
- Acceptance reviewed: A-12, A-15, A-32, A-35, A-37, and A-56.

Review ran in a clean detached worktree at the exact checkpoint. The product is
an ancestor of the checkpoint and there is no product drift under either owned
package. Product, execution state, task definitions, schemas, generated output,
and unrelated files were not edited. Independent probes ran from a temporary
archive of the exact checkpoint with synthetic plans and temporary SQLite
databases. No live service, credential, private coordinate, inventory, or media
was used.

## Prior finding closure

### R1 closed: workflow deadline fences due and claim paths

The review transaction now reads the durable workflow deadline and writes it to
the linked action's `deadline_at` in the same transaction as decision, step link,
action, and idempotency state at `internal/reviews/reviews.go:451-471`. Existing
decision replay verifies the linked action retains the same nullable deadline at
`internal/reviews/reviews.go:412-434`.

The prior independent probe passed 10/10. Without calling workflow `Sync`, the
action carried the exact workflow deadline, `ListDueActionRuns` returned zero
after expiry, and `ClaimActionRun` returned `sql.ErrNoRows`. This closes the P1
dispatch boundary under A-35.

### R2 crash window closed: rejection step state is atomic

`ensureWorkflowBinding` now writes `decision=reject`, optional rejection reason,
and `StepBlocked` while the review transaction remains open at
`internal/reviews/reviews.go:657-680`. The second `markRejected` transaction was
removed. A direct review call followed by a fresh service instance observed the
blocked step, the original decision/reason, no action, and a conflict on later
approval. The prior crash-boundary defect is closed under A-32/A-37.

### R3 closed: append lifecycle uses the service clock

`AddStep` captures one trusted service time, uses it for plan expiry and
`ensureOpen`, and rejects a caller timestamp in the future before writes. The
prior backdated append probe passed 10/10: an unexpired candidate plan could not
be appended after the workflow deadline, and the step count remained unchanged.
Producer coverage also verifies expired-plan and future-timestamp refusal.

## Remaining findings

### R2a — P2: rejected current step projects the workflow as running

After an atomic rejection, `projectAggregate` recognizes the `decision=reject`
marker and correctly leaves the step blocked at
`internal/workflows/workflows.go:1284-1295`. It then initializes the aggregate
to `WorkflowRunning` and only maps a blocked step to `WorkflowNeedsReview` when
metadata contains `actionState=needs_review` at
`internal/workflows/workflows.go:1299-1323`. A rejected decision has no action,
so that condition can never hold.

The independent restart probe reproduced 10/10:

```text
rejected projection workflow=running step=blocked, want needs_review/blocked
```

The same probe verified that no action exists and any later approval returns
`reviews.ErrDecisionConflict`. The workflow therefore reports active execution
while it has no executable action and no legal progress path. This conflicts
with the documented `needs_review` workflow state and A-37's requirement to
distinguish blocked work. The producer regression currently codifies
`running/blocked` at `internal/workflows/workflows_test.go:216-222`; change the
aggregate projection and regression to `needs_review/blocked`.

### R2b — P2: a fresh-key rejection replay can rewrite immutable evidence

The existing-decision branch checks only plan identity and decision at
`internal/reviews/reviews.go:412-434`, then calls `ensureWorkflowBinding` with
the new request. For a rejection, that function writes the new request's reason
into workflow step metadata at `internal/reviews/reviews.go:665-680`. It does not
project the reason from the already durable `review_decisions` row or require
the immutable attribution fields to match.

The independent probe rejected a plan with actor `first` and reason `original`,
then repeated the same rejection using a fresh idempotency key, actor `second`,
and reason `rewritten`. It reproduced 10/10:

```text
step reason="rewritten" but immutable review decision reason="original"
```

No duplicate decision or action is created, but the workflow evidence and
review record disagree. Preserve the original decision projection on semantic
replay, or reject conflicting immutable attribution. Add fresh-key replay
coverage for actor, caller label, and rejection reason.

## Preserved behavior

- Exact plan revision/digest, current revision, expiry, required precondition,
  manifest rows, source/config/mapping bindings, and typed approval gate checks
  remain intact.
- Registration and later import retain distinct approvals and action runs.
- Approval decision, workflow link, action queue row, deadline, and idempotency
  record share one SQLite transaction.
- Cancellation still closes the workflow, marks linked actions cooperatively,
  cancels undispatched steps, and prevents later approval or append.
- Recipe/metadata trailing JSON rejection, identifier and request bounds, step
  count bounds, and desired-state bounds remain intact.
- Durable action state, effects, unresolved counts, failure, cancellation, and
  late evidence remain available after caller/BFF closure.
- No upstream/native transport, generated DTO, filesystem mutation, or live
  service dependency was added. F-05 and G-01 remain fail-closed in their
  owning lanes.
- No tracked `go.work` or local `replace` directive is present.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| `go test ./internal/reviews ./internal/workflows -count=1` | Passed. |
| `go test -race ./internal/reviews ./internal/workflows -count=3` | Passed; reviews `40.345s`, workflows `41.315s`. |
| `go vet ./internal/reviews ./internal/workflows` | Passed. |
| Independent prior R1 and R3 probes, `-count=10` | Passed. Exact deadline excluded due/claim without `Sync`; trusted time rejected backdated append with zero added steps. |
| Independent R2 restart/aggregate probe, `-count=10` | Failed at the remaining aggregate-state assertion; atomic blocked state and immutable decision survived restart. |
| Independent fresh-key rejection-reason probe, `-count=10` | Failed deterministically with workflow metadata rewritten while the review row retained the original reason. |
| Architecture/import and native-write inspection | Passed. No generated DTO or direct adapter/client/filesystem dependency in the reviewed packages. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible/staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not mark W-04 complete. R1 and R3 are closed, and the R2 transaction split is
closed. Correct R2a and R2b so rejected work has one durable authoritative
projection and an honest aggregate state. Keep A-37 open for W-04; the corrected
R1/R3 evidence supports A-32 and A-35, while A-12/A-15/A-56 remain preserved.
This receipt does not authorize release, deployment, live upstream writes, or
live filesystem or media mutation.
