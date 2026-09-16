# X-08 independent review, round 5

## Decision and exact scope

Decision: **changes requested**. The correction closes the direct round-four
capture-versus-delete interleaving and rejects contradictory capture terminal
outcomes. One P1 remains in fence ownership: a second executor for the same
operation may reuse and release the first live executor's fence, after which the
opposite operation can acquire a new fence while the first executor is still in
its protected filesystem transition.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `cc7c530e4114112cceeaba28000ab59d893efc59`.
- Product tree: `49a90dc4f770c2a96d2fa778695e799ba3f695f9`.
- Correction handoff: `3da7d80323818f69eed9dd7ff1c2f164345005a0`.
- Handoff tree: `bd4baa32b562ca05286b56cb5a9f36c0ecf5b9cc`.
- Dispatch checkpoint: `eef08f0e6e0c8db161b10bb089d3b9d21a0a42c5`.
- Prior receipt: `c29adbaccdd9b5d671fe72b00ba740c81269cc1f`.
- Scope: `internal/descriptors/`, correction handoff, A-06 and A-42.

Review ran in an isolated detached checkout at the exact dispatch checkpoint.
Independent package-local probes were removed before this receipt. Product
files, execution state, shared main and live services were untouched. This
commit contains only this receipt.

## Round-four finding closure

The append-only `descriptor.capture.fence` now separates direct
`capture-recover` and `delete-reconcile` operations. Acquisition requires the
exact capture intent to remain pending and excludes an unreleased fence for the
opposite operation. Capture retains its fence through stage publication,
read-back, cleanup, metadata adoption and terminal capture state. Delete retains
its fence through final/stage observation and its terminal decision.

The committed forward regression passed ten race repetitions: while capture
paused immediately before stage publication with its fence held, acknowledged
Delete returned `ErrCaptureUncertain`; capture then published the exact object
and made the row available. An independent reverse-order probe passed ten race
repetitions: Delete acquired its fence first, capture returned
`ErrCaptureUncertain`, Delete refused to abort while the bound stage existed,
and a later capture recovered the same stage successfully.

`finishCaptureIntent` now reads the existing terminal outcome. Repeating the
same outcome is idempotent; requesting `aborted` after `committed`, or the
reverse, returns `ErrCaptureUncertain`. The committed and independent probes
confirmed this boundary. Prior metadata/cleanup recovery, fresh-service stage
and final adoption, exact inode/digest checks and foreign equal-content
replacement refusal remained green.

## Finding

### R4b — P1: same-operation reuse can release another live executor's capture fence

When `acquireCaptureFence` finds an unreleased fence with the same operation, it
returns the same fence event to the caller without a new owner identity,
generation or lease (`internal/descriptors/descriptors.go:1368-1383`).
`releaseCaptureFence` records release for only that shared fence event
(`internal/descriptors/descriptors.go:1461-1505`). It cannot distinguish the
executor that originally acquired the fence from a concurrent executor that
reused it.

Independent `TestReviewerSameOperationResumeCannotReleaseLiveFence` created an
unavailable descriptor with a durable capture intent and verified stage. One
service acquired the `capture-recover` fence and deliberately remained live.
A second service acquired the same operation and received the same fence event.
After only the second service released it, a third service successfully acquired
a `delete-reconcile` fence for the same still-pending intent. The first capture
executor had neither released its fence nor completed its filesystem work.

Command:

`GOWORK=off go test -race -mod=readonly -count=5 ./internal/descriptors -run '^TestReviewerSameOperationResumeCannotReleaseLiveFence$' -v`

Outcome: exit 1 in 8.033s. All five repetitions reported a newly acquired delete
fence while the first capture executor still owned its earlier fence event.

This defeats the exact serialization introduced for R4a. Once the opposite
fences coexist, the already-proven schedule is reachable again: delete may
observe final absence, the still-live capture executor moves the bound inode
from stage to final, and delete observes stage absence, aborts the intent and
deletes unavailable metadata while bytes remain. The producer regression does
not cover this because it uses one capture executor and one delete executor.

Restart recovery cannot safely mean unqualified concurrent reuse. The durable
record needs ownership that a second live executor cannot release, such as an
exact owner generation with a reviewed lease/takeover protocol, or another
transition that proves the prior executor is no longer active. Release and
terminal transitions must be fenced to that exact owner. Until then the
cross-service fence does not guarantee the requested data-integrity boundary.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| Committed forward publication/Delete fence plus prior capture recovery cases under race, count 10 | Exit 0; package 64.946s. |
| Independent delete-first fence, recovery and contradictory-terminal probe under race, count 10 | Exit 0; package 16.774s. |
| Independent same-operation live-owner/reuse/release probe under race, count 5 | Exit 1 in 8.033s; R4b reproduced in all five repetitions. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 29.995s, 31.47s wall. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly package compilation | Exit 0 for all four targets. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, staged and working-tree Vacuum, lint/architecture, root/UI/tools/six-client tests and race, module verification and 18 Linux cross-build entries passed; final marker `guardrail checks passed (ci)`. |
| Product drift check from product through dispatch for `internal/descriptors/` | Exit 0; no product drift. |

No live service, credential, private media, tracker data or user path was used.
X-08 should remain blocked until R4b has a deterministic regression and an
independent correction review approves exact owner-fenced restart recovery.
