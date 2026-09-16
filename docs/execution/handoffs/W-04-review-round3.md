# W-04 independent review, round three

## Decision

`changes_requested`.

Correction round two closes both findings from the prior receipt. A rejected
current step now projects as `needs_review/blocked` after restart without an
action, and fresh-key semantic replay preserves the immutable decision
attribution and step evidence. Conflicting actor, caller-label, or reason
returns `reviews.ErrDecisionConflict` without a durable mutation.

One P2 fail-closed finding remains in the new rejection projection helper. A
present `decision` marker is treated as ordinary unreviewed state whenever it
is not exactly `"reject"`. Syntactically valid but contradictory values such as
`"approve"`, an unknown string, the empty string, or `null` therefore make
`Sync` change the durable step from `blocked` to `queued` instead of returning
`ErrWorkflowConflict`. There are no P1 findings.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `c05777cda74b8e3940d633c01495aae86a89b73b`.
- Product parent:
  `a2e1fdeb5b8bad2a7280beb739b9157ff9a5ff28`.
- Product tree:
  `516e6982b989ba4f9c8a64039bf4b7a258229e50`.
- Exact correction handoff:
  `8f012eb0a9e1b7bc6ee227460886d477d7979c01`.
- Handoff parent: the exact correction product.
- Handoff tree:
  `563a3ba472916dc633c9704d5b3c045f798c4bb6`.
- Exact review-dispatch checkpoint:
  `8bf3b860eed24af2c9acc3b6dbd91f3ecb69e6e0`.
- Checkpoint parent: the exact correction handoff.
- Checkpoint tree:
  `50ec7dd96a9b951ab297efe64c94099a03c9634b`.
- Prior review receipt:
  `78ccbb8b234b4265280c3383dfad2ac782804b48`.
- Reviewed scope: `internal/reviews/`, `internal/workflows/`, and
  `docs/execution/handoffs/W-04-correction-round2.md` only.
- Acceptance reviewed: A-12, A-15, A-32, A-35, A-37, and A-56.

Review ran in a clean detached worktree at the exact checkpoint. The product
is the handoff parent, the handoff is the checkpoint parent, and no product
drift exists under either owned package between product and checkpoint. The
independent probes ran from a temporary archive of the exact checkpoint with
synthetic plans and temporary SQLite databases. Product files, execution
state, task definitions, schemas, generated output, and unrelated files were
not edited. No live service, credential, private coordinate, inventory, or
media was used.

## Prior finding closure

### R2a closed: rejected current steps project `needs_review/blocked`

`projectAggregate` now recognizes an exact rejection marker and returns
`WorkflowNeedsReview` while leaving the current step blocked at
`internal/workflows/workflows.go:1284-1300`. The review transaction first
creates the valid durable `running` bridge, and `Sync` then uses the permitted
`running -> needs_review` transition. It creates no action and later approval
still conflicts with the immutable rejection.

The producer regression passed 25 consecutive focused runs and five focused
race runs. An independent restart probe passed 25 ordinary and ten race runs.
It checked the initial durable `running/blocked` bridge, zero action rows,
fresh service construction, first and repeated `Sync`, final
`needs_review/blocked`, empty action linkage, and the domain transition graph.

### R2b closed: replay attribution and workflow evidence are immutable

The existing-decision path compares actor, caller label, and reason before it
can enter workflow binding at `internal/reviews/reviews.go:412-424`. Rejection
binding validates and preserves the typed decision attribution at
`internal/reviews/reviews.go:673-685,719-745`.

The independent probe verified that a matching fresh-key replay retains the
same decision ID, decision row, step outcome, step update time, and zero action
rows. Separate actor, caller-label, and reason conflicts each returned
`reviews.ErrDecisionConflict`; complete before/after snapshots of workflow,
step, decision, action, and idempotency rows were identical for each failed
request. This passed 25 ordinary and ten race runs.

