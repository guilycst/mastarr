# W-05 independent review, round one

## Decision

`changes_requested`.

Three P1 findings and one P2 finding remain. W-05 cannot close A-26, A-27,
A-29, or A-57 in this state. Default retention, ordinary expiry gating,
single-claim behavior, metadata-only client removal ordering, and restore's
no-readd/no-resume boundary passed their supplied tests, but the findings below
block integration.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `37ecdde36b56a6cd3d7d147cd8af1b49eba3f45a`.
- Product parent: `c4d2bfa035ff75c2fc84a76e18cf15d59e333c09`.
- Product tree: `ae1f80f880b8d85bf17dff4d51db4085e651ce07`.
- Exact handoff: `91038f799d894b58d1c7f149606ac2c686515beb`;
  its direct parent is the product.
- Handoff tree: `8837caa5ba069c049c89d46546b9d0bf9504686e`.
- Exact review checkpoint:
  `0f16f47a81631ff31cb666b6b7fd8117f08bf53b`;
  its direct parent is the handoff.
- Checkpoint tree: `fab205350d003b348d17ccb04539997166f868b0`.
- Reviewed product paths: `internal/trash/trash.go` and
  `internal/trash/trash_test.go` only.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30, and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Product
paths have no delta between product and checkpoint. Independent probes ran in
a temporary archive of that checkpoint and used synthetic SQLite databases,
paths, media metadata, and ports. Probe source SHA-256:
`68da0b0c7d2b0edec1115263ecf28d1ab68f517344c5c99a9786dfb28322334b`.
No product, state, task, schema, generated, shared-worktree, live service, or
real media data was changed.

## Findings

### R1 - P1 - Early permanent deletion has no approved claim path and cannot execute

`PurgeRequest.HardDelete` and `Force` are caller-controlled booleans. `Purge`
uses either value to bypass retention at `internal/trash/trash.go:475-486`.
The hard branch at `internal/trash/trash.go:1125-1169` explicitly rejects an
approval-bearing janitor row, manually claims the trash entry, then updates the
generic janitor record. It never accepts plan revision, digest, decision,
action-run, or approved entry version, and never calls the existing
`ClaimApprovedEarlyPurge` storage CAS.

The resulting path is both unauthoritative and nonfunctional. The manual entry
claim causes the janitor claim trigger to reject the following janitor update;
the public call returns an error wrapping `ErrStorage`, rolls back, and makes
zero delete calls. Independent
`TestIndependentHardDeleteHasNoApprovedClaimPath` reproduced this 20/20
ordinary runs and 5/5 race runs before expiry. The janitor remained unbound:
`approval_plan_id IS NULL`.

This conflicts with `docs/specs/spec-001-media-reconciliation/http-api.md:81-84`,
which defines direct hard delete as a standalone approved `fs.delete` plan,
and with A-26. Replace the boolean authority with exact immutable approval
binding and the approved early-purge CAS, or remove this claimed capability
until composition supplies that binding. Add rejection probes for absent,
stale, foreign, and changed approval identities plus a successful exact-bound
early purge.

### R2 - P1 - Expanded filesystem effect reports are accepted as exact success

Trash, purge, and restore verify that each selected item appears in returned
`Affected`, but never reject additional, duplicate, or contradictory entries:

- `finalizeTrash` converts the report to a map and checks only selected items
  at `internal/trash/trash.go:943-975`;
- `runPurge` checks only the current item at
  `internal/trash/trash.go:1235-1252`;
- `runRestore` accepts the first matching destination at
  `internal/trash/trash.go:1310-1321`.

Independent `TestIndependentTrashAcceptsForeignAffectedEvidence` supplied the
approved item plus `downloads/unselected.mkv`; the service committed the entry
as `trashed/applied`. `TestIndependentPurgeAcceptsForeignAffectedEvidence`
supplied the selected trash item plus an object under another trash entry; the
service committed `purged`. Both reproduced 20/20 ordinary and 5/5 race runs.

Returned effect evidence is the coordinator's record of actual scope. A
foreign effect must not be silently discarded while terminalizing an exact
manifest. Require exact semantic set equality, reject duplicates and
contradictory identities, and preserve the full report as uncertain evidence.
Add the same matrix for restore. This blocks A-27 and A-57 because expanded or
ambiguous effects currently become terminal success rather than a visible
hold.

### R3 - P1 - A held trash entry replays as already satisfied without materialization

When the stop prerequisite fails, `holdPlanned` stores `held` and no filesystem
trash call occurs. A same-request replay without an idempotency key enters
`internal/trash/trash.go:340-346`; every existing state except `planned`,
including `held` and `failed`, returns `OutcomeAlreadySatisfied` with nil
error.

