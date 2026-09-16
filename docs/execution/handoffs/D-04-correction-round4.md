# D-04 correction round four handoff

## Assignment

- Task ID and title: D-04 correction round four, fail closed when final
  read-back detects content drift after native publication.
- Owner: `/root/x05_implementer`; independent reviewer: coordinator-assigned
  after this checkpoint.
- Dispatch checkpoint: `dcc450acf0f8d659e498a3e7ea8ded35a94464f7`.
- Reviewed source product: `91e63363c9a2c15bce8964d19508a1888a97ba51`.
- Prior review receipt: `766903206caba0c59e4febed252b2a19e41ccd5b`; receipt
  integrated at `22fd87f285880c7cdacf55ea406b0eba8bfb518c`.
- Owned paths: `internal/backup/`, `docs/operations/backup-restore.md`, and
  this handoff. The coordinator owns `docs/execution/state.json`; this lane
  did not edit it.
- Required acceptance: A-41, A-43 and A-44, with the backup and restore
  safety requirements in `data-and-recovery.md`.

## Correction result

The product commit for this correction is
`036288114bcf303f98e0ee02e54ddce18384e504` (`fix(backup): rollback
publication content drift`). It preserves the reviewed SQLite, credential,
descriptor, trash, exact manifest, schema, overlap, cancellation, bound and
staging-identity safeguards and closes the remaining post-publication drift
case.

### Fail-closed post-publication drift

- `Create` and `Restore` keep their existing complete private final checks and
  exact manifest checks. After native no-replace publication, their post-check
  still performs complete destination read-back and rejects changed child sets,
  manifests or reports.
- When that post-check fails, publication now attempts an identity-checked
  rollback of the operation-owned directory from its requested destination to
  its private stage name. Linux uses a parent-file-descriptor-relative
  `renameat2(RENAME_NOREPLACE)`; macOS uses
  `renameatx_np(RENAME_EXCL)`. Both paths retain the opened stage descriptor,
  require its root identity to match the visible destination, reject an
  occupied stage name, recheck the parent and stage identity, require the
  destination name to be absent, and sync the containing parent.
- A proven rollback returns `ErrIncomplete` with the original post-check error
  wrapped and leaves the requested destination absent. If destination/stage
  identity changes, the rollback primitive is unavailable, or parent sync
  cannot be proven, the operation does not delete by pathname; it returns
  `ErrPublicationUncertain` and retains the visible effect for read-only
  reconciliation.
- The package-local `afterFinalStagingRead` seam mutates the operation-owned
  tree after the final private read and before native rename. `Create` adds an
  unexpected child; `Restore` changes staged `manifest.json` while preserving
  the stage root device/inode. Both public entrypoints now reject the result
  and leave their requested destinations absent on Linux/macOS. The tests skip
  only where the platform lacks the reviewed no-replace publication/rollback
  primitive.
- Existing pre-final-check mutation, post-check uncertainty, stage
  substitution, source-manifest same-inode mutation, parent-sync uncertainty,
  no-replace collision and retained-stage tests remain unchanged and pass.

## Verification

All Go commands below use `GOWORK=off`; the aggregate gate additionally uses
`GOPROXY=off GOSUMDB=off`.

| Command or scenario | Result | Evidence |
| --- | --- | --- |
| `gofmt -w internal/backup/*.go`, `git diff --check` | Passed, exit 0 | owned product paths |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed, exit 0; 5.392s | complete backup suite on product tree |
| `GOWORK=off go test -race -mod=readonly -run 'Test(CreateRollsBackChildMutationAfterFinalRead\\|RestoreRollsBackManifestMutationAfterFinalRead)$' -count=5 -timeout=420s ./internal/backup` | Passed, exit 0; 19.933s | last-final-read Create/Restore regressions |
| `GOWORK=off go test -race -mod=readonly -count=3 -timeout=420s ./internal/backup` | Passed, exit 0; 167.428s | repeated backup publication, tamper, cancellation and uncertainty suite |
| `GOWORK=off go vet -mod=readonly ./internal/backup` | Passed, exit 0 | backup package |
| `GOWORK=off go mod verify` | Passed: `all modules verified` | root module cache |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases; local links resolve | planning graph |
| `GOOS=linux GOARCH=amd64/arm64` and `GOOS=darwin GOARCH=amd64/arm64` with `CGO_ENABLED=0 GOWORK=off go test -mod=readonly -c ./internal/backup` | Passed, exit 0 for all four targets; 2.8s | platform publication files |
| `MSTARR_CACHE_DIR="$(mktemp -d -t mastarr-d04-r4-cache.XXXXXX)" && GOCACHE="$MSTARR_CACHE_DIR" GOWORK=off GOPROXY=off GOSUMDB=off /usr/bin/time -p ./scripts/check-guardrails.sh --ci` | Passed, exit 0; `real 397.00` | fresh-cache generation/staged generation, API and standalone Vacuum with zero warnings/errors, architecture, lint, root/UI/tools/client tests, race/vet/mod verification and 18 Linux CGO-free cross-builds |
| Live services, credentials, private coordinates, media mutation or deployment | Not used | synthetic temporary fixtures only |

## Review and resume

- Finding addressed: a child or manifest mutation injected after the final
  private read and before native rename no longer leaves the requested
  destination visible when the operation-owned object can be safely rolled
  back. The public Create and Restore regressions reproduce the requested
  window and assert destination absence.
- Product status: ready for independent review at
  `036288114bcf303f98e0ee02e54ddce18384e504`.
- Handoff commit: assigned after this file is committed because a commit
  cannot contain its own object ID; the exact SHA is reported to the
  coordinator after commit.
- Known limitation: Linux and Darwin directory moves remain name-based native
  operations. Held root descriptors, same-file checks, parent-FD-relative
  no-replace operations and post-publication read-back protect the reviewed
  boundary, but cannot make a concurrent adversarial exchange invisible between
  separate kernel operations. If rollback cannot prove ownership, the visible
  effect remains uncertain and is retained for reconciliation. Platforms
  without the reviewed no-replace primitive fail closed.
- Retained private stages require the existing/future janitor to reclaim them
  with its own object-safe policy. This lane adds no pathname deletion fallback.
- The quiescence flag remains operator-enforced, and HTTP/worker backup
  bootstrap remains downstream integration work. These checks do not claim
  live restore readiness.
- Next safe action: coordinator records exact product and handoff SHAs in
  `docs/execution/state.json` and dispatches independent review. No live
  restore, deployment or media operation is authorized by this handoff.
