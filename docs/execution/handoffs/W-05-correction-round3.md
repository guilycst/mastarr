# W-05 correction round three handoff

## Assignment

- Task: W-05 correction round three, close R3b from the independent round-three review.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `a68fafb29f07ff0561f1e28b6857a1c3fa0ab311`.
- State dispatch commit: `6c2079a8e5812cd57866e31fdd22d00d5465e1a2`.
- Source product: `730b3774fb412c2d9dcecce5a5dec292d11f63eb`.
- Prior review: `docs/execution/handoffs/W-05-review-round3.md`, R3b P1.
- Product commit: `985be79` (full SHA recorded below).
- Owned product paths: `internal/trash/`.
- Handoff path: `docs/execution/handoffs/W-05-correction-round3.md`.
- Checkout: shared `main`; execution state remains coordinator-owned.

No state, schema, API, adapter, client, worker, live service, credential or
real media files were changed.

## R3b correction

Planned trash intents now have an explicit read-only reconciliation boundary:

- `OperationTrash` and `Service.ReconcileTrash` inspect every selected leaf's
  exact original and mapped trash path before deciding. They never call the
  filesystem action port or download-client mutation port.
- Each item records exact paths, observed identity when available, observation
  time and evidence. The complete observation is durably written into the
  existing purge janitor row's `outcome_json`; the frozen schema has no third
  janitor operation, so no schema change or second queue was introduced.
- Item/aggregate states distinguish `source_only`, `trash_only`,
  `both_present`, `neither_observable`, `changed_identity`, `partial`, and
  directory-leaf evidence. Collisions, identity changes, unavailable paths and
  mixed multi-file outcomes remain held for review.
- A `trash_only` read-back finalizes the planned entry as already materialized,
  marks exact selected items trashed and leaves the normal purge janitor
  lifecycle intact. No filesystem action is dispatched.
- A `source_only` read-back remains planned, returns `ErrUncertain` with
  `Retryable`, and records that a separately authorized retry is required.
  `RetryTrash` (and generic `Retry` with `OperationTrash`) repeats the exact
  read-only check, takes an entry-scoped lease, observes/stops the associated
  client when required, and dispatches only the stored exact manifest.
- Planned replay through `Trash`, including replay without an idempotency key,
  routes through `ReconcileTrash`; it cannot infer safety from `planned` state
  or blindly redispatch.
- `Recover` and bounded `Tick` discover planned entries, clear expired planned
  retry leases, and invoke read-only reconciliation. Held/collision entries
  remain durable manual-review state; they are never auto-retried.
- Because `trash_entries.active_operation` is constrained by the existing
  schema to `purge` or `restore`, an explicit trash retry stores its temporary
  entry lease in the `purge` slot while the janitor row stays queued. Planned
  entries are ineligible for ordinary purge, and the lease is CAS-checked and
  recovered by worker/expiry identity.

Existing approval bindings, idempotency, qBittorrent stop-before-payload and
metadata-only removal remain unchanged. Restore still never re-adds or resumes
a download. G-01 Arr native registration/import writes remain blocked. F-05
trash/restore/permanent-delete capabilities remain fail-closed as unsupported;
this correction does not broaden those filesystem capabilities.

## Synthetic regressions

- `TestPlannedTrashReconciliationSourceOnlyRequiresExplicitRetry` seeds a
  planned pre-dispatch intent, proves both exact paths are observed and no
  action runs, checks Recover/Tick rediscovery, then proves only explicit
  `RetryTrash` dispatches and reaches trashed state.
- `TestPlannedTrashReconciliationFinalizesTrashOnlyWithoutAction` models a
  lost response where only the exact mapped destination exists, proves
  already-satisfied finalization, item state and durable evidence, and proves
  zero action calls.
- `TestPlannedTrashReconciliationHoldsCollisionIdentityAndNeither` covers
  both-present collision, changed source identity and neither-observable
  paths, with held state, per-item effect state and durable disposition.
- `TestPlannedTrashReconciliationPreservesPartialDirectoryEvidenceAcrossRestart`
  covers mixed child states below a directory, per-item source/trash evidence,
  `partial_directory_leaves`, and restart/Tick behavior with no redispatch.

All fixtures use synthetic IDs, temporary SQLite databases and fake normalized
ports. No private endpoint, credential, cookie, inventory or media path is
present.

## Verification

| Check | Result |
| --- | --- |
| `gofmt -w internal/trash/*.go` | Passed |
| `gofmt -l internal/trash/*.go` and `git diff --check` | Passed |
| `GOWORK=off go test ./internal/trash -count=1 -timeout=180s` | Passed |
| `GOWORK=off go test -race ./internal/trash -count=1 -timeout=300s` | Passed; 46.674s |
| `GOWORK=off go test ./... -count=1 -timeout=300s` | Passed; root matrix |
| `GOWORK=off go vet ./...` | Passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` | Passed; all modules verified |
| `python3 scripts/check-architecture.py` | Passed |
| `python3 scripts/check_planning.py` | Passed; 53 tasks, 60 acceptance cases |
| Product pre-commit hook | Passed; generation, staged generation, root/standalone Vacuum, architecture and fast guardrails |
| `git commit -m 'fix(trash): reconcile planned intents durably'` | Passed; hook exit 0 |

The full aggregate race and Linux amd64/arm64 cross-build matrix was not
rerun after this bounded product slice; coordinator/reviewer may repeat it at
the exact product commit. The host's standalone `golangci-lint` binary could
not load the repository config because it was built with Go 1.26 while the
repository targets Go 1.27.1; the pinned repository guardrail path passed its
architecture/fast checks and this host limitation remains explicit.

## Exact checkpoint

- Product full SHA: `985be79` (run `git rev-parse HEAD` after product commit).
- Handoff commit: pending; this file is the next atomic commit.
- Review status: independent round-four review pending.
- Remaining blocker: F-05 reviewed filesystem trash/restore/permanent-delete
  operations remain unsupported and fail closed. G-01 Arr native writes remain
  disabled by policy/evidence.
- Coordinator action: record product and handoff SHAs in
  `docs/execution/state.json`, then dispatch independent review against the
  exact product tree. No state file was edited by this worker.
