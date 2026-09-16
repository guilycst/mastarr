# D-04 correction round one independent review

Decision: **changes_requested**.

The correction closes the wrong-key/private-verification, future-schema,
archive/target overlap, symlink-ancestor, ordinary no-replace collision, and
bounded cancellation cases. Two P1 data-integrity findings remain. Publication
is still bound only to a staging pathname, so an unverified replacement can be
published and uncertainty cleanup can delete another actor's replacement at
that pathname. Restore also copies a mutable manifest independently of the
manifest it preflighted, allowing an in-flight manifest change to be published
while the returned report describes the earlier manifest.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`, independent of correction author
  `/root/x05_implementer`.
- Exact correction product:
  `8c4b223c1e211571c9c2db127f81b1ef6a1919d6`; tree
  `2d8fe37713846d8e742e6f970d51c9697e427a70`.
- Exact correction handoff:
  `d8299c9fc8e9dd7b1d4e5dd3f7fe0257468b323e`; tree
  `5197a3651839f57616922c38b1c852d080a94c5f`.
- Exact review dispatch checkpoint:
  `39c7e40bf9037f671f969c27fbb5296885d05499`; tree
  `aea208f3b35c64df5e27fe983d4c42f1b3ada48e`.
- Prior independent receipt:
  `ca64d535cb891889553e62ae516a917ef72fc962`.
- Correction-scope diff SHA-256:
  `1514aab13c9755d98716235f4233c672766617b3e752082b77608f589002bce9`.
- Reviewed paths: `internal/backup/`,
  `docs/operations/backup-restore.md`, the correction handoff, and the prior
  receipt. Acceptance contributions: A-41, A-43 and A-44.
- Review receipt checkpoint: the commit containing this file; its exact SHA is
  reported after commit because a commit cannot contain its own SHA.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code d8299c9 39c7e40 -- internal/backup
docs/operations/backup-restore.md` passed. Temporary adversarial probes were
removed after execution. Product, execution state, generated output and the
shared checkout were not edited.

## Findings

### P1: verified staging identity is not bound through publication or cleanup

`Create` and `Restore` now verify their private staging trees, but retain only
the staging pathname (`backup.go:269-282`, `408-421`).
`atomicPublishDirectory` revalidates whatever directory currently occupies
that pathname and then calls the native rename by basename
(`publish_darwin.go:15-45`, `publish_linux.go:15-45`). It has no expected
device/inode identity from the tree that passed `Verify`. The deferred cleanup
is likewise an unconditional `os.RemoveAll(staging)` against the pathname.

Two independent deterministic probes ran five times under the race detector:

1. Create a verified stage with marker `verified-original`, rename it aside,
   and create a different directory at the same stage pathname with marker
   `unverified-replacement`. `publishDirectory` returned success and the
   destination contained `unverified-replacement`.
2. Run the public `Create` path and retain its private staging pathname. Inject
   a containing-parent sync failure; after native rename, create a foreign
   directory at the now-vacant old staging pathname. `Create` correctly
   returned `ErrPublicationUncertain` and its published destination passed
   `Verify`, but the deferred cleanup deleted the foreign replacement in all
   five repetitions.

The native `RENAME_EXCL`/`RENAME_NOREPLACE` fix protects the destination name;
it does not bind the verified source object. The second behavior is concrete
data loss during the correction's new uncertainty path. This leaves the prior
round-one requirement to bind publication and cleanup to the staging object
open.

Required change: retain an object-bound staging capability from creation
through verification, native publication and cleanup. A safe design can use a
held descriptor for a private per-operation source directory and perform the
native rename relative to that descriptor and the held destination-parent
descriptor. Cleanup must remove only the object proven to be owned by this
operation and must not remove the old pathname after publication has made it
vacant. Sync the same held destination-parent descriptor used for publication.
Add exact pre-publication substitution and post-rename uncertainty-replacement
regressions.

Disposition: `current_blocker`.

### P1: restore can publish a different manifest than its preflight and report

`Restore` parses and preflights `manifest` (`backup.go:397-406`). Later,
`copyManifestTreeContext` copies the mutable source `manifest.json` without
binding its bytes or parsed value to that preflight manifest
(`backup.go:1669-1684`). `Verify` then validates whichever manifest was copied
into staging (`backup.go:428-430`). The final archive check compares only the
archive root inode (`backup.go:432-438`), and success overwrites the verified
staging report with the older preflight manifest (`backup.go:446`).

Independent deterministic probe:

