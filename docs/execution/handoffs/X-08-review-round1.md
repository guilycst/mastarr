# X-08 independent review, round 1

## Decision and exact scope

Decision: **changes requested**. Two P1 data-integrity findings remain in the
retained descriptor service. The ordinary package suite and the full offline
guardrail pass, but neither covers the failing source-selection replacement nor
the irreversible delete/journal split reproduced below.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Initial product: `2e1169c3cf1a2763227ac28ac8e7d96a82fe047b`.
- Final reviewed product: `d07779afd4a9b052b8f91159debb0c7af6df3632`.
- Product tree: `463c59bfe5ee972e903220b68e178a76f4f7f405`.
- Handoff: `caabffc946cedec237e89f688abfe7ab2d2c58da`.
- Handoff tree: `ba2a0ce15d498e16b922b82d3d0daf65cf286e27`.
- Dispatch checkpoint: `bb88b701a911a05c5ced3044d766067ea7f621eb`.
- Scope: `internal/descriptors/`, the X-08 handoff, A-06 and A-42.

Review ran in an isolated detached checkout at the exact dispatch checkpoint.
Independent probes temporarily added package-local tests, ran under race, and
were removed before this receipt. Product files, execution state, shared main
and live services were untouched. This commit contains only this receipt.

## Findings

### R1 — P1: mounted capture accepts bytes from a path that was replaced after selection

`CaptureMounted` opens the selected path once, reads the opened descriptor,
invokes the source-read boundary, then validates only that same open descriptor
again (`internal/descriptors/descriptors.go:228-249`). It never reopens or
otherwise proves that the configured root-relative pathname still identifies
the selected object immediately before capture. An atomic replacement therefore
leaves the old unlinked descriptor stable and lets capture succeed with bytes
that are no longer selected by the configured path.

Independent reproduction `TestReviewerMountedPathReplacementIsRejected` used a
configured mounted root and an original regular file. At the package's
`afterSourceRead` seam it removed that pathname and created a different regular
file at the same pathname. Public `CaptureMounted` returned success five times
under race, with an available record containing the original unlinked object's
size and digest instead of `ErrDescriptorChanged`.

Command:

`GOWORK=off go test -race -count=5 ./internal/descriptors -run '^TestReviewerMountedPathReplacementIsRejected$'`

Outcome: exit 1 because every iteration violated the reviewer assertion; each
call returned a successful available record. This breaks the source-mutation
and exact-selection requirement in A-06 and the filesystem rule that identity
is rechecked after transfer. Capture must bind the final configured pathname to
the originally opened object, failing closed if it is absent or replaced.

### R2 — P1: a journal failure after unlink strands deletion without audit or reconciliation

`Delete` physically unlinks and syncs the retained object before calling
`markDeleted` (`internal/descriptors/descriptors.go:427-443`). If the database
transaction then fails, the method correctly reports `ErrDeleteUncertain`, but
the durable row still says retained and has no deletion audit. A later call
tries to reopen the now-absent object and returns `ErrDescriptorChanged` at
lines 396-403. There is no read-only reconciliation path that can finish the
exact acknowledged deletion.

Independent reproduction `TestReviewerDeleteJournalFailureCanReconcile`
captured a synthetic descriptor and closed the fixture database at the first
post-unlink clock call, deterministically making `markDeleted` fail after the
object had been removed. The first call returned `ErrDeleteUncertain`. After
reopening the same SQLite database and constructing a fresh service, metadata
still had retention `retain` and no `DeletedAt`; retrying the same acknowledged
exact-ID deletion returned `ErrDescriptorChanged` and could not create the
required retained audit/provenance state.

Command:

`GOWORK=off go test -race -count=1 ./internal/descriptors -run '^TestReviewerDeleteJournalFailureCanReconcile$'`

Outcome: exit 1 at the reviewer assertion with `retry could not reconcile the
already absent exact object: descriptor content changed: retained object cannot
be verified`. This violates A-06 deletion idempotency and the X-08 retained
audit/provenance contract after an irreversible effect. Persist a durable
delete intent before unlink, or add an identity-bound recovery state that a
fresh service can reconcile without deleting or accepting any replacement.

## Verified behavior outside the findings

Inspection and committed tests support the remaining scoped claims. Verified
exports require explicit source identity, verification reference, expected
digest and size. Mounted paths reject traversal, symlinks, directories and
special files in the ordinary cases. Private objects use exclusive staging,
no-replace hardlink publication, stable read-back, digest/size checks and
directory sync. Ordinary records and deletion audit metadata omit raw bytes and
private paths; byte retrieval remains an explicit call. Honest unavailable
records and repeated successful capture are idempotent. Exact deletion requires
the irreversible acknowledgement and rejects an already-observed replacement.

These results do not close R1 or R2. No live service, credential, private media,
tracker data or user path was used.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 1.668s. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 16.212s. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Mounted-path replacement probe, race count 5 | Reproduced R1 five times; reviewer assertion failed because capture succeeded. |
| Delete journal-failure/restart probe, race count 1 | Reproduced R2; first result uncertain, fresh-service retry returned changed and left the row unaudited. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, all Vacuum checks, lint/architecture, root/UI/tools/six-client tests and race, vet, module verification and 18 Linux cross-builds passed; final line `guardrail checks passed (ci)`. |
| Product drift check from handoff through dispatch checkpoint for `internal/descriptors/` | Exit 0; no descriptor product drift after the handoff. |

The receipt commit runs the versioned pre-commit hook without bypass. X-08
should remain blocked until both P1 findings have regression tests and an
independent correction review approves the exact corrected product.
