# W-05 correction round four handoff

## Assignment

- Task: W-05 correction round four, close R3c from the independent round-four review.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `03d06218a36d19b2f504731664c35b8d9fd5b06f`.
- State dispatch commit: `cd6fb94e5496d2cb0c91a78ac0fe496df7148b85`.
- Source product under review: `985be793b184f090599b2ee7aea3860993091139`.
- Prior handoff: `735086cd7123ba2527f02dc7346a606ab79c6ed0`.
- Prior review: `docs/execution/handoffs/W-05-review-round4.md`, finding R3c P1.
- Product commit: `2ba78ca03c1b3f2afa31949782f75af012d8aab6`.
- Owned product paths: `internal/trash/`.
- Handoff path: `docs/execution/handoffs/W-05-correction-round4.md`.
- Checkout: shared `main`; execution state remains coordinator-owned.

No state, schema, API, adapter, client, worker, live service, credential or
real media files were changed.

## R3c correction

Explicit trash retries now preserve trustworthy per-item effects across a
partial filesystem result:

- `finishClaimedTrash` derives the pending set from durable item states before
  validating the returned affected set. Every identity-valid affected item with
  a valid effect outcome is atomically marked `trashed`, even when another
  selected item is omitted or the action returns an error.
- The returned result and durable janitor outcome contain one item-bound
  `trashed` effect for each matched item and one `pending` effect for each
  unresolved item. An invalid outcome never terminalizes an item. Observed
  time, action error evidence and affected-scope evidence remain visible.
- A partial result keeps the entry `held` and releases the lease. Foreign,
  duplicate, ambiguous and identity-contradictory affected entries add the
  durable `scope_unresolved` marker; an exact match may remain terminal while
  the unresolved scope prevents retry or finalization.
- `ReconcileTrash` accepts only a held entry whose durable hold reason and
  purge-journal outcome identify the explicit trash retry path. It performs
  only exact original/destination reads, preserves terminal trash-only items,
  and returns an explicit retryable source-only result only when no hard scope
  evidence remains.
- A fresh `RetryTrash` claim reconstructs an exact leaf manifest from the
  immutable stored manifest and the still-pending item rows. It cannot submit
  already-trashed items or a directory wildcard. The remaining item set is
  read back again before the one authorized filesystem dispatch.
- Expired retry leases on planned or held retry entries are recoverable through
  the existing entry-scoped purge lease slot. Ordinary held trash states and
  ordinary purge/restore flows remain outside this reconciliation path.

The existing planned-intent reconciliation, approval binding, idempotency,
qBittorrent stop-before-payload and metadata-only removal behavior remain
unchanged. Restore still never re-adds or resumes a download. G-01 Arr native
registration/import writes remain disabled. F-05 filesystem trash/restore and
permanent-delete capabilities remain fail-closed as unsupported; this
correction does not broaden those gates.

## Synthetic regressions

- `TestRetryTrashPersistsExactPartialEffectsAndRetriesOnlyRemainingItems`
  returns an identity-valid first effect plus a partial action error, proves
  the first item is durably `trashed`, the second remains selected/pending,
  and proves a fresh service performs read-only reconciliation before sending
  a request containing only the second item.
- `TestRetryTrashPartialForeignOrDuplicateKeepsMatchedItemAndBlocksReconciliation`
  covers exact-plus-foreign and exact-plus-duplicate reports. Each preserves
  the exact matched item, leaves the remaining item selected, records
  `scope_unresolved`, and keeps a later held reconciliation non-retryable with
  no second filesystem call.
- Existing round-three planned/restart, directory-leaf, collision,
  changed-identity, trash-only and neither-observable regressions remain in
  the same package and continue to pass.

All fixtures use synthetic manifests, temporary SQLite databases and fake
normalized ports. No private endpoint, credential, cookie, inventory or live
media path is present.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -w internal/trash/trash.go internal/trash/trash_test.go internal/trash/correction_round4_test.go` | Passed |
| `gofmt -l ...` and `git diff --check` | Passed |
| `GOWORK=off go test ./internal/trash -count=1 -timeout=180s` | Passed |
| `GOWORK=off go test -race ./internal/trash -run 'TestRetryTrash' -count=5 -timeout=180s` | Passed; 25.219s |
| `GOWORK=off go test -race ./internal/trash -count=1 -timeout=180s` | Passed; 51.937s |
| `GOWORK=off go test ./... -count=1 -timeout=300s` | Passed |
| `GOWORK=off go vet ./...` | Passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | Passed; all modules verified |
| `python3 scripts/check-architecture.py` | Passed |
| `python3 scripts/check_planning.py` | Passed; 53 tasks, 60 acceptance cases |
| Product pre-commit hook | Passed; generation, staged generation, Vacuum, architecture and fast guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed; final marker `guardrail checks passed (ci)` |

The CI-equivalent aggregate included root and nested module tests/races,
lint/architecture, generation and Vacuum, module verification, and Linux
amd64/arm64 CGO-free cross-builds. No live services or credentials were used.

## Exact checkpoint

- Product full SHA: `2ba78ca03c1b3f2afa31949782f75af012d8aab6`.
- Handoff commit: pending; this file is the next atomic commit.
- Review status: independent round-five review pending.
- Remaining blockers: the reviewed F-05 filesystem trash/restore/permanent
  delete operations remain unsupported and fail closed; G-01 Arr native writes
  remain disabled by policy/evidence.
- Coordinator action: record product and handoff SHAs in
  `docs/execution/state.json`, then dispatch independent review against the
  exact product tree. No state file was edited by this worker.
