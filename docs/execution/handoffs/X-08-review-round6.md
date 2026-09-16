# X-08 independent review, round 6

## Decision and exact scope

Decision: **changes requested**. Distinct owner/generation fence events close
round-five live-owner release by a second same-operation executor. Two lease
boundaries remain unsafe: an expired owner can still mutate the filesystem while
an opposing delete fence is active, and the durable release record omits its
lease identity so exact release is not idempotent.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `a6cb5f015680fd830ab0165f5d7a500b8134159b`.
- Product tree: `7f420debd9c077762f93360d29dc2ffdf32fa814`.
- Correction handoff: `9e87d25b04159abf870e45dd893361a2b8158340`.
- Handoff tree: `6159118edde999ede4d16b4fde0470be30c88bde`.
- Corrected dispatch checkpoint: `32e2e2643637a0961132f38a42762557236fa131`.
- Prior receipt: `0bd2bb566fc4feb44141ef28a93ab2880c93bf13`.
- Scope: `internal/descriptors/`, correction handoff, A-06 and A-42.

The original dispatch recorded nonexistent product SHA
`a6cb5f06fc3248704ff84270776cc7a203966305`. The coordinator corrected the
execution record to the exact product above at checkpoint `32e2e264...`; this
review uses that corrected product. The handoff text still contains the old
long SHA, so this receipt is the durable correction reference.

Review ran in an isolated detached checkout at the corrected dispatch. Temporary
package-local probes were removed before this receipt. Product files, execution
state, shared main and live services were untouched. This commit contains only
this receipt.

## Round-five finding closure

Each acquisition now creates a distinct event with a service owner ID, local
generation and lease deadline. Same-operation executors no longer share an
event. Releasing the second executor's event leaves the first executor's event
active, so an opposing `delete-reconcile` acquisition remains blocked. Terminal
capture transitions require the exact event/owner/generation and reject another
unexpired owner. The committed owner regression and focused race suite passed.

The direct capture-first and delete-first publication cases, stage/final
identity validation, metadata/cleanup recovery, contradictory terminal outcome
rejection and foreign equal-content refusal remain covered by the retained
suite. The product's basic expired-lease test also proves a fresh executor gets
a distinct event rather than reusing the expired event. That test does not run
the old executor after takeover, which exposes R4c below.

## Findings

### R4c — P1: expired capture owner can mutate stage/final state under an opposing delete fence

Lease expiry is checked when acquiring a new fence and when writing the terminal
capture transition. It is not checked or renewed before filesystem publication.
`resolvePendingCapture` acquires its fence at
`internal/descriptors/descriptors.go:1867-1877`, then may observe, publish,
read back and clean the stage before the lease is checked only inside
`finishCaptureIntentOwned` at lines 1598-1611. `publishPendingCaptureStage`
does not receive a fence or validate its owner/lease.

Independent `TestReviewerExpiredOwnerCannotPublishUnderOpposingFence` created a
verified pending stage and capture intent. A capture service acquired its owner
fence, the deterministic clock advanced beyond the 30-second lease, and a delete
service successfully acquired the opposing `delete-reconcile` fence. The
expired capture owner then called the exact production stage publication helper.
It returned success, created the final object and removed the stage while the
delete fence was active.

Command:

`GOWORK=off go test -race -mod=readonly -count=5 ./internal/descriptors -run '^TestReviewerExpiredOwnerCannotPublishUnderOpposingFence$' -v`

Outcome: exit 1 in 7.863s. All five repetitions showed nil publication error,
the exact final object present and stage absent while distinct expired-capture
and live-delete owner events coexisted.

The full public orphan schedule also reproduced in one of five stress
repetitions: acknowledged Delete completed while the expired live capture owner
retained the exact final object. Timing did not hit the narrow two-observation
window in the other four repetitions, so the deterministic mutation-under-
opposing-fence probe above is the authoritative reproduction.

Blocking only the stale terminal database write is too late. Once an expired
owner can link/remove files under a successor's opposing fence, the round-four
final-absent/stage-absent orphan race is reachable again. Recovery needs lease
renewal and exact generation validation around every filesystem transition, or
a takeover protocol that prevents the prior executor from resuming its side
effects. A fixed-duration lease without external-effect fencing does not provide
the claimed data-integrity boundary.

### R4d — P2: exact fence release record is malformed and cannot be retried idempotently

`releaseCaptureFence` builds release metadata without `LeaseUntil` at
`internal/descriptors/descriptors.go:1516`, while
`decodeCaptureFenceMetadata` requires a valid RFC3339Nano lease at lines
1325-1348. The first release inserts that incomplete record and unblocks future
acquisition because active-fence queries match only its `fence_event_id`. A
second exact-owner release finds the record, fails its own decoder and returns
`ErrCaptureUncertain` instead of idempotent success.

Independent `TestReviewerCaptureFenceReleaseIsIdempotent` acquired one owner
fence and invoked exact release twice. Three race-detector repetitions failed
consistently with `descriptor capture result is uncertain: capture fence release
identity changed` on the second call.

Command:

`GOWORK=off go test -race -mod=readonly -count=3 ./internal/descriptors -run '^TestReviewerCaptureFenceReleaseIsIdempotent$' -v`

Outcome: exit 1 in 5.317s. The durable release must include the original lease
identity and survive a repeated call or lost response without changing the
result.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| Committed owner, expiry, publication/Delete and prior capture regressions under race, count 5 | Exit 0; package 46.432s. |
| Independent expired-owner publication under opposing fence, race count 5 | Exit 1 in 7.863s; R4c reproduced in every repetition. |
| Independent public expired-owner/Delete orphan stress, race count 5 | One orphan reproduced; four bounded runs missed the narrow timing window. |
| Independent exact release retry, race count 3 | Exit 1 in 5.317s; R4d reproduced in every repetition. |
| `GOWORK=off go test -race -mod=readonly -count=1 ./internal/descriptors` | Exit 0; package 31.807s, 32.97s wall. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly package compilation | Exit 0 for all four targets. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, staged and working-tree Vacuum, lint/architecture, root/UI/tools/six-client tests and race, module verification and 18 Linux cross-build entries passed; final marker `guardrail checks passed (ci)`. |
| Product drift check from corrected product through dispatch for `internal/descriptors/` | Exit 0; no product drift. |

No live service, credential, private media, tracker data or user path was used.
X-08 should remain blocked until R4c and R4d have deterministic regressions and
an independent correction review approves the exact product.
