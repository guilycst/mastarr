# W-05 correction round one handoff

## Assignment

- Task: W-05 correction round one, close the independent review findings R1-R4.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Source product: `37ecdde36b56a6cd3d7d147cd8af1b49eba3f45a`.
- Review receipt: `docs/execution/handoffs/W-05-review-round1.md`.
- Dispatch checkpoint: `c436ce2abe6c16f72227bd6b5c307c106b09ef5e`.
- Product commit: `2f843b3e678d86e9300ce60f48f31e3c87f18791`.
- Owned product paths: `internal/trash/`.
- Handoff path: `docs/execution/handoffs/W-05-correction-round1.md`.
- Checkout: shared `main`; execution state remains coordinator-owned.

No state, schema, API, adapter, client, worker, live service, credential or
real media files were changed.

## Corrections

### R1: exact early-purge approval binding

`PurgeRequest.HardDelete` and `Force` now require an `EarlyPurgeApproval`
containing the plan ID/revision/digest, approved decision ID, action-run ID,
and the approved trash-entry and action-run versions. The service validates the
shape before touching the journal and routes the claim through the existing
`ClaimApprovedEarlyPurge` sqlc operation. SQLite validates the immutable
plan/revision/manifest target, approved decision, action-run state/deadline,
entry version and coupled leases in the same claim boundary. Missing, stale,
foreign and changed bindings return a conflict before filesystem dispatch.

### R2: exact affected-set evidence

Trash, purge and restore now map every returned affected identity to exactly one
eligible stored item. Foreign, duplicate, missing, ambiguous and changed
identity reports hold the operation and preserve the scope issue in the result
and durable effect evidence. Directory roots are not accepted as a substitute
for their selected file entries, and each operation uses its own original or
trash path namespace. A held result cannot report aggregate `applied` merely
because one response row matched.

### R3: state-specific trash replay

Existing and idempotent trash replays now preserve the durable state: `trashed`
is already satisfied, `planned` remains pending/claimed, `held` remains held,
`failed` is a conflict requiring review, and in-progress or terminally absent
states are not treated as materialized trash. No replay of held or failed work
dispatches another filesystem operation.

### R4: durable client and filesystem evidence

The private client-state JSON envelope is retained when an observed stop is
followed by filesystem finalization. Filesystem evidence is merged into that
existing envelope, preserving connection/external IDs, observed client state,
seeding state and stop outcome/evidence for API/UI read-back.

## Synthetic regressions

- `TestHardPurgeRequiresExactApprovalAndHasZeroDispatchOnBindingFailures`
  covers absent, stale-entry, foreign-plan, foreign-decision and foreign-action
  approval bindings and proves zero payload-delete dispatch.
- `TestHardPurgeUsesBoundApprovalAndExactPayload` creates a synthetic ready
  `fs.delete` plan, exact target, approval and action run, then proves the
  bound early purge reaches only the stored trash item.
- `TestTrashRejectsForeignDuplicateAndChangedAffectedEvidence` and
  `TestPurgeAndRestoreRejectForeignAffectedEvidence` cover foreign, duplicate
  and contradictory effect identities across all three filesystem operations.
- `TestValidateAffectedSetRequiresExactSemanticIdentity` directly covers the
  shared mapping predicate, including missing evidence.
- `TestTrashReplayKeepsHeldAndFailedStatesVisible` proves held/failed replay
  cannot become `OutcomeAlreadySatisfied` or dispatch again.
- `TestTrashPreservesClientAndFilesystemEvidenceInOneStateEnvelope` proves the
  stop envelope and filesystem result coexist after successful trashing.

All fixtures use synthetic IDs, temporary SQLite databases and fake normalized
ports. No private endpoint, credential, cookie, inventory or media path is
present.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -w internal/trash/trash.go internal/trash/correction_round1_test.go` | Passed |
| `GOWORK=off go test ./internal/trash -count=1` | Passed |
| `GOWORK=off go test -race ./internal/trash -count=1` | Passed in 27.833s |
| `GOWORK=off go vet ./internal/trash` | Passed |
| `GOWORK=off go test ./... -count=1` | Passed in 19.0s |
| Product pre-commit hook | Passed: generation, staged generation, bundled and standalone Vacuum, architecture, client boundaries and fast guardrails |
| `git diff --check` | Passed before product commit |

The full aggregate race and Linux cross-build matrix was not rerun in this
bounded correction checkpoint; it remains an independent reviewer/coordinator
gate. The product hook did rerun deterministic generation, staged generation,
all contract Vacuum checks, architecture and fast guardrails.

## Gates and resume

- Product checkpoint: `2f843b3e678d86e9300ce60f48f31e3c87f18791`.
- Handoff commit: pending; this file is the next atomic commit.
- F-05 filesystem trash/restore/permanent-delete operations remain explicitly
  unsupported and fail closed on the reviewed platform path. This correction
  does not broaden that capability.
- qBittorrent metadata removal remains metadata-only and follows selected
  payload evidence. Restore never re-adds or resumes a download.
- Coordinator owns `docs/execution/state.json`, integration and the next
  independent review.
