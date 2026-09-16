# W-05 independent review, round two

## Decision

`changes_requested`.

Correction closes round-one R4 and the initial happy-path portions of R1-R3,
but three P1 findings and one P2 finding remain. W-05 cannot close A-26, A-27,
or A-57. No product integration is recommended from this checkpoint.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact correction product:
  `2f843b3e678d86e9300ce60f48f31e3c87f18791`.
- Product parent: `c436ce2abe6c16f72227bd6b5c307c106b09ef5e`.
- Product tree: `6be2fed181727fbe9064da19c61a5bc94c6b9867`.
- Exact correction handoff:
  `c8c862f92e32dc894f5273e657323720af777694`; its direct parent is the product.
- Handoff tree: `52ecca0a2f6216678b3504116a6f49a85e92c829`.
- Exact review checkpoint:
  `182a1f6e2bb8d3945754ec43c0e3f39911d1b191`; its direct parent is the handoff.
- Checkpoint tree: `0b0c279bbd38572de5852f2147818dd32519a2e5`.
- Scoped correction diff SHA-256 from round-one product:
  `76586860b9423581f657f6ca22cc790d51e177ac92e898582ef60b823e0c3836`.
- Product-scope archive SHA-256:
  `31d06dbcbb2c673668d4c53f8e064064190aa07e2175362d0bbb9c43a58f68f9`.
- Reviewed product paths: `internal/trash/trash.go` and
  `internal/trash/correction_round1_test.go` only.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30, and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Product paths
have no delta between product and checkpoint. Independent probes ran from a
temporary archive of that checkpoint with synthetic SQLite databases, paths,
media metadata, and ports. Probe source SHA-256:
`dca3095cbf2fba1752e757c042d27f47b7c7f137ff3fd0ddf01831ed9c641aca`.
No product, state, task, schema, generated, shared-worktree, live service, or
real media data was changed.

## Findings

### R1a - P1 - Approved early purge cannot recover after a process loss

Initial hard-delete approval binding now uses the exact approved CAS and its
happy path is sound. Recovery is not wired to the corresponding approved
reconciliation CAS.

After an approved claim succeeds, `Recover` changes the abandoned running
record to reconciliation at `internal/trash/trash.go:593-603`. `Tick` then
always calls the ordinary, approval-free claim at
`internal/trash/trash.go:557-575`. That ordinary claim cannot claim an
approval-bound janitor record. Calling `Purge` again is also blocked because
`claimApprovedEarlyPurge` rejects every already-bound record at
`internal/trash/trash.go:1193-1200`. `Retry` cannot repair the state because
it only requeues a held entry at `internal/trash/trash.go:710-728` and
`internal/trash/trash.go:1481-1508`.

Storage exposes `ClaimApprovedEarlyPurgeReconciliation`, but `internal/trash`
never calls it. Independent
`TestIndependentApprovedEarlyPurgeCannotRecover` claimed an exact approved
hard delete, simulated process loss through `Recover`, and constructed a fresh
service. `Tick` processed zero records; explicit approved `Purge` returned
`ErrConflict`; read-back reported retryable; `Retry` returned
`ErrClaimed`/`ErrConflict`; payload delete calls remained zero. Reproduced
10/10 ordinary and 5/5 race runs.

This leaves an explicitly approved permanent-delete action durably stuck after
the normal crash boundary. Route approval-bound recovery through the immutable
approval reconciliation CAS and prove fresh-process read-before-write recovery,
including lost-response behavior and exact approval revalidation.

### R2a - P1 - A foreign purge effect can be cleared and terminalized as success

The new exact-set validator detects an exact-plus-foreign response, but
`runPurge` records the approved item as `purged` before holding the operation at
`internal/trash/trash.go:1283-1303`. `finishClaim` commits every such terminal
item even when the janitor is held at `internal/trash/trash.go:1393-1408`.

The later read-only reconciliation skips every `purged` item and therefore does
not retain or recheck the foreign effect. With no remaining approved item,
`Reconcile` returns retryable at `internal/trash/trash.go:704-707`; `Retry`
requeues the hold; the next purge sees no payload work and records aggregate
success without another filesystem read or delete.

Independent `TestIndependentForeignPurgeEffectCanBeClearedAsSuccess` returned
one exact and one foreign affected object. First purge held the entry but marked
its only approved item purged. Reconciliation then reported safe-to-retry,
retry succeeded, and a second purge terminalized the entry as `purged` while
delete-call count stayed one. Reproduced 10/10 ordinary and 5/5 race runs.

An out-of-scope effect is unresolved evidence, even if all approved items also
completed. Keep the operation held until the full returned effect has a durable,
explicit resolution. Do not derive retry safety solely from remaining approved
items or overwrite the original foreign-effect evidence with success.

### R3a - P1 - Planned replay without an idempotency key blindly redispatches trash

