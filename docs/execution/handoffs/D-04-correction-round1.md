# D-04 correction round one handoff

## Assignment

- Task ID and title: D-04 correction round one, close the independent backup and restore review findings.
- Owner: `/root/x05_implementer`; independent reviewer: coordinator-assigned after this checkpoint.
- Dispatch checkpoint: `f1ece6a65f2c990d73748d2fbe2ec3a59e318cd0`.
- Reviewed source product: `9b682b56306d450d582e4ec23f6f8e5e1df37243`.
- Prior review receipt: `ca64d535cb891889553e62ae516a917ef72fc962`; coordinator integration checkpoint: `bfc411a663a430a009d8d7b97864fb502179b9f0`.
- Owned paths: `internal/backup/`, `docs/operations/backup-restore.md`, and this handoff. The coordinator owns `docs/execution/state.json`; this lane did not edit it.
- Required acceptance: A-41, A-43 and A-44, with the backup and restore safety requirements in `data-and-recovery.md`.

## Correction result

The product commit for this correction is
`8c4b223c1e211571c9c2db127f81b1ef6a1919d6` (`fix(backup): enforce verified
isolated recovery boundary`). It addresses all six review findings while
preserving the database-neutral backup format and existing credential,
storage, and trash APIs.

- `Create` copies into a private `0700` staging tree, writes a bounded manifest,
  synchronizes it, and runs the complete read-only `Verify` check before the
  destination can be published. Wrong keys, missing retained descriptor or
  trash references, malformed copied state, and an oversized manifest remain
  private and fail closed. A final source validation and context check run
  immediately before publication.
- Schema support is derived from the highest embedded `*.up.sql` migration
  (`9`). Create, Restore, and Verify reject a dirty, empty, or future database
  or manifest schema. A supported older schema remains eligible for an
  isolated check and the normal forward migration path.
- Restore canonicalizes existing identities and rejects equal or overlapping
  archive and destination paths before staging. It refuses application-
  controlled symlink ancestors and lexical aliases, accepts only the host's
  OS-owned temporary aliases needed by Go test roots on Darwin, and verifies
  that the archive root inode is unchanged before publication.
- Publication uses Linux `renameat2(RENAME_NOREPLACE)` or Darwin
  `renameatx_np(RENAME_EXCL)` against an opened, no-follow parent. There is no
  replace-capable rename fallback. Unsupported platforms return
  `ErrPublicationUnsupported`. A containing-parent sync failure after the
  atomic rename returns `ErrPublicationUncertain`; the destination remains a
  visible effect that must be reconciled with `Verify` before retrying.
- Filesystem walking, hashing, copying, manifest reads and synchronization
  check context between bounded chunks/entries. Capture limits bound aggregate
  entries, trees, regular-file bytes and encoded manifest bytes; callers may
  tighten the built-in ceilings but cannot raise them. Cancellation leaves no
  published destination.

## Verification

| Command or scenario | Result | Evidence |
| --- | --- | --- |
| `gofmt -w internal/backup/*.go` and `git diff --check` | Passed, exit 0 | owned Go and documentation paths |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed, exit 0 | focused backup fixture suite |
| `GOWORK=off go test -race -mod=readonly -count=5 -timeout=420s ./internal/backup` | Passed, exit 0; 220.701s | repeated backup publication, restore, tamper and cancellation suite |
| `GOWORK=off go vet -mod=readonly ./internal/backup` | Passed, exit 0 | backup package |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./internal/...` | Passed, exit 0 | all root internal packages |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./...` | Passed, exit 0 | root package and fixture matrix |
| `GOWORK=off go mod verify` | Passed: `all modules verified` | root module cache |
| `GOWORK=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0, about 81s | generation, API/Vacuum, architecture, lint, all module test/race/vet/mod checks and Linux amd64/arm64 CGO-free matrix |
| `python3 scripts/check-architecture.py` | Passed | root and standalone-client import boundaries |
| `python3 scripts/check_planning.py` | Passed; 53 tasks and 60 acceptance cases | planning consistency |
| `TestCreatePublishesOnlyAfterPrivateVerification` | Passed | wrong key, missing descriptor/trash references and manifest-size refusal leave no destination |
| `TestRestoreRejectsFutureSchemaBeforePublication` | Passed | database and manifest version 999 rejected against embedded ceiling; archive remains unchanged |
| `TestRestoreRejectsArchiveDestinationOverlapAndAliases` | Passed | equal, nested, symlink-alias and lexical-alias cases fail before staging |
| `TestCreateRejectsSymlinkAndLexicalSourceAliases` | Passed | source tree aliases fail closed |
| `TestAtomicPublicationRejectsExistingDestination` | Passed on Darwin host | no-replace collision preserves both stage and destination |
| `TestPublicationParentSyncFailureIsUncertainAndReconciles` | Passed | visible post-rename effect is classified uncertain and Verify succeeds |
| `TestFilesystemWorkHonorsCancellationAndLeavesNoDestination` and `TestRestoreHonorsMidCopyCancellation` | Passed | deterministic mid-walk, mid-copy and restore cancellation leaves no target |
| `GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Passed through CI-equivalent matrix | Linux cross-builds |

The CI-equivalent run also passed the bundled generation check, API checks,
Vacuum with zero warnings/errors for the root and all six standalone OpenAPI
contracts, full lint, architecture, module tests, race tests, vet, module
verification, and Linux amd64/arm64 CGO-free builds. The pre-commit fast gate
passed on the staged product tree before the product commit.

All executable fixtures use synthetic SQLite rows, keys, bytes and temporary
roots. No live service, credential, media inventory, private coordinate or
runtime mount was used.

## Review and resume

- Findings addressed: private verification before Create publication; embedded
  migration ceiling; archive/target overlap and identity protection; atomic
  no-replace publication with visible uncertainty; symlink/lexical alias
  refusal; bounded context-aware filesystem work and aggregate limits.
- Product status: ready for independent review at
  `8c4b223c1e211571c9c2db127f81b1ef6a1919d6`.
- Handoff commit: recorded after this file is committed because Git cannot
  include its own object ID in its contents.
- Remaining product limitation: Darwin and Linux publication are covered by
  their reviewed native primitives; other platforms fail closed. Native
  process-crash, disk-full and cross-device fault injection was not available
  in this synthetic environment. The caller must enforce the quiescence
  boundary before setting `Source.Quiesced`, and an HTTP/worker backup command
  remains downstream integration work.
- Next safe action: coordinator records the product and handoff SHAs in
  `docs/execution/state.json` and dispatches the independent reviewer. No live
  restore or deployment is claimed from these local checks.
