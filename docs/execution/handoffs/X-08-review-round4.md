# X-08 independent review, round 4

## Decision and exact scope

Decision: **changes requested**. The correction durably binds capture preparation
to the generated descriptor ID, digest, size, relative final/stage names and Unix
device/inode identity before publication. It also recovers the reviewed metadata
and cleanup failure, refuses equal-content foreign replacements, and keeps
ordinary records and errors free of descriptor bytes and absolute paths. One P1
remains: deletion can abort an active stage-to-final transition after two
non-atomic absence observations, delete the unavailable metadata row, and leave
the exact descriptor object behind.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `b4724473923d2b7059eca519252d119bff9a850e`.
- Product tree: `9d4636f12c96e0d565ceda194f9378de5cca2e27`.
- Correction handoff: `6333ea37169bbf9c63b1065e6be50bff36c5d224`.
- Handoff tree: `721bf0e7728d6cab0be99b04dc7e849ed4895448`.
- Dispatch checkpoint: `4cca06958f35dbd1168201b7bed2b292bcb8b083`.
- Prior receipt: `0ca424094dbce6700d5823ecbf6bb6e409a4178a`.
- Scope: `internal/descriptors/`, the correction handoff, A-06 and A-42.

Review ran in an isolated detached checkout at the exact dispatch checkpoint.
Independent package-local probes were removed before this receipt. Product
files, execution state, shared main and live services were untouched. This
commit contains only this receipt.

## Prior finding closure

The new `descriptor.capture.intent` is written before the no-replace final link.
Its strict metadata binds the request, generated resource ID, digest, byte count,
capture source, deterministic root-relative object path, opaque stage basename,
and operation-created device/inode identity. Recovery validates the same inode,
size and digest before publishing or adopting it. Metadata insertion/update and
terminal capture events happen after verified filesystem publication. A failed
metadata transition whose cleanup also fails remains `ErrCaptureUncertain`; a
fresh service can adopt the exact final object or publish the exact retained
stage. `RecordUnavailable` and `Delete` refuse an active bound object rather
than claiming ordinary completion.

The committed recovery tests passed ten race repetitions. An independent
equal-content replacement probe also passed ten race repetitions: after a
synthetic metadata and cleanup failure, the reviewer atomically replaced the
final pathname with a new inode containing identical bytes. Fresh capture
returned `ErrDescriptorChanged`; `RecordUnavailable` returned
`ErrCaptureUncertain`; acknowledged `Delete` returned `ErrDescriptorChanged`;
the replacement remained unchanged. The journal metadata contained neither
descriptor bytes nor the absolute fixture root.

## Finding

### R4a — P1: delete can abort an in-flight stage publication and orphan the bound object

`reconcileCaptureBeforeDelete` observes the final path first and the stage path
second (`internal/descriptors/descriptors.go:1592-1639`). For an unavailable row,
it writes an `aborted` capture terminal when both separate observations report
absence. Those observations are not fenced against a second `Service` running
`publishPendingCaptureStage`, which links the verified stage at the final path
and then removes the stage (`internal/descriptors/descriptors.go:1409-1463`).

The unsafe interleaving is:

1. Delete observes the final path absent while the bound inode is at the stage
   path.
2. A fresh capture recovery links that inode at the final path and removes the
   stage path.
3. Delete observes the stage path absent, records the capture intent as aborted,
   and continues because the durable row is still unavailable.
4. Delete records the unavailable descriptor as deleted without a filesystem
   phase. The exact descriptor inode remains at the final path. Capture recovery
   can no longer find an active intent; if its metadata update loses the race,
   the bytes have no recoverable active journal.

Independent `TestReviewerDeleteCannotAbortCaptureDuringStagePublication`
created an unavailable row and durable capture intent for a verified stage,
then alternated the same bound inode between the journaled stage and final names
while public acknowledged `Delete` ran. This models the externally observable
states on either side of the production link/remove transition. Every one of
five race-detector repetitions reproduced successful deletion while the exact
object still existed: four ended at the final name and one at the stage name.

Command:

`GOWORK=off go test -race -mod=readonly -count=5 ./internal/descriptors -run '^TestReviewerDeleteCannotAbortCaptureDuringStagePublication$' -v`

Outcome: exit 1. Each repetition reported either `delete completed while
capture object remained at final` or `delete completed while capture object
remained at stage`.

The two service values have independent in-memory locks. The database capture
intent is the cross-service authority, but deletion closes it without first
acquiring a durable publication fence. `finishCaptureIntent` also treats any
existing terminal for the intent as success without checking that its outcome
matches the requested outcome (`internal/descriptors/descriptors.go:1315-1334`),
so an abort can mask a concurrent commit attempt.

This violates the requested rule that `Delete` cannot claim completion while an
active capture object exists, exact deletion, A-06 retention honesty and the
private-data boundary contributed to A-42. Deletion must not infer stable
absence from two path reads while publication owns an active intent. It needs a
durable CAS/lease/state transition that excludes publication, or it must leave
the active capture uncertain until capture recovery terminalizes it. Terminal
outcome conflicts must fail closed.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| Committed capture-intent and prior descriptor regressions under race, count 10 | Exit 0; package 133.684s. |
| Independent equal-content final replacement probe under race, count 10 | Exit 0; foreign inode rejected by capture, unavailable update and delete; exact bytes preserved. |
| Independent active stage/final transition versus acknowledged delete under race, count 5 | Exit 1 in 7.719s; R4a reproduced in all five repetitions. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 26.223s, 27.43s wall. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly package compilation | Exit 0 for all four targets. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, staged and working-tree Vacuum, lint/architecture, root/UI/tools/six-client tests and race, module verification, and the 18 Linux cross-build entries passed; final marker `guardrail checks passed (ci)`. |
| Product drift check from product through dispatch for `internal/descriptors/` | Exit 0; no product drift. |

No live service, credential, private media, tracker data or user path was used.
X-08 should remain blocked until R4a has a deterministic regression and an
independent correction review approves the exact corrected product.