State-specific replay is applied for idempotency lookups and for existing states
other than `planned`. An existing `planned` entry without an idempotency key is
explicitly allowed through at `internal/trash/trash.go:357-365`, then preflighted
and sent to `FilesystemActionPort.Trash` again. This contradicts the correction
handoff claim that planned replay remains pending/claimed with no dispatch.

Independent `TestIndependentPlannedReplayWithoutKeyRedispatches` created the
same durable state left when a process exits after intent commit but before its
effect is recorded. The identical public `Trash` request made one new action
call and committed `trashed`. Reproduced 10/10 ordinary and 5/5 race runs.

Entry ID and immutable request digest already identify this durable intent.
Treat planned replay as claimed/reconciling regardless of optional API
idempotency-key presence. Require read-back before deciding whether any mutation
can be dispatched again.

### R2b - P2 - Directory manifests can finish with a permanently selected item

`validateAffectedSet` excludes directory items from both expected and missing
sets at `internal/trash/trash.go:1968-2015`. Purge, restore, and reconciliation
also skip directories. A directory-root effect is classified foreign, while a
child-only exact effect can succeed and leave the durable directory item in
`selected` forever.

Independent `TestIndependentDirectoryLifecycleLeavesSelectedDirectory` used an
exact directory manifest with one child. Child-only trash evidence produced a
`trashed` entry while the directory item remained `selected`. Child-only purge
then produced a `purged` entry and still left that item `selected`. Reproduced
10/10 ordinary and 5/5 race runs.

Directory actions must expand to their immutable child scope, as the product
specification requires, and directory bookkeeping must reach an honest terminal
state or be omitted from actionable item state. Add nested-directory trash,
partial purge, restore, root-effect rejection, and remaining-object probes.

## Closed or preserved behavior

- R1 initial binding: absent, stale entry version, foreign plan, decision, or
  action identity reject before filesystem dispatch. Exact approval reaches
  only the stored payload. Storage CAS also binds revision, digest, action-run
  version, deadline, manifest, and entry version.
- R2 initial validation: ordinary non-directory foreign, duplicate, missing,
  ambiguous, and changed identities enter a visible hold. The new findings are
  recovery/state-model defects after that detection.
- R3 non-planned replay: held stays held, failed stays conflict, in-progress
  states stay claimed, and verified trashed state is the only materialized
  trash success. R3a is the remaining planned/no-key branch.
- R4: client association, stop observation/outcome/evidence, and filesystem
  outcome/evidence coexist in the durable state envelope after successful
  trash. The round-one overwrite finding is closed.
- Default/custom retention, ordinary expiry, claim exclusion, cancellation
  persistence, metadata-only client removal ordering, and restore's no-readd /
  no-resume boundary retain passing synthetic coverage.
- F-05 action capability remains unsupported and fail-closed. No Arr adapter or
  write path is imported or called, so G-01 remains intact.
- No generated upstream DTO, direct transport, credential, private coordinate,
  live service, local `replace`, or tracked `go.work` entered this scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identities, ancestry, trees, clean worktree, product-to-checkpoint no-drift, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| `go test ./internal/trash -count=20 -timeout=240s` | Passed. |
| `go test -race ./internal/trash -count=3 -timeout=300s` | Passed; package time `83.962s`. |
| `go vet ./internal/trash` and `go mod verify` | Passed; all modules verified. |
| Independent four-probe suite, `-count=10` | Passed in package time `2.962s`; all four defects reproduced every run. |
| Independent four-probe suite, `-race -count=5` | Passed in package time `33.389s`; all four defects reproduced every run. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| `./scripts/check-guardrails.sh --ci` | Passed, exit 0; final marker `guardrail checks passed (ci)`. Generation, staged generation, root and standalone Vacuum, lint, architecture, root/UI/tools/client tests and race, vet, module verification, and Linux cross-builds passed. |

## Acceptance disposition

- A-25: default/custom retention and ordinary no-early-purge behavior remain
  covered. Startup recovery is unsafe for approved early purge under R1a.
- A-26: blocked by R1a. Exact early deletion works only without interruption;
  its durable crash/restart boundary cannot resume.
- A-27: blocked by R2a, R3a, and R2b. Partial/foreign effects and planned intent
  do not remain recoverable without blind success or redispatch; directory item
  state is incomplete.
- A-29: stop and metadata-only removal ordering pass, and combined client /
  filesystem evidence now persists. No new A-29 finding.
- A-30: restore keeps the client stopped and does not re-add/resume. Directory
  state under R2b remains incomplete for directory-scoped restore workflows.
- A-57: blocked by R1a, R2a, and R3a. Restart and foreign-effect reconciliation
  can either deadlock or authorize a blind terminal transition/redispatch.

Correction should address R1a, R2a, R3a, and R2b with focused durable-state
regressions before another independent review. This receipt does not approve
integration, release, deployment, live filesystem mutation, or live client
control.