1. Pad a valid archive manifest with allowed leading whitespace so copying
   requires multiple 64 KiB chunks.
2. After the first manifest copy chunk, change `created_at` in place from 2026
   to 2027 without changing file size or the archive root inode.
3. Complete `Restore` and inspect the published manifest and returned report.

All five race-detector repetitions returned success. The target contained and
passed `Verify` with the changed 2027 manifest, while
`report.Manifest.CreatedAt` retained the preflight 2026 value. The archive
root identity comparison did not detect the mutation.

This violates the strict tamper-failure and exact recovery-evidence contract.
The caller cannot know which manifest the successful report represents.

Required change: bind the exact preflight manifest bytes or their digest to the
restore copy and final report. Copy artifacts against that immutable manifest,
reject any source-manifest change, and return the manifest proven by the final
staging verification. A stronger archive snapshot/descriptor binding should
also prevent same-root child replacement during restore. Add the multi-chunk
same-inode manifest mutation fixture and assert zero published target.

Disposition: `current_blocker`.

## Prior finding disposition

- **Private verification before `Create` publication: functionally closed.**
  `Create` now runs full `Verify` while staging is private. Wrong keys, missing
  descriptor/trash references and an over-limit manifest leave no destination.
  The verified object still needs the publication binding in P1 above.
- **Supported schema ceiling: closed.** The ceiling is derived from embedded
  upward migrations. Equal database/manifest version 999 is rejected before
  staging publication; supported versions and dirty/mismatch rejection remain.
- **Archive/target overlap and aliases: closed.** Equal/nested paths, lexical
  aliases, application-controlled symlink ancestors and source aliases fail
  before publication. Canonical overlap uses the resolved missing-leaf parent.
- **Atomic no-replace and post-rename uncertainty: partially closed.** Darwin
  `RENAME_EXCL` and Linux `RENAME_NOREPLACE` preserve an existing destination;
  unsupported platforms fail closed. Parent-sync failure is correctly exposed
  as `ErrPublicationUncertain` and the visible destination reconciles. Source
  identity and cleanup remain unsafe as detailed above.
- **Context and aggregate bounds: closed for the reviewed paths.** File copy,
  hashing, manifest reads, tree walks and directory syncs check context in
  bounded chunks or entries. Entry/tree/byte/manifest ceilings are enforced,
  and callers may tighten but not raise them. Deterministic mid-copy,
  mid-walk and restore cancellation leaves no destination.

The quiescence flag remains an operator-enforced boundary as documented; this
receipt does not claim the package can prove external source writers stopped.

## Independent checks

All Go checks used `GOWORK=off`; the aggregate check additionally used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identity, ancestry, trees, correction scope and unchanged post-handoff product | Passed. |
| Stage substitution plus uncertainty-cleanup replacement probes, `go test -race -count=5` | Unsafe behavior reproduced in every repetition; suite exit 0. |
| Multi-chunk same-inode manifest mutation probe, `go test -race -count=5` | Restore succeeded in every repetition with published/report manifest disagreement; suite exit 0. |
| `go test -mod=readonly -count=1 -timeout=300s ./internal/backup` | Passed, package 6.792s. |
| `go test -race -mod=readonly -count=1 -timeout=420s ./internal/backup` | Passed, package 48.241s. |
| `go vet -mod=readonly ./internal/backup` | Passed. |
| Root `go test -mod=readonly -count=1 -timeout=300s ./...` and `go mod verify` | Passed; all modules verified. |
| Linux amd64/arm64 CGO-free root builds | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed; generation and staged generation, root plus standalone Vacuum with zero warnings/errors, API, architecture, lint, all nine module tests/race/vet/mod checks and 18 Linux cross-builds completed. Root storage race completed in 197.462s; final `guardrail checks passed (ci)`. |
| Live services, credentials, private coordinates, media mutation or deployment | Not used. |

## Acceptance and integration recommendation

- A-41: private credential/key verification now closes D-04's functional
  contribution, subject to publishing the same verified staging object.
- A-43: the future-schema ceiling and ordinary restored lock behavior are
  accepted for this correction.
- A-44: not accepted. Staging substitution/cleanup data loss and manifest
  preflight/report drift prevent a trustworthy isolated recovery claim.

Do not close or integrate D-04 as approved from this correction. Route a
focused correction for both P1 findings, preserve the passing schema,
overlap, no-replace, uncertainty, cancellation and resource-limit behavior,
and submit exact new product and handoff SHAs for another independent review.
This receipt does not authorize release, deployment, live backup/restore, or
any live media operation.
