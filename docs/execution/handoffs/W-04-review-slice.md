# W-04 review-decision slice handoff

## Assignment

- Task ID and title: W-04 review-decision boundary, narrowed first slice
- Owner/agent: `/root/x05_implementer`
- Independent reviewer: `/root/x05_reviewer`
- Dispatch base: `fc73bef5167f2cf7848858b2a02110fbc851c683`
- Branch/worktree: shared `main` checkout
- Owned product paths: `internal/reviews/`
- Handoff path: `docs/execution/handoffs/W-04-review-slice.md`
- Coordinator-owned paths left unchanged: `docs/execution/state.json`, API/UI contracts, storage schema, workers, adapters, clients and workflows
- Planned acceptance contributions: A-12, A-15, A-56

## Contract and work

The review package is the local durable boundary between an immutable
`planning.Plan` and `execution.ActionRun`. It has no upstream, filesystem or
worker calls.

- `StorePlan` validates a ready or explicitly conflicted planning object,
  persists the complete immutable revision and flattened manifest rows in one
  SQLite transaction, and appends later revisions only at `current+1`.
- The persisted input envelope keeps the plan, exact action intent bytes and a
  SHA-256 intent digest. Loading a plan rejects malformed/trailing JSON,
  changed plan identity, changed manifest rows and a mismatched intent digest.
- `Approve` requires the exact current plan ID, revision, digest, plan status,
  approval window and at least one required precondition. The action-specific
  gate is enforced: registration, import and generic action approvals cannot
  substitute for one another, and `ApprovalNone` is rejected as blanket
  approval.
- Approval, the immutable review decision, the optional workflow-step binding,
  the queued action run and the idempotency record share one short SQLite
  transaction. A rejected decision has no action run.
- Same request keys replay the original response. Reusing a key with changed
  actor, decision, plan binding or workflow binding returns an idempotency
  conflict. Deterministic decision/action IDs prevent a second resource for
  the same plan revision.
- Actor defaults to `unauthenticated`; caller labels and reasons are bounded
  and control-character checked. No authentication claim is made in v0.0.1.

The ordered workflow composition layer is intentionally deferred to the next
W-04 slice. This boundary already accepts an exact workflow/step binding when
the workflow tables are populated; it does not create or advance workflows.
No native Arr, client, Jellyfin, Seerr or filesystem write is enabled.

## Verification

All checks used the synthetic local SQLite migration fixture and no live
service, credential, private coordinate or media data.

| Command or scenario | Result | Evidence |
| --- | --- | --- |
| `GOWORK=off go test ./internal/reviews -count=1 -timeout=90s` | Passed, exit 0 | `internal/reviews/reviews_test.go` |
| `GOWORK=off go test -race ./internal/reviews -count=1 -timeout=90s` | Passed, exit 0 | `internal/reviews/reviews_test.go` |
| `GOWORK=off go vet ./internal/reviews` | Passed, exit 0 | `internal/reviews/` |
| `GOWORK=off go mod verify` | Passed, exit 0; all modules verified | root module |
| `GOWORK=off go test ./internal/... -count=1 -timeout=180s` | Passed, exit 0; all root internal packages | root internal packages |
| Product pre-commit hook | Passed, exit 0; generation, staged generation, API/Vacuum, architecture, targeted tests and fast guardrails | `.githooks/pre-commit` |
| A-12 registration/import gate regression | Passed; registration gate is accepted, an import plan carrying the registration gate is rejected as blanket approval | `TestApprovalGateNeverReusesRegistrationForImport` |
| A-15 expiry/forgery/revision regressions | Passed; expired and forged approvals create no action, stale revision is rejected, exact current revision is accepted | `TestApproveRejectsExpiredForgedAndBlanketRequests`, `TestStorePlanAppendsImmutableRevisionAndRejectsStaleApproval` |
| A-56 invalid/oversized/missing-precondition regressions | Passed; malformed preconditions, oversized intent, and no required precondition fail before action creation | `TestStorePlanRejectsMissingPreconditionAndOversizedIntentBeforeWrite`, `TestApproveRequiresAtLeastOneRequiredPrecondition` |
| Atomic/idempotent approval regressions | Passed; decision and queued action appear together, same-key replay returns the same IDs, concurrent same intent creates one decision/action | `TestApprovePersistsDecisionAndActionAtomically`, `TestApproveSameKeyReplaysAndChangedPayloadConflicts`, `TestApproveConcurrentSameIntentCreatesOneDecision` |

The repository-wide CI-equivalent aggregate and ordered workflow tests were not
run in this narrowed lane; the coordinator should run them after integrating
the slice with the next workflow implementation. Generated API output and
module files were unchanged.

## Review and integration

- Product commit: `2d4712df7679a67d5f054f0c614558f812e7dea5`
- Review receipt: pending `/root/x05_reviewer`
- Findings and correction commits: pending independent review
- Final reviewer decision: pending
- Integrated commit and execution state: coordinator-owned; not changed here

## Resume checkpoint

- Current state: the review boundary is committed and independently testable.
- Remaining W-04 work: create ordered recipes, persist workflow steps, enforce
  prerequisite completion/cancellation/deadline projection and expose workflow
  progress through the API layer in a separately owned slice.
- Safety gates: G-01 native Arr registration/import remains fail-closed; F-05
  filesystem capability limitations remain explicit; Seerr remains read-only.
- Next safe action: independent review of product commit
  `2d4712df7679a67d5f054f0c614558f812e7dea5`, followed by coordinator state
  recording and a separately dispatched workflow-composition slice.
- No conflicting writes or unknown files removed.
