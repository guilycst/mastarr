# D-04 backup and restore independent review, round one

Decision: **changes_requested**.

The package has useful strict parsing, digest, credential-binding, SQLite
snapshot, and isolated read-back foundations. It cannot yet be accepted as a
recovery boundary. Five P1 findings permit an unusable backup to be published,
accept an unsupported future schema, corrupt the source archive through an
overlapping restore, race an existing destination, or bypass path-overlap and
symlink checks through an ancestor alias. Cancellation also does not bound the
potentially terabyte-scale filesystem work.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product commit:
  `9b682b56306d450d582e4ec23f6f8e5e1df37243`; tree
  `895c33e02f8ba26d284a987854119f4124066050`.
- Exact handoff commit:
  `0d8bccedf51cc25490167cb5692374807f2fc90c`; tree
  `774c39b91259d81ddf1a312d19723cc83bfdb0a5`.
- Exact dispatch/state checkpoint:
  `af18c7194dfaaf0b3cb4e4628a846a850c9b26ef`; tree
  `a49c480693c7ecdb719819deaa5b4c50e5904294`.
- Product-scope diff SHA-256:
  `87c2fada44e5e3eeb4a8d5a3a1c4b2a724f413c454a8c47ac97b79cb0a8d4acf`.
- Acceptance reviewed: A-41, A-43 and A-44, plus D-04's recovery,
  publication, safety, portability and cancellation invariants.
- Review receipt checkpoint: the commit containing this file; its exact SHA is
  reported to the coordinator after commit because a commit cannot contain its
  own SHA.

The review ran in an isolated detached worktree at the exact dispatch
checkpoint. `git diff --exit-code 0d8bcce af18c71 --
docs/operations/backup-restore.md internal/backup` passed, so the current tree
contains the exact reviewed product. Reviewer probes were temporary package
tests against that tree and were removed after execution. Product, state,
task, generated and shared-checkout files were not edited.

## Findings

### P1: `Create` publishes a coupled snapshot without proving that it is restorable

`Create` copies the database, selected key, descriptors and trash roots, writes
the manifest, syncs, and publishes (`backup.go:172-244`). It never calls
`Verify` on the private staging tree. `validateSource` checks only that the key
is a regular private file (`backup.go:372-408`); it does not prove that key can
open the encrypted credential rows in the captured database. Retained
descriptor and trash references are likewise checked only by later `Verify`.

Independent deterministic probe:

1. Create the normal synthetic source containing an encrypted credential.
2. Replace `Source.CredentialKeyPath` with a different, well-formed 0600 Mastarr
   key.
3. Call `Create` and inspect the destination.
4. Call `Verify` on the published destination.

Observed in all five race-detector repetitions: `Create` returned success and
the destination existed; `Verify` returned `ErrCredentialVerification`.

This contradicts A-41/A-44 and the operator contract that an incomplete backup
is not published. It also lets generated manifests larger than the reader's
4 MiB limit be published because `writeManifest` has no matching size gate.

Required change: perform the complete read-only recovery verification on the
private staging tree before publication, including credential binding,
descriptor/trash references, schema support, exact manifest readability and
all resource limits. Add wrong-key, missing referenced descriptor/trash item,
and oversized generated-manifest creation regressions proving zero published
destination.

Disposition: `current_blocker`.

### P1: an internally consistent unsupported future schema is accepted and published

`migrationVersion` rejects only nonpositive or dirty versions
(`backup.go:511-523`). `Verify` compares the database version only to the
archive-controlled manifest version (`backup.go:338-344`). It never compares
either value with the highest migration version supported by this binary.

Independent deterministic probe changed the copied database's clean migration
version to `999`, recomputed its recorded digest, and set the manifest schema
version to `999`. `Restore` succeeded, published the target, and returned
schema version 999 in all five race-detector repetitions.

The producer's “schema downgrade” fixture edits only the manifest and leaves
the database at version 9, so it proves disagreement rejection rather than
future-schema rejection. The result conflicts with A-43 and
`docs/operations/backup-restore.md:110-115`, which says a newer backup remains
blocked until the binary supports it.

Required change: derive the supported migration ceiling from the embedded
migration set through a maintained storage contract, reject database or
manifest versions above it before publication, and add equal manifest/database
future-version fixtures. Preserve supported older snapshots for normal upward
migration after the isolated check.

Disposition: `current_blocker`.

### P1: restore allows its target inside the source archive and corrupts the restore point

`Restore` validates the archive and destination independently
(`backup.go:250-268`) but never rejects overlap between them. Unlike `Create`,
it has no `pathsOverlap` gate.

Independent deterministic probe restored a valid archive to
`<archive>/restored`. `Restore` returned success. A subsequent `Verify` of the
source archive returned `ErrIncomplete` because the newly published subtree is
an unexpected archive entry. This passed as a reproduction in all five
race-detector repetitions.

The operation destroys the validity of the known-good restore point while
claiming recovery success, contrary to A-44 and the documented isolated-target
procedure.

Required change: reject source/target overlap in both directions before
creating staging, using resolved path identities rather than lexical spelling.
Add target-under-archive, archive-under-target, equal path and alias fixtures;
prove source bytes and tree membership remain unchanged on every refusal.

Disposition: `current_blocker`.

### P1: publication is check-then-rename rather than atomic no-replace

`publishDirectory` calls `Lstat(destination)` and then ordinary
`os.Rename(staging, destination)` (`backup.go:748-760`). Another actor can
create an empty destination directory between those calls; POSIX rename may
replace that directory. This violates the public “never overwrites” promise
and is the same check/write race the filesystem execution specification
forbids. The code is used by both backup and restore.

There is a second certainty defect after rename: if parent-directory `Sync`
fails, the function returns an ordinary error while the destination remains
published. The deferred cleanup targets the old staging pathname and cannot
undo or accurately report that visible effect.