Independent `TestIndependentHeldTrashReplayClaimsAlreadySatisfied` forced a
synthetic stop failure. First call returned a held entry and error with zero
trash calls. Second identical call returned nil error,
`OutcomeAlreadySatisfied`, state `held`, and still zero trash calls. Reproduced
20/20 ordinary and 5/5 race runs.

`already_satisfied` is reserved for proven desired state. A held/failed entry
must remain held/failed or enter explicit read-only reconciliation; it cannot
authorize dependent workflow progress. Make replay state-specific and add
held, failed, planned, trashed, restored, purging, and purged replay coverage.
This blocks A-27 and A-57.

### R4 - P2 - Successful trash finalization erases durable client-stop evidence

`persistClientObservation` records client state, seeding state, observed time,
stop outcome, operation ID, and evidence at
`internal/trash/trash.go:1066-1080`. `finalizeTrash` then calls
`effectState`; `effectState` rebuilds `client_state_json` from
`entryClientState` at `internal/trash/trash.go:2017-2035`, which retains only
connection/external IDs before adding filesystem evidence. The verified stop
observation is overwritten in the same successful Trash call.

Independent `TestIndependentTrashOverwritesStopEvidence` observed an active
synthetic torrent, stopped it, completed trash, then inspected SQLite. Both
`$.stop` and `$.state` were absent. Reproduced 20/20 ordinary and 5/5 race
runs.

Merge filesystem evidence into the durable client-state object instead of
reconstructing it from the reduced public projection. Preserve stop and
missing-record evidence through trash completion so A-29/A-57 and the UI can
distinguish association, verified stopped state, and later metadata removal.

## Other reviewed behavior

- Default 30-day retention begins after accepted trash effect. Ordinary purge
  and periodic tick do not claim before recorded expiry.
- Normal purge and restore share SQLite claim/lease exclusion. Expired running
  janitor records enter reconciliation before another claim.
- Filesystem identity and original-destination collision checks are present.
- Purge asks the normalized client port to stop before payload work and calls
  metadata-only `Remove` only after selected payload effects.
- Restore issues no add/resume client operation and skips already purged items.
- Missing client records are treated as already-satisfied metadata state.
- F-05 remains fail-closed: its reviewed implementation reports unsupported
  before pending trash, restore, or permanent-delete mutation. This receipt
  does not broaden that capability.

These observations do not close the findings above. Producer coverage contains
only four tests and has no hard-delete approval, restore, partial failure,
foreign/duplicate effect, cancellation, restart, clock anomaly, idempotency
failure replay, or durable evidence regression matrix.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identities, ancestry, trees, clean worktree, product-to-checkpoint no-drift, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| `go test ./internal/trash -count=1 -timeout=180s` | Passed. |
| `go test -race ./internal/trash -count=1 -timeout=180s` | Passed in `7.736s`. |
| `go vet ./internal/trash` | Passed. |
| `go mod verify` | Passed; all modules verified. |
| Independent five-probe suite, `-count=20` | Passed in `6.211s`; every unsafe/incorrect behavior reproduced on every run. |
| Independent five-probe suite, `-race -count=5` | Passed in `38.572s`; every behavior reproduced on every run. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| `./scripts/check-guardrails.sh --ci` | Passed, exit 0; final marker `guardrail checks passed (ci)`. Generation, staged generation, all Vacuum checks, architecture, lint, root and nested module tests/race/vet/module verification, and Linux cross-builds passed. |

## Acceptance disposition

- A-25: implementation evidence exists for default retention and ordinary
  no-early-purge behavior. Startup/periodic recovery needs broader correction
  coverage but has no separate P1/P2 finding in this round.
- A-26: blocked by R1. Explicit approved early purge is neither bound nor
  executable through W-05.
- A-27: blocked by R2 and R3. Expanded effects can terminalize exact scope,
  and held state can falsely replay as satisfied.
- A-29: blocked by R4. Stop is performed in the supplied happy path, but its
  durable evidence is erased; metadata-only removal ordering otherwise passed.
- A-30: restore contains no re-add/resume call and preserves client association,
  but broader partial-restore correction coverage remains required.
- A-57: blocked by R2-R4. Ambiguous scope and held replay are not represented
  honestly, and prerequisite evidence is lost.

Correction should address R1-R4 and add focused synthetic regressions before
another independent review. This receipt does not approve integration,
release, deployment, live filesystem mutation, or live client control.