### Earlier R1/R2/R3 controls remain closed

- The workflow deadline remains copied to `action_runs.deadline_at`; ordinary
  due and claim SQL cannot select it after expiry without a prior `Sync`.
- Decision, blocked rejection evidence, and workflow linkage remain in one
  transaction and survive fresh service construction.
- `AddStep` continues to use the trusted service clock for workflow deadline
  and plan expiry authority and rejects future caller timestamps.

The three producer controls passed 25 consecutive focused runs and the focused
race suite passed five runs.

## Finding

### R2c — P2: contradictory decision markers are silently re-queued

`rejectedStepDecision` at `internal/workflows/workflows.go:1342-1351` decodes a
present `decision` value into a string, but returns `(false, nil)` for every
value other than `"reject"`. `projectAggregate` interprets `false` as an
ordinary blocked step ready to resume and updates that step to `queued` at
`internal/workflows/workflows.go:1284-1297`.

That behavior is correct only when the decision key is absent. In the current
contract, a present decision marker on a blocked step is rejection authority;
approval binding uses `decisionId` plus `actionRunId` and does not write a
`decision: "approve"` marker. A present non-rejection value is therefore
contradictory durable evidence and must fail closed. The correction handoff
also says malformed rejection evidence returns a workflow conflict and that a
rejected step cannot be queued.

The independent temporary-copy probe created a synthetic blocked current step,
set one decision value, and called the public `Sync` path. All four cases
reproduced deterministically:

```text
decision="approve"  -> Sync error=<nil>, durable state="queued"
decision="unknown"  -> Sync error=<nil>, durable state="queued"
decision=""         -> Sync error=<nil>, durable state="queued"
decision=null       -> Sync error=<nil>, durable state="queued"
```

The probe expected `ErrWorkflowConflict` and unchanged blocked evidence. The
ordinary producer regression covers only the valid `"reject"` value, so the
full suite does not exercise this contradictory-marker branch.

Treat an absent decision key as unreviewed. Treat exactly `"reject"` as a
rejection. Any present null, empty, unknown, or other decision value must return
`ErrWorkflowConflict` before changing the step or workflow. Add the four-case
regression through `Sync` and assert that the transaction leaves both rows
unchanged. This is P2 because the current path makes durable evidence
dishonest and removes the safe blocked posture, although it does not itself
create or dispatch an action.

## Independent checks

All Go commands used `GOWORK=off GOPROXY=off GOSUMDB=off` unless noted.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint ancestry, trees, scoped drift, `git diff --check`, clean review worktree, no tracked `go.work`, and no local `replace` | Passed. |
| `go test ./internal/reviews ./internal/workflows -count=1 -timeout=180s` | Passed; reviews `1.386s`, workflows `1.596s`. |
| `go vet ./internal/reviews ./internal/workflows` | Passed. |
| Focused correction/prior-control race suite, `-count=5` | Passed; reviews `17.476s`, workflows `26.031s`. |
| Producer R2a/R2b/R1/R3 focused controls, `-count=25` | Passed; workflows `5.178s`. |
| Independent R2a/R2b restart/attribution probe | Passed 25 ordinary runs (`2.357s`) and ten race runs (`20.039s`). |
| Independent contradictory decision-marker matrix | Failed as described in R2c; all four values returned nil and committed `blocked -> queued`. |
| `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, every Vacuum contract, architecture, lint, root and nested module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not mark W-04 complete yet. R2a and R2b are closed, and the earlier deadline,
atomicity, and trusted-time fixes remain intact. Correct R2c and add the exact
semantic-marker regression before approval. A-12, A-15, A-32, A-35, and A-56
remain supported by the reviewed contribution; keep the W-04 contribution to
A-37 open until contradictory durable decision evidence fails closed. F-05 and
G-01 remain explicit external capability gates. This receipt does not authorize
release, deployment, upstream writes, or live filesystem or media mutation.
