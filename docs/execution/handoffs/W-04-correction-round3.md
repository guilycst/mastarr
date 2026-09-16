# W-04 correction round three handoff

## Identity and scope

- Task: W-04 correction round three.
- Base/checkpoint: `52bda9ca5c2b820c39c84b8a4f54695315937fae`.
- Prior product: `c05777cda74b8e3940d633c01495aae86a89b73b`.
- Prior handoff: `8f012eb0a9e1b7bc6ee227460886d477d7979c01`.
- Prior review receipt: `128674e1de36944720a8dbccd010fa110c2b9f4c`.
- Product commit: `780c00e92498660c08b6ac67259bbb4147c58531`.
- Handoff commit: the separate commit containing this file; its exact SHA is
  returned with this handoff.
- Owned product paths: `internal/workflows/` and `internal/reviews/`.
- Handoff path: `docs/execution/handoffs/W-04-correction-round3.md`.
- Acceptance scope: A-12, A-15, A-32, A-35, A-37, and A-56.

All fixtures use temporary SQLite stores and synthetic plans. No credentials,
live services, private coordinates, inventories, or media were used. State
files and unrelated lanes were not edited.

## Correction closure

### R2c: contradictory decision markers fail closed

`rejectedStepDecision` now distinguishes the three durable cases explicitly:

- an absent `decision` key is ordinary blocked evidence and follows the
  existing queue/resume projection;
- exactly the JSON string `"reject"` is a rejection and projects the workflow
  to `domain.WorkflowNeedsReview` while retaining `domain.StepBlocked`;
- every present value other than that exact string, including `"approve"`, an
  unknown or empty string, `null`, and an invalid typed value, returns
  `ErrWorkflowConflict`.

`projectAggregate` validates the marker before changing the blocked step or
workflow. Because Sync performs projection in one SQLite transaction, a
contradictory marker rolls back any earlier projection work and leaves both
durable rows and their evidence unchanged. The existing exact rejection path
continues to preserve no action, a blocked step, and the valid
`awaiting_approval -> running -> needs_review` transition sequence.

`TestSyncRejectsContradictoryDecisionMarkersWithoutMutation` covers absent,
exact reject, approve, unknown, empty, null, and malformed typed markers. It
asserts the expected projection for absent/reject and compares workflow and
step state, current step, outcome evidence, and update timestamps before and
after every rejected Sync. SQLite enforces valid JSON envelopes, so the
malformed case uses a valid envelope whose `decision` member has an invalid
non-string type; the decoder rejects it before projection.

## Preserved behavior

- Rejected current steps remain blocked and never create or dispatch an action.
- Immutable review decision attribution and fresh-key replay checks from round
  two remain intact.
- Workflow deadlines, atomic decision/step linkage, trusted lifecycle time,
  cancellation, ordered approvals, and no-blanket-approval gates remain
  unchanged.
- No upstream/native transport, generated DTO, filesystem mutation, or live
  service dependency was added. G-01 Arr writes and F-05 filesystem writes
  remain explicitly fail-closed in their owning lanes.
- No tracked `go.work`, local `replace`, credentials, or private runtime data
  was introduced.

## Checks

All Go commands ran with `GOWORK=off` unless noted.

| Check | Result |
| --- | --- |
| `git diff --check` | Passed. |
| `GOWORK=off go test ./internal/workflows ./internal/reviews -count=1 -timeout=120s` | Passed. |
| `GOWORK=off go test -race ./internal/workflows ./internal/reviews -run 'Test(SyncRejectsContradictoryDecisionMarkersWithoutMutation\|RejectedReviewAtomicallyProjectsBlockedStepAcrossRestart\|ApproveSameKeyReplaysAndChangedPayloadConflicts\|ApproveConcurrentSameIntentCreatesOneDecision)$' -count=3 -timeout=180s` | Passed; workflows `38.596s`, reviews `10.944s`. |
| `GOWORK=off go vet ./internal/workflows ./internal/reviews` | Passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0. Generation/reproducibility, staged generation, API/Vacuum, architecture/import boundaries, lint, root and nested module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |
| Pre-commit hook during product commit | Passed; fast generation, staged API/Vacuum, architecture, and targeted checks passed. |

## Disposition

R2c is closed. W-04 remains subject to independent review and coordinator
state recording; this handoff does not authorize release, deployment, upstream
writes, or live filesystem or media mutation.
