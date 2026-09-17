# W-05 correction round two handoff

## Assignment

- Task: W-05 correction round two, close independent review findings R1a-R3a and R2b.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Source product: `2f843b3e678d86e9300ce60f48f31e3c87f18791`.
- Review receipt: `docs/execution/handoffs/W-05-review-round2.md`.
- Dispatch checkpoint: `8df72860d4ef56e69393753a3814c288bb9860d2`.
- Product commit: `730b3774fb412c2d9dcecce5a5dec292d11f63eb`.
- Owned product paths: `internal/trash/`.
- Handoff path: `docs/execution/handoffs/W-05-correction-round2.md`.
- Checkout: shared `main`; execution state remains coordinator-owned.

No state, schema, API, adapter, client, worker, live service, credential or
real media files were changed.

## Corrections

### R1a: exact approved early-purge recovery

Approved hard-delete claims now perform a fresh read-only reconciliation before
payload mutation. `Recover` leaves the durable approval binding in
`reconciling`; `Tick` and explicit approved `Purge` reacquire that record
through the existing `ClaimApprovedEarlyPurgeReconciliation` CAS. The CAS
retains the exact plan, revision, digest, decision, action-run, entry and
action-run-version binding, and the service revalidates the current entry before
dispatch. A failed, missing or ambiguous read releases the owner lease to
durable reconciliation with evidence and a retry deadline. A fresh service can
then complete the exact approved purge only after exact read-back; a foreign
approval cannot claim or dispatch it.

### R2a: unresolved affected-set evidence

Purge treats every foreign, duplicate, contradictory or otherwise invalid
affected identity as unresolved, even when the response also contains the exact
approved item. The exact item is not marked purged, aggregate success is not
recorded, and the durable outcome carries `scope_unresolved` plus the original
scope evidence. Reconciliation and retry preserve the hold and refuse a second
delete until a read-only path proves the complete effect safe.

### R3a: planned replay without an idempotency key

An existing planned trash entry is now replayed as the same durable intent for
all callers, including requests without an idempotency key. It remains visible
as planned/claimed and does not preflight and redispatch the filesystem action.
Recovery must use the durable reconciliation path before any later mutation.

### R2b: directory child scope bookkeeping

Directory manifests are expanded to their immutable leaf scope for actionable
trash items. Directory roots are retained in the entry manifest for provenance
but are not persisted as independently selected items. Partial child effects
hold the entry, directory-root effects are rejected as out of scope, and
complete trash, purge and restore reach terminal state for every selected leaf
without a permanently selected directory row. A root-only directory manifest
is rejected because it has no actionable file or subtitle leaf.

## Synthetic regressions

- `TestApprovedEarlyPurgeRecoversWithExactBindingAndReadBack` covers an exact
  approval claim, process-loss recovery, foreign approval rejection, fresh
  worker lost-read hold, durable reconciliation evidence, and exact read-back
  before the single successful delete.
- `TestPurgeForeignEffectStaysHeldAndCannotBeRetriedBlindly` covers an exact
  plus foreign affected response, proves the exact item is not terminalized,
  and proves reconciliation/retry do not dispatch a second delete.
- `TestPlannedTrashReplayWithoutIdempotencyKeyDoesNotDispatch` covers durable
  planned intent replay with no idempotency key and proves zero new trash calls.
- `TestDirectoryManifestUsesExpandedLeafScope` covers partial child trash,
  root-effect rejection, complete nested purge, and nested restore with no
  directory bookkeeping row left selected.

All fixtures use synthetic IDs, temporary SQLite databases and fake normalized
ports. No private endpoint, credential, cookie, inventory or media path is
present.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -l internal/trash/*.go` | Passed |
| `git diff --check` | Passed before product commit |
| `GOWORK=off go test ./internal/trash -count=1 -timeout=180s` | Passed in 1.951s |
| `GOWORK=off go test -race ./internal/trash -count=1 -timeout=240s` | Passed in 41.052s |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/trash -count=5 -timeout=300s` | Passed in the focused correction run |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./internal/... -count=1 -timeout=180s` | Passed in the bounded aggregate run |
| `GOWORK=off go vet ./internal/trash` | Passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | Passed: all modules verified |
| Product pre-commit hook | Passed: generation, staged generation, bundled and standalone Vacuum, architecture, client boundaries and fast guardrails |

The full aggregate race and Linux cross-build matrix was not rerun in this
bounded correction checkpoint; it remains an independent reviewer/coordinator
gate. The product hook reran deterministic generation, staged generation, all
contract Vacuum checks, architecture and fast guardrails.

## Gates and resume

- Product checkpoint: `730b3774fb412c2d9dcecce5a5dec292d11f63eb`.
- Handoff commit: pending; this file is the next atomic commit.
- F-05 filesystem trash/restore/permanent-delete operations remain explicitly
  unsupported and fail closed on the reviewed platform path. This correction
  does not broaden that capability.
- qBittorrent metadata removal remains metadata-only and follows selected
  payload evidence. Restore never re-adds or resumes a download.
- Coordinator owns `docs/execution/state.json`, integration and the next
  independent review.
