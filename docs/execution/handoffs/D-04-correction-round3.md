# D-04 correction round three handoff

## Assignment

- Task ID and title: D-04 correction round three, bind the complete verified child set and report through publication.
- Owner: `/root/x05_implementer`; independent reviewer: coordinator-assigned after this checkpoint.
- Dispatch checkpoint: `6cbe18fda5f87b4b6586a9b4821abfc20407efe4`.
- Reviewed source product: `6adb5a5cf2ce7d9effd5d4ba6c2a8737d9e28b3a`.
- Prior review receipt: `e5e51d9d7476c88b54732a0f4a5b42ccd8e8754d`; receipt integrated at `7980ac79d30179d0f6165957b272db7675932ad8`.
- Owned paths: `internal/backup/`, `docs/operations/backup-restore.md`, and this handoff. The coordinator owns `docs/execution/state.json`; this lane did not edit it.
- Required acceptance: A-41, A-43 and A-44, with the backup and restore safety requirements in `data-and-recovery.md`.

## Correction result

The product commit for this correction is
`91e63363c9a2c15bce8964d19508a1888a97ba51` (`fix(backup): bind child
verification to publication`). It preserves the reviewed SQLite, credential,
descriptor, trash and exact source-manifest safeguards while closing the
remaining child-mutation P1.

### Complete child-set binding

- `Create` records the exact generated manifest bytes and digest before writing
  them to its private stage. After the existing private `Verify`, the held
  staging object installs a final content check. That check requires the exact
  manifest bytes and reruns the complete read-only `Verify`, which rejects
  unexpected, missing, replaced, changed or incorrectly-modeled children.
- `Restore` keeps the exact preflight source-manifest bytes and digest. Its
  final private check confirms the source snapshot and staged manifest, reruns
  `Verify`, confirms the source and staged snapshots again, and stores that
  final verified report for return.
- The final private check runs after the existing publication seam and before
  the native no-replace operation. A public Create regression adds an
  unexpected child while retaining the stage root device/inode; Create returns
  incomplete and leaves the destination absent. A public Restore regression
  rewrites only the staged `manifest.json` while retaining the stage root
  device/inode; Restore returns incomplete and leaves the destination absent.
- After native publication, `Create` and `Restore` perform a complete
  destination `Verify` read-back. Restore also requires the destination
  manifest to match the exact source snapshot and replaces its returned report
  with the destination read-back report. A child mutation that occurs after
  the final private check is therefore returned as `ErrPublicationUncertain`
  rather than as a successful result; the visible destination remains for
  read-only reconciliation.
- Existing root-directory descriptor identity, no-follow path checks,
  platform no-replace publication and close-only uncertain-stage cleanup remain
  in force. No pathname cleanup can delete a replacement stage.

## Verification

All Go commands below use `GOWORK=off`; the aggregate gate also uses
`GOPROXY=off GOSUMDB=off`.

| Command or scenario | Result | Evidence |
| --- | --- | --- |
| `gofmt -w internal/backup/*.go`, `git diff --check` | Passed, exit 0 | owned product paths |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed, exit 0; 4.180s on the product tree | focused backup suite |
| `GOWORK=off go test -race -mod=readonly -run 'Test(CreateRejectsChildMutationBeforePublication\\|RestoreRejectsStagingManifestMutationBeforePublication)$' -count=5 -timeout=420s ./internal/backup` | Passed, exit 0; 19.495s | public Create/Restore root-identity-preserving child mutation regressions |
| `GOWORK=off go test -race -mod=readonly -count=3 -timeout=420s ./internal/backup` | Passed, exit 0; 157.044s | repeated backup publication, uncertainty, manifest, tamper and cancellation suite |
| `GOWORK=off go vet -mod=readonly ./internal/backup` | Passed, exit 0 | backup package |
| `GOWORK=off go mod verify` | Passed: `all modules verified` | root module cache |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases; local links resolve | planning graph |
| `GOOS=linux GOARCH=amd64/arm64` and `GOOS=darwin GOARCH=amd64/arm64` with `CGO_ENABLED=0 GOWORK=off go test -mod=readonly -c ./internal/backup` | Passed, exit 0 for all four targets | platform publication files |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Passed, exit 0 | generation and staged generation, API checks, root and standalone Vacuum with zero warnings/errors, architecture, lint, root/UI/tools/client tests, race/vet/mod verification and 18 Linux CGO-free cross-builds |
| `TestCreateRejectsChildMutationBeforePublication` | Passed, including five race repetitions | unexpected child is rejected before native publication; destination absent; stage root identity unchanged |
| `TestRestoreRejectsStagingManifestMutationBeforePublication` | Passed, including five race repetitions | staged manifest mutation is rejected before native publication; destination absent; stage root identity unchanged |
| Live services, credentials, private coordinates, media data or deployment | Not used | synthetic temporary fixtures only |

## Review and resume

- Finding addressed: the complete child set and exact Restore report are now
  checked after the publication seam, and the published destination is read
  back before success. Both public entrypoints fail closed for the reviewed
  root-identity-preserving child mutations.
- Product status: ready for independent review at
  `91e63363c9a2c15bce8964d19508a1888a97ba51`.
- Handoff commit: assigned after this file is committed because a commit
  cannot contain its own object ID; the exact SHA is reported to the
  coordinator after commit.
- Known limitation: Linux and Darwin directory moves remain name-based native
  operations surrounded by held-root identity checks and complete pre/post
  content verification. A mutation observed after native publication is an
  uncertain visible effect and must be reconciled with `Verify`; platforms
  without the reviewed no-replace primitive fail closed. A process crash or
  adversarial mutation after the final read-back is outside deterministic
  fixture control and remains subject to the same recovery/reconciliation
  policy.
- Retained private stages require the existing/future janitor to reclaim them
  with its own object-safe policy. This lane adds no pathname deletion
  fallback.
- The quiescence flag remains operator-enforced, and HTTP/worker backup
  bootstrap remains downstream integration work. These checks do not claim
  live restore readiness.
- Next safe action: coordinator records the exact product and handoff SHAs in
  `docs/execution/state.json` and dispatches an independent review. No live
  restore, deployment or media operation is authorized by this handoff.
