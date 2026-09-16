# X-08 independent review, round 7

## Decision and exact scope

Decision: **approved**. No P1 or P2 finding remains for correction round six.
The product closes R4c at each recovery-owned filesystem transition and closes
R4d across a reopened database. X-08 may advance through the coordinator's
integration gate.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `8c770f779204a00c2d8cc6312a2c0fc04262ea35`.
- Product tree: `53d843d2a11ec970746c7fbd485d74e148676b4d`.
- Correction handoff: `06961d6a775a16106fe36a3b85181845ac39e443`.
- Handoff tree: `c86420aeacf43689e18e4c4f9b5edf272a1d5f76`.
- Dispatch/state checkpoint: `f5bea0b9288f1f7c74f8311610096d5aad9e583e`.
- Dispatch tree: `1c0f634218b5214db3599cc4837af2d57f23ca2f`.
- Prior review: `e15275d5741e66a05102540e18cb503b429734fb`.
- Scope: `internal/descriptors/`, correction handoff, A-06 and A-42.

Review ran in an isolated detached worktree at the dispatch checkpoint. Product
files matched the product commit byte-for-byte, with no descriptor drift through
the dispatch checkpoint. Temporary package-local probes were removed before
this receipt. Product files, execution state, shared main and live services
were untouched. This commit contains only this receipt.

The correction handoff's `Source product under correction` field retains the
old mistyped round-six source SHA. This does not affect the reviewed product:
the exact current product, handoff and dispatch SHAs above all exist and match
the coordinator's review request.

## R4c: expired-owner filesystem transitions

Approved. `ensureCaptureFenceLive` validates the exact pending fence event,
intent, resource, operation, owner, generation and lease before accepting a
transition. It also scans all active fences for the same intent and rejects any
other unexpired event, including a same-operation owner. Released, expired,
foreign and opposing owners therefore fail closed with `ErrCaptureUncertain`.

Recovery publication revalidates after the publication seam and again directly
before the no-replace hardlink. Stage removal invokes the same check after
identity verification and immediately before removal, both after publication
and during standalone stage cleanup. Final-object cleanup acquires its own
durable `capture-cleanup` fence, revalidates it at the service boundary and
again after descriptor-relative identity verification immediately before
`unlinkat`. Non-Unix platforms retain the existing fail-closed removal path.

Independent package-local probes constructed an expired `capture-recover`
owner followed by a live opposing `delete-reconcile` owner. Under the race
detector, ten repeated runs checked three transitions separately:

- stale publication returned `ErrCaptureUncertain`, retained the exact stage,
  and left the final path absent;
- stale stage cleanup returned `ErrCaptureUncertain` and retained the exact
  stage;
- stale final-object cleanup returned `ErrCaptureUncertain` and retained the
  exact final object.

All thirty transition attempts were rejected without filesystem mutation. The
committed deterministic publication/Delete regression also passed repeatedly:
the stale capture owner is stopped after the Delete owner acquires its fence,
the stage remains recoverable, no final object appears, and no false deletion
terminal is recorded. Existing owner/generation isolation, same-operation
contention, lease-expiry takeover, exact identity and restart reconciliation
regressions remain green.

## R4d: durable release identity and lost-response retry

Approved. Fence release metadata now persists the original RFC3339Nano
`LeaseUntil` together with fence event, intent, resource, operation, owner and
generation. A repeated release first reads and strictly decodes the existing
release record, requires every identity component to match, and returns success
without appending another event. Changed or incomplete identities remain
uncertain.

The committed regression proves same-process exact-owner retry and one release
row. An independent restart probe strengthened that evidence: it released a
fence, closed the SQLite store, reopened the same journal, built a fresh
`Service`, retried the exact release and observed success with exactly one
durable release row. Ten race-detector repetitions passed. This covers a lost
response followed by process restart without treating another owner or
generation as the acknowledged release.

## Findings

No P1 or P2 findings.

Informational: the fence is a bounded lease checked immediately before each
external filesystem operation; the filesystem cannot atomically consume a
SQLite generation token. Safety for the reviewed Delete interleaving comes
from retaining the operation-owned stage until publication has produced and
verified the final object, while opposing Delete reconciliation refuses either
bound stage or final evidence. The independent transition probes and committed
end-to-end regression cover those observable states.

## Independent checks

| Reviewer command or scenario | Outcome |
| --- | --- |
| Independent expired-owner publication, stage cleanup and final cleanup under a live opposing Delete fence, race count 10 | Exit 0; all 30 transitions rejected, 59.754s package time including release probe. |
| Independent release retry after SQLite close/reopen and fresh `Service`, race count 10 | Exit 0; exactly one durable release row after every retry. |
| `GOWORK=off go test -race -mod=readonly -count=10 -timeout=240s ./internal/descriptors -run 'TestCaptureFenceRelease\|TestExpiredCaptureOwner\|TestCaptureFenceOwner\|TestCapturePublicationAndDeleteUseDurableFence'` | Exit 0; package 47.228s. |
| `GOWORK=off go test -race -mod=readonly -count=1 -timeout=240s ./internal/descriptors` | Exit 0; package 35.781s. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Linux amd64/arm64 and Darwin amd64/arm64 CGO-free readonly descriptor builds | Exit 0 for all four targets. An initial reviewer shell-loop invocation used zsh-incompatible word splitting and failed before any build; explicit target commands then passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off /usr/bin/time -p ./scripts/check-guardrails.sh --ci` | Exit 0 in 294.47s; final marker `guardrail checks passed (ci)`. Generation, staged checks, root and standalone Vacuum, lint, architecture, tests, race, vet, module verification and 18 Linux cross-build entries passed. Root storage race completed in 198.100s. |
| Product drift, `git diff --check`, temporary-probe removal and review worktree status | Exit 0; no descriptor drift or whitespace finding; worktree clean before receipt creation. |

Only synthetic bytes, temporary roots and temporary SQLite journals were used.
No credential, private coordinate, tracker data, user path or live service was
read or mutated.

## Acceptance and integration recommendation

- A-06 contribution accepted: exact descriptor bytes remain available only
  through the retained-content boundary; ordinary records and audit metadata
  remain metadata-only and redacted, with uncertainty preserved during
  interrupted capture recovery.
- A-42 contribution accepted for X-08: capture and acknowledged deletion retain
  durable provenance and exact object identity; unavailable, pending,
  uncertain and deleted states remain distinct; recovery is idempotent and
  refuses foreign replacement.
- R4c and R4d are closed at the exact product SHA above.
- Coordinator may record this approval and advance X-08. Release, deployment
  and live-stack verification remain separate gates.
