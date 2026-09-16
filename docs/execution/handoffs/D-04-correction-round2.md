# D-04 correction round two handoff

## Assignment

- Task ID and title: D-04 correction round two, bind verified recovery staging and the restore manifest snapshot.
- Owner: `/root/x05_implementer`; independent reviewer: coordinator-assigned after this checkpoint.
- Dispatch checkpoint: `744a2cefe25e3dc491b5d8c865892c1ea5997b8d`.
- Reviewed source product: `8c4b223c1e211571c9c2db127f81b1ef6a1919d6`.
- Prior review receipt: `ec768b048cb46121c8bd09ea87d79dc79de2308e`; receipt integrated at `04eac468167386f4810bb327941785a7c90ea4d5`.
- Owned paths: `internal/backup/`, `docs/operations/backup-restore.md`, and this handoff. The coordinator owns `docs/execution/state.json`; this lane did not edit it.
- Required acceptance: A-41, A-43 and A-44, with the backup and restore safety requirements in `data-and-recovery.md`.

## Correction result

The product commit for this correction is
`6adb5a5cf2ce7d9effd5d4ba6c2a8737d9e28b3a` (`fix(backup): bind recovery
publication and manifest snapshots`). It preserves the reviewed SQLite,
credential, descriptor and trash format and closes both round-two P1 findings.

### Verified staging identity

- `Create` and `Restore` open the operation-created private stage and retain a
  descriptor plus its `fs.FileInfo` identity through verification, publication
  and return. The descriptor is chmodded directly and is closed only when the
  operation ends.
- Each publication boundary checks both the open descriptor and an `Lstat` of
  the stage pathname, rejects symlink substitution, and requires `os.SameFile`.
  The Linux and Darwin native no-replace calls receive this held stage object
  and an opened no-follow destination-parent descriptor.
- A test seam exchanges the stage pathname after the first check. The second
  identity check rejects the operation before the native call, leaves the
  destination absent and leaves the replacement stage intact.
- After native rename, publication reads the destination with `Lstat` and
  requires the same object identity. A mismatch is an
  `ErrPublicationUncertain` result and the visible destination is left for
  read-only reconciliation. Parent-sync failure has the same uncertainty
  behavior. No deferred pathname `RemoveAll` remains: failed or uncertain
  private stages are closed and retained for a janitor, so a foreign
  replacement cannot be deleted.
- Existing Linux `renameat2(RENAME_NOREPLACE)` and Darwin
  `renameatx_np(RENAME_EXCL)` behavior remains unchanged for ordinary
  destination collisions; unsupported platforms still fail closed.

### Exact restore manifest snapshot

- `Restore` reads and strictly validates the archive manifest once before
  staging, retains the exact bytes and SHA-256 digest, and writes those bytes
  to the private stage with an exclusive, bounded, synced write.
- Artifact copying no longer rereads a mutable `manifest.json`; it consumes
  the preflight byte snapshot. Restore rereads the archive after copy and
  after isolated `Verify`, requiring both exact bytes and digest equality.
- The staged manifest is strictly reread and must match the preflight bytes
  and digest. `RestoreReport.Manifest` is the manifest proven by that staged
  read-back, rather than a stale preflight value.
- A synthetic multi-chunk test mutates the archive manifest in place on the
  same inode while the snapshot is copied. Restore returns `ErrIncomplete`
  and publishes no destination, even though the archive root inode is
  unchanged.

## Verification

All Go commands below use `GOWORK=off`; the aggregate gate also uses
`GOPROXY=off GOSUMDB=off`.

| Command or scenario | Result | Evidence |
| --- | --- | --- |
| `gofmt -w internal/backup/*.go`, `git diff --check` | Passed, exit 0 | owned product paths |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed, exit 0; 5.371s on final product tree | focused backup suite |
| `GOWORK=off go test -race -mod=readonly -count=3 -timeout=420s ./internal/backup` | Passed, exit 0; 138.701s | repeated publication, uncertainty, manifest mutation and prior backup regressions |
| `GOWORK=off go vet -mod=readonly ./internal/backup` | Passed, exit 0 | backup package |
| `GOWORK=off go mod verify` | Passed: `all modules verified` | root module cache |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases; local links resolve | planning graph |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0 | generation and staged generation, API checks, root and standalone Vacuum with zero warnings/errors, architecture, lint, root/UI/tools/client tests, race/vet/mod verification and 18 Linux CGO-free cross-builds |
| `GOOS=linux GOARCH=amd64/arm64` and `GOOS=darwin GOARCH=amd64/arm64` with `CGO_ENABLED=0 GOWORK=off go test -mod=readonly -c ./internal/backup` | Passed, exit 0 for all four targets | platform publication files |
| `TestPublicationRejectsSubstitutedStagingDirectory` | Passed, including race repetitions | exchanged stage is rejected before publication; destination absent and replacement retained |
| `TestPublicationUncertaintyRetainsReplacementStagingDirectory` | Passed, including race repetitions | post-rename parent-sync uncertainty does not remove replacement pathname |
| `TestRestoreRejectsSameInodeManifestMutation` | Passed, including race repetitions | multi-chunk same-inode manifest mutation returns incomplete and publishes no target |
| Live services, credentials, private coordinates, media data or deployment | Not used | synthetic temporary fixtures only |

## Review and resume

- Findings addressed: staging object identity is retained and checked through
  publication/uncertainty; unsafe pathname cleanup is removed; restore is
  bound to exact preflight manifest bytes/digest/value and rejects same-inode
  mutation before publication.
- Product status: ready for independent review at
  `6adb5a5cf2ce7d9effd5d4ba6c2a8737d9e28b3a`.
- Handoff commit: assigned after this file is committed because a commit
  cannot contain its own object ID; the exact SHA is reported to the
  coordinator after commit.
- Known platform limitation: Linux and Darwin expose name-based directory
  rename primitives rather than a universally available descriptor-bound
  directory move. This implementation retains the verified descriptor,
  cross-checks the source name immediately before the native no-replace call,
  verifies the destination object after the call, and returns uncertainty
  without pathname cleanup if a concurrent exchange is observed. Platforms
  without the required native no-replace primitive fail closed. A native
  process crash, disk-full fault, or adversarial exchange after the final
  kernel boundary is not deterministically injectable here; the visible
  result remains subject to read-only reconciliation.
- Retained private stages require the existing/future janitor to reclaim them
  using its own object-safe policy. This lane intentionally does not add a
  pathname deletion fallback.
- The quiescence flag remains operator-enforced, and HTTP/worker backup
  bootstrap remains downstream integration work. These checks do not claim
  live restore readiness.
- Next safe action: coordinator records the exact product and handoff SHAs in
  `docs/execution/state.json` and dispatches an independent review. No live
  restore, deployment or media operation is authorized by this handoff.
