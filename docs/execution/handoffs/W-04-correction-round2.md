# W-04 correction round two handoff

## Identity and scope

- Task: W-04 correction round two.
- Base/checkpoint: `a2e1fdeb5b8bad2a7280beb739b9157ff9a5ff28`.
- Prior W-04 product: `4a43bd68c281559c0cd80386415b05db4df9e19d`.
- Prior review receipt: `78ccbb8b234b4265280c3383dfad2ac782804b48`.
- Product commit: `c05777cda74b8e3940d633c01495aae86a89b73b`.
- Handoff commit: the separate commit containing this file; its exact SHA is
  returned with this handoff.
- Owned product paths: `internal/reviews/`, `internal/workflows/`, and
  `internal/workflows/workflows_test.go`.
- Handoff path: `docs/execution/handoffs/W-04-correction-round2.md`.
- Acceptance scope: A-12, A-15, A-32, A-35, A-37, and A-56.

All fixtures use temporary SQLite stores and synthetic plans. No credentials,
live services, private coordinates, inventories, or media were used. No
execution state or unrelated lane was edited.

## Correction closure

### R2a: rejected current steps project `needs_review`

`projectAggregate` now decodes the typed rejection marker and returns
`domain.WorkflowNeedsReview` while retaining the current step as
`domain.StepBlocked`. A rejected step is terminal for the current approval
gate, so Sync does not queue it or create an action. The review transaction
advances an `awaiting_approval` workflow to `running` for both approval
outcomes; Sync then performs the legal `running -> needs_review` transition.
This preserves the domain transition graph while making a durable rejection
honest after a fresh service restart. Malformed rejection evidence fails
closed as a workflow conflict.

The regression in
`TestRejectedReviewAtomicallyProjectsBlockedStepAcrossRestart` verifies the
durable blocked step, no action run, the valid running bridge, and the fresh
projection `needs_review/blocked`. Later approval remains a
`reviews.ErrDecisionConflict`.

### R2b: immutable attribution and workflow evidence

When a deterministic decision already exists, a request using a fresh
idempotency key must match the persisted actor, caller label, and reason. Any
conflict returns `reviews.ErrDecisionConflict` before workflow metadata can be
rewritten. Rejection workflow evidence now preserves existing typed values and
rejects contradictory metadata. New rejection bindings retain the decision,
actor, caller label, and reason in the step evidence; matching semantic
replays leave those values unchanged.

The same regression covers a matching fresh-key replay and separate actor,
caller-label, and reason conflicts. It checks the immutable
`review_decisions` row and exact workflow evidence before and after replay.

## Checks

All commands ran with `GOWORK=off` unless noted.

| Check | Result |
| --- | --- |
| `git diff --check` | Passed. |
| `GOWORK=off go test ./internal/reviews ./internal/workflows -count=1` | Passed. |
| `GOWORK=off go test -race ./internal/reviews ./internal/workflows -run 'Test(RejectedReviewAtomicallyProjectsBlockedStepAcrossRestart\|ApproveSameKeyReplaysAndChangedPayloadConflicts\|ApproveConcurrentSameIntentCreatesOneDecision)$' -count=5 -timeout=120s` | Passed; reviews `19.663s`, workflows `11.125s`. |
| `GOWORK=off go vet ./internal/reviews ./internal/workflows` | Passed. |
| `GOWORK=off go test ./... -count=1 -timeout=180s` | Passed for all root packages and synthetic fixtures. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0. Generation/reproducibility, staged generation, API/Vacuum, architecture/import boundaries, lint, root and nested module test/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |
| Pre-commit hook during product commit | Passed; fast generation, staged API/Vacuum, architecture, and targeted checks passed. |

## Remaining gates

Arr native registration/import writes remain blocked by G-01. F-05
filesystem capability failures remain fail-closed in the owning lane. This
correction adds no upstream calls, filesystem mutation, generated DTO, or
live-service dependency. Independent review is still required before W-04 is
marked complete; this handoff does not authorize release, deployment, or live
media mutation.
