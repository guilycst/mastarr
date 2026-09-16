# X-08 independent review, round 2

## Decision and exact scope

Decision: **changes requested**. The correction closes both round-one P1
reproductions, but one new P1 data-integrity race remains between deletion of an
unavailable record and capture of newly available descriptor bytes. The race
makes deletion report success while leaving raw descriptor bytes orphaned in
private storage.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Corrected product: `b766ea8712fd18f6b02dd6c8aa4c304745304298`.
- Product tree: `9dc252b41ec6786a2b50f52ff55c1e56ed7c4a56`.
- Correction handoff: `662fa6c19c0274578f52b705450a07d66d977a32`.
- Handoff tree: `5a968c6f3b6c20978b23ac613f775619f4507fb4`.
- Dispatch checkpoint: `8ca6150a873d5df89031e2cc0849cbbf3c23d6d7`.
- Prior receipt: `cedb49af4ee4dfb99bba0d4abacfff46aca1f0a3`.
- Scope: `internal/descriptors/`, correction handoff, A-06 and A-42.

Review ran in an isolated detached checkout at the exact dispatch checkpoint.
The independent probe temporarily added one package-local test, ran under race,
and was removed before this receipt. Product files, execution state, shared main
and live services were untouched. This commit contains only this receipt.

## Round-one finding closure

### R1 closed: mounted pathname replacement

`CaptureMounted` now reopens the configured root-relative pathname after the
original opened descriptor's two stability reads. `verifyMountedPath` requires
the reopened path to identify the same file, performs bounded stable read-back,
and compares digest and size before any private capture. The committed atomic
rename regression returned `ErrDescriptorChanged`, created no row and preserved
the replacement bytes on all ten race repetitions. Traversal, absolute paths,
symlinks, directories, missing sources and in-place mutation remain rejected.

### R2 closed: post-unlink journal failure and restart reconciliation

Deletion now commits a strict, redacted `descriptor.delete.intent` before
unlink. Its metadata binds descriptor type, digest and Unix device/inode
identity. A fresh service reconciles an absent exact object to one terminal
deletion record; a present object must match the persisted identity before
digest verification and removal. The committed regression forced the database
failure after unlink, restarted the service, rejected a same-content replacement
without changing it, then reconciled absence with one terminal audit. It passed
all ten race repetitions. Unsupported platforms fail closed when a persistent
object identity is unavailable.

## Finding

### R3 — P1: unavailable deletion can race capture and orphan retained bytes after reported success

Deletion and capture serialize the same descriptor through different lock keys:
`Delete` locks the descriptor ID (`internal/descriptors/descriptors.go:398`),
while capture locks download ID plus descriptor type (line 503). For an
unavailable record, `Delete` commits an intent with no filesystem identity and
then proceeds directly to its terminal transition (lines 474-483). Meanwhile,
capture may write the newly available private object and update that same row;
`updateCaptured` checks only `id` and `deleted_at IS NULL` (lines 880-892).
`markDeleted` then checks only the same two fields (lines 838-866), so it accepts
the stale unavailable snapshot, marks the row deleted and appends an empty-digest
terminal audit without removing the object that capture just published.

Independent `TestReviewerUnavailableDeleteCannotRaceCapture` used one public
`Service`, one SQLite store and synthetic bytes. It first recorded an honest
unavailable descriptor. A controlled clock blocked the delete after its durable
intent and before `markDeleted`. During that pause, public `CaptureExport` for
the same download/type returned an available record and published its private
object. Releasing the delete made public `Delete` return success, but the exact
new bytes remained at the descriptor's object path while metadata was terminally
deleted and ordinary content access could no longer expose or manage them.

Command:

`GOWORK=off go test -race -count=10 ./internal/descriptors -run '^TestReviewerUnavailableDeleteCannotRaceCapture$'`

Outcome: exit 1 on all ten repetitions with `delete reported success but retained
newly captured object "newly captured descriptor must not be orphaned"`. This is
a same-process production interleaving; it does not require two service
instances or an external filesystem actor.

This violates exact descriptor deletion, A-06 retention honesty and the private
data boundary contributed to A-42. Capture and delete need one descriptor
identity serialization boundary plus a durable compare-and-swap fence. The
terminal delete must prove the row still matches the intent's reviewed digest,
availability and storage identity; capture must not materialize a descriptor
under a pending delete intent. A conflicting transition must abort or reconcile
without reporting deletion while bytes remain.

## Retained behavior

Verified export evidence, root confinement/no-follow behavior, exclusive private
staging, no-replace publication, digest/read-back, ordinary metadata/content
separation, redacted audit metadata, honest unavailable state, repeated capture,
acknowledgement, successful exact deletion and repeated completed deletion remain
covered by inspection and committed tests. These results do not close R3.

No live service, credential, private media, tracker data or user path was used.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 2.174s. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 19.862s. |
| Corrected R1/R2 and replacement regressions under race, count 10 | Exit 0; package 47.217s. |
| Independent unavailable-delete/capture race, count 10 | Reproduced R3 in all ten iterations; reviewer assertion failed because bytes remained after successful deletion. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly package builds | Exit 0 for all four targets. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, all Vacuum checks, lint/architecture, root/UI/tools/six-client tests and race, vet, module verification and 18 Linux cross-builds passed; final line `guardrail checks passed (ci)`. |
| Product drift check from correction handoff through dispatch checkpoint for `internal/descriptors/` | Exit 0; no descriptor product drift after handoff. |

The receipt commit runs the versioned pre-commit hook without bypass. X-08
should remain blocked until R3 has a deterministic regression and an independent
correction review approves the exact corrected product.
