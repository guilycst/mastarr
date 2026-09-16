# X-08 independent review, round 3

## Decision and exact scope

Decision: **changes requested**. The correction closes round-two R3 for
capture, `RecordUnavailable` and deletion across separate service values. One
P1 remains in the capture publication failure path: a metadata failure followed
by cleanup failure leaves sensitive descriptor bytes outside durable metadata;
retry cannot adopt them, and acknowledged deletion of the unavailable row
reports success without removing them.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Corrected product: `b8f648f055d922699061264877e9acb81d3ad075`.
- Product tree: `f8ec86fba8177c5cc9f9cd85089bc937e8e6e8af`.
- Correction handoff: `c243b9f4a1f894114c19794b0c307797cc005918`.
- Handoff tree: `8a2a0c0f3f95d60aef2883e782b11926689b032d`.
- Dispatch checkpoint: `454ac0acce6893745094bdbfd395df325ea0967f`.
- Prior receipt: `eb996380766dad776c7915f0b27dfecbe23a7724`.
- Scope: `internal/descriptors/`, correction handoff, A-06 and A-42.

Review ran in an isolated detached checkout at the exact dispatch checkpoint.
Independent probes temporarily added package-local tests, ran under race, and
were removed before this receipt. Product files, execution state, shared main
and live services were untouched. This commit contains only this receipt.

## Round-two finding closure

### R3 closed: durable capture, unavailable and delete transitions

Capture, `RecordUnavailable` and `Delete` now share the same local
download/type lock key, and `Delete` reloads after acquiring it. Cross-service
safety comes from database fences. Intent insertion compares every durable row
field from the reviewed snapshot. Terminal deletion repeats that comparison and
requires a pending intent. Capture and unavailable updates refuse a pending
intent in their SQL predicates.

The committed cross-service regression passed ten race repetitions in both
orderings. When deletion commits its intent first, capture returns
`ErrDescriptorDeletePending`, publishes no object, and deletion completes.
When capture changes the unavailable row before intent creation, the stale
delete returns `ErrDescriptorConflict`; the exact bytes and available metadata
remain, with no pending or terminal deletion audit.

An independent two-service `RecordUnavailable` probe also passed ten race
repetitions. Intent-first updates returned `ErrDescriptorDeletePending`; an
update that changed the unavailable reason before intent creation made stale
deletion return `ErrDescriptorConflict`, preserving the new reason and retained
state. The prior mounted pathname replacement, post-unlink restart
reconciliation and same-content replacement regressions also passed ten race
repetitions.

## Finding

### R4 — P1: failed capture cleanup can permanently orphan sensitive bytes and survive successful deletion

For an existing unavailable row, capture publishes the final private object in
`writeObject` before attempting its fenced metadata update
(`internal/descriptors/descriptors.go:558-562`). If `updateCaptured` fails, the
service calls `removeGeneratedObject` but discards its error at line 566 and
returns only the metadata error. The durable row therefore contains no digest
or identity for an object that may still exist. A retry reaches the no-replace
destination collision at lines 687-696, so it cannot adopt or reconcile the
operation-owned bytes. `Delete` then sees the durable row as unavailable and
skips the filesystem phase at lines 489-496.

Independent `TestReviewerCaptureCleanupFailureDoesNotOrphanBytes` registered a
synthetic SQLite trigger before opening the fixture. Immediately before aborting
the descriptor metadata update, the trigger invoked a test-only SQLite scalar
function that removed write permission from the private objects directory.
Public `CaptureExport` had already published and verified the exact bytes; its
fenced update failed, and the ignored cleanup failure left the object present.
After restoring permissions, an identical retry returned
`ErrDescriptorConflict`. Public acknowledged `Delete` then succeeded against
the still-unavailable row, while the exact sensitive bytes remained readable at
the private object path.

Command:

`GOWORK=off go test -race -count=1 ./internal/descriptors -run '^TestReviewerCaptureCleanupFailureDoesNotOrphanBytes$' -v`

Outcome: exit 1 with `capture returned descriptor metadata storage failed:
update descriptor metadata; retry conflicted; deletion succeeded; sensitive
bytes still orphaned: "sensitive bytes must not be orphaned"`.

This violates the requested no-orphan guarantee, exact deletion, A-06 retention
honesty and the private-data boundary contributed to A-42. A published object
must have durable recoverable identity before an error can escape. Cleanup
failure cannot be discarded. The service needs a durable capture intent/staged
state or equivalent recovery record that binds the object and allows retry,
deletion and startup reconciliation to adopt or remove it safely. At minimum,
cleanup uncertainty must remain visible and must prevent an unavailable-row
deletion from claiming completion while the bound object exists.

## Retained behavior

Verified export evidence, mounted root confinement and replacement checks,
exclusive private staging, no-replace publication, digest/read-back, ordinary
metadata/content separation, redacted audit metadata, honest unavailable state,
acknowledgement, successful exact deletion, completed-deletion idempotency and
post-unlink restart reconciliation remain covered by inspection and committed
tests. These results do not close R4.

No live service, credential, private media, tracker data or user path was used.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 3.351s. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 23.320s. |
| Corrected cross-service fence plus prior filesystem/restart regressions under race, count 10 | Exit 0; package 76.569s. |
| Independent cross-service `RecordUnavailable` fence probe under race, count 10 | Exit 0; both transition orderings preserved the durable winner. |
| Independent capture metadata/cleanup failure probe under race | Reproduced R4; update failed, retry conflicted, delete succeeded and exact bytes remained. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly package builds | Exit 0 for all four targets. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, all Vacuum checks, lint/architecture, root/UI/tools/six-client tests and race, vet, module verification and 18 Linux cross-builds passed; final line `guardrail checks passed (ci)`. |
| Product drift check from correction handoff through dispatch checkpoint for `internal/descriptors/` | Exit 0; no descriptor product drift after handoff. |

The receipt commit runs the versioned pre-commit hook without bypass. X-08
should remain blocked until R4 has a deterministic regression and an independent
correction review approves the exact corrected product.