Required change: use a platform-specific atomic no-replace directory
publication primitive where it is proven safe and fail closed on unsupported
platforms. Bind publication and cleanup to the staging object. Represent a
post-publication durability failure as an uncertain visible effect and
reconcile the destination rather than reporting an ordinary no-effect failure.
Add controlled collision and injected parent-sync failure fixtures.

Disposition: `current_blocker`.

### P1: symlink ancestors bypass both source validation and overlap confinement

`validateAbsoluteDirectory` and `validateAbsoluteRegular` use `Lstat` only on
the final path (`backup.go:411-430`). A path whose ancestor is a symlink is
accepted. `pathWithin` then compares only lexical absolute strings
(`backup.go:468-479`), so an alias for the same directory is considered
unrelated.

Independent deterministic probe created `alias -> real`, passed
`alias/descriptors` to `validateAbsoluteDirectory`, and compared the real
source with `alias/descriptors/backup`. Validation succeeded and
`pathsOverlap` returned false in all five race-detector repetitions. In
`Create`, this can place the staging tree inside a source tree through an alias
before `copyTree` walks it, defeating the intended source/destination fence.

Required change: resolve and bind every ancestor without following untrusted
symlinks during later filesystem work, or reject any symlink component. Use
the resulting identities for overlap checks and revalidate before publication.
Add source, archive, destination-parent and overlap alias regressions.

Disposition: `current_blocker`.

### P2: cancellation does not bound file copy, hashing, walking or syncing

The context is checked at public entry and used for SQLite calls, but
`copyStableFile`, `copyTree`, `digestFile`, `copyManifestArtifact`,
`verifyTreeContents` and `syncTree` do not receive or inspect it
(`backup.go:582-720`, `772-798`, `965-1058`, `1147-1182`). A request canceled
after entry can continue walking up to 100,000 entries per tree and copying or
hashing artifacts up to 1 TiB each. The number of configured trash trees and
aggregate bytes are also unbounded.

Required change: copy and hash in bounded chunks with context checks; inspect
context during walks and between fsync steps; establish aggregate entry, tree,
byte and manifest limits before publication. Add mid-copy, mid-walk and
mid-restore cancellation fixtures that leave no destination and only remove
proven private staging.

Disposition: `current_blocker`.

## Preserved behavior

- `Quiesced` is mandatory, the SQLite source must be persistent, and the
  snapshot uses the open store handle with `VACUUM INTO`.
- Dirty or empty migration state and manifest/database version disagreement
  fail closed.
- Manifest decoding is bounded and strict for duplicate and unknown fields;
  archive-relative traversal, direct symlinks, special files, unexpected tree
  entries, mode/size/digest changes and missing artifacts are rejected.
- `Verify` opens the copied key in existing-data mode, does not regenerate it,
  and authenticates each credential using its connection and field binding.
  Errors do not reveal plaintext, ciphertext, key bytes or source paths.
- The database snapshot preserves action, effect, idempotency and trash rows;
  the report exposes bounded counts without private payloads. The restored
  normal store lock excludes a second owner.
- The package makes no upstream or live-media calls. All tests use synthetic
  temporary files, SQLite databases and credential envelopes.

These properties remain valuable but do not close A-44 while the findings
above can publish or accept an invalid recovery point.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate and offline checks also used
`GOPROXY=off GOSUMDB=off` where applicable.

| Check | Result |
| --- | --- |
| Exact product/handoff/checkpoint identity, ancestry, trees, scoped diff and unchanged product after handoff | Passed. |
| Wrong-key create, future-schema restore and restore-under-archive reviewer probes, `go test -race -count=5` | Reproduced all three unsafe behaviors; probe suite exit 0. |
| Symlink-ancestor validation/overlap probe, `go test -race -count=5` | Reproduced accepted ancestor alias and missed overlap; exit 0. |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=180s ./internal/backup` | Passed, package 3.389s. |
| `GOWORK=off go test -mod=readonly -race -count=1 -timeout=240s ./internal/backup` | Passed, package 21.461s. |
| `GOWORK=off go vet -mod=readonly ./internal/backup` | Passed. |
| Root `go test -mod=readonly -count=1 -timeout=240s ./...` | Passed, including storage and backup. |
| `GOWORK=off go mod verify` | Passed: all modules verified. |
| `./scripts/check-guardrails.sh --generation` and `--fast` | Passed; generation clean, Vacuum 100/100 with zero warnings/errors, API and architecture checks passed. |
| `./scripts/check-lint.sh` and `python3 scripts/check_planning.py` | Passed; zero lint issues; 53 tasks and 60 acceptance cases valid. |
| Linux amd64/arm64 CGO-free root builds | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed; root race storage segment completed in 199.431s, all nine module checks and cross-builds completed, final `guardrail checks passed (ci)`. |
| Live services, real credentials, private coordinates and media mutation | Not used. |

## Acceptance and integration recommendation

- A-41: the standalone credential verification behavior is present, but D-04
  fails to apply it before publishing a backup. D-04's A-41 contribution is
  not accepted.
- A-43: dirty/disagreeing schemas and locking are covered, but an equal
  database/manifest future version is accepted. D-04's A-43 contribution is
  not accepted.
- A-44: ordinary fixture restore preserves synthetic rows and selected files,
  but invalid publication, archive corruption, collision and alias paths make
  recoverability unsafe. D-04's A-44 contribution is not accepted.

Do not integrate or close D-04 from this product. Route a correction that
addresses every P1 and the cancellation/resource-bound P2, preserves the
passing strict verification behavior, and supplies independent regressions.
Record the corrected product and handoff as new immutable SHAs. This review
does not authorize deployment, live backup/restore, release, or any live media
operation.
