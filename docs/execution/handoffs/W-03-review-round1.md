# W-03 independent review, round one

Decision: **changes requested**. Four P1 findings block integration. The
repository baseline passes its existing checks, but lost-response reconciliation,
filesystem uncertainty, native rename scope, and linked-client coordination are
not safe under the reviewed handler contract.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`; independent of product authorship.
- Exact product commit: `fc2bab90bd3db21c6801842babee6b473e2742da`.
- Exact product tree: `465d2bf49e18dbb08beb9a663368f421771e0f07`.
- Product parent/base: `13e32c7b8f96a9d322ab933214d4a54e687d7a98`.
- Exact handoff: `19b13f86411c74ae2bec249e0481de8ebcf16187`;
  direct parent is the product commit.
- Exact handoff tree: `33099d8e57b401c11fa400a010c8acda03b28785`.
- Exact review-dispatch checkpoint:
  `067fc969efb6c270726f6f3c242b76b17101cc3d`.
- Dispatch tree: `8abc3d1d0bec45adfb59611a476d7c3b84bd29f7`.
- Reviewed product scope: `internal/actions/`, with the frozen execution,
  filesystem, descriptor, Arr, download-control, and Jellyfin ports/adapters
  inspected for contract behavior.
- Acceptance reviewed: A-13, A-14, A-17, A-18, A-20, A-24, A-33, and A-60.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code fc2bab9 067fc96 -- internal/actions` passed. Product,
state, generated output, and unrelated files were not edited. Independent
probes ran in a temporary archive of the exact checkpoint and were not added
to the repository. All fixtures were synthetic; no live service, credential,
private coordinate, inventory, or media was used.

## Findings

### R1 — P1: partial materialization is declared safe to retry

`ImportHandler.Reconcile` at `internal/actions/arr.go:388-400` treats every
`ObserveNeedsAction` result as proof that no import effect happened. It rewrites
all effects to pending and returns `SafeToRetry: true`. A pack with one exact
association present and another missing therefore loses the present association
evidence and authorizes another import call.

`FilesystemHandler.Reconcile` repeats the same behavior at
`internal/actions/filesystem.go:234-246`. One verified destination plus one
missing destination is rewritten to two pending effects and declared safe to
retry.

This contradicts the executor contract at
`internal/execution/execution.go:333-358`: safe retry is allowed only after the
handler proves that no desired effect was materialized. It also breaks A-17's
per-file partial evidence and A-33's zero-blind-resubmission rule.

Independent race probes constructed those two exact mixed states. Both failed
the required regression ten of ten times. The Arr result contained
`import_association_present` for the first file before the handler changed its
state to pending. The filesystem result contained `destination_read_back` for
the first target before the same rewrite.

Required correction: preserve every observed per-target state. Return
`SafeToRetry` only when every exact target is authoritatively pending/absent and
none is already materialized. A mixed result must stay unresolved or dispatch
only a newly approved exact remainder; it cannot requeue the original batch.

### R2 — P1: post-call filesystem effects can be labeled pre-dispatch

`FilesystemHandler.Dispatch` calls the mutation port at
`internal/actions/filesystem.go:218`, then decides whether an error is uncertain
only from its sentinel name at lines 219-220 and 559-560. It discards the
returned `FilesystemEffect`, including `Affected`.

That classification is invalid for the approved ports. For example,
`placement.runCopy` accumulates affected files while processing the batch at
`internal/filesystem/placement/placement.go:356-385` and can later return
`ErrSourceChanged`, including from final directory stability verification.
The handler classifies `placement.ErrSourceChanged` as pre-dispatch even when
earlier files were published.

An independent race probe returned an affected applied entry plus
`placement.ErrSourceChanged` from the called port. The handler made one mutation
call but returned `execution.Failure{Dispatched:false, Kind:"invalid"}`. The
required uncertainty regression failed ten of ten times.

This can bypass W-02 reconciliation after a partial filesystem effect and
violates A-24/A-33. Required correction: preserve returned effect evidence and
treat any error after a port could have mutated as dispatched/uncertain unless
the typed port contract proves zero effects for that exact result. The decision
cannot rely on a sentinel that occurs both before and after earlier batch
effects.

### R3 — P1: native rename can report the wrong root as applied

The native rename bridge checks only the relative containing directory at
`internal/actions/filesystem.go:527-539`. It never requires
`mapping.Source.RootID == mapping.Destination.RootID`. It then invokes
`RenameFile`/`RenameFolder` with the source target and new basename, so the
native operation remains in the source root while the action effect is emitted
for the destination root.

The product fixture itself uses source root `download` and destination root
`library` at `internal/actions/actions_test.go:504-526` and expects success.
An independent regression reproduced an accepted `applied` result for
`library:incoming/renamed.mkv` after exactly one rename call on
`download:incoming/movie.mkv`; no library-root operation occurred. The rejection
regression failed ten of ten race repetitions.

This is false effect evidence and can make a workflow continue after the
approved destination was never created. Required correction: native rename
must require the exact same root and containing directory before any client
call, and post-call/read-back evidence must match the approved destination.
Cross-root placement must use an explicitly supported relocation/transfer
action rather than basename rename.

### R4 — P1: linked-client handlers cannot coordinate the approved reference

The registered filesystem handlers receive one process-wide
`HandlerOptions.LinkedClient.Ref` at `internal/actions/actions.go:75-83` and
`NewHandlers` constructs exactly one handler per action kind at lines 187-234.
`checkLinkedClient` rejects every action whose immutable `LinkedDownload` is not
that one hardcoded reference at `internal/actions/filesystem.go:252-271`.
Consequently, one registered handler cannot process stopped downloads across a
normal multi-download or multi-instance inventory.

An independent regression configured the registered handler with `hash-a` and
submitted a valid stopped `hash-b` action through the same normalized control
port. It returned `ObserveUnknown` before observing the approved reference. The
regression failed ten of ten race repetitions.

The concurrency half is also missing: `FilesystemHandler.Reservations` at
`internal/actions/filesystem.go:124-140` reserves only paths. It omits the
`client:<connection>:<external-id>` key used by client stop/remove handlers.
The independent reservation probe returned only the source and destination
paths; it failed ten of ten repetitions. A native relocation/rename and a
metadata removal can therefore be scheduled without a shared reservation even
though both require the same client record.

Required correction: route the immutable action reference through a normalized
multi-instance control port, observe that exact returned reference, and reserve
the same client key for every linked filesystem/native action. Do not bind a
global action-kind handler to one download record. Add executor-level collision
coverage for linked filesystem versus client stop/remove actions.

## Passing boundaries

Direct inspection and existing tests found no generated upstream DTO, direct
HTTP, SQL, arbitrary command, or standalone client-module import in
`internal/actions`. Intent decoding is bounded, rejects unknown fields and
recursive duplicate keys, and validates action kind/identity/deadline before
dispatch. Existing focused tests preserve G-01's unknown capability block with
zero Arr writes, reject preview mismatch before import, propagate the F-05
unsupported gate without fallback, require descriptor deletion acknowledgement,
and keep Jellyfin refresh acceptance distinct from later availability. These
passing boundaries do not compensate for R1-R4.

## Independent checks

All baseline commands passed with exit status zero. Go commands used
`GOWORK=off`; offline guards also used `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Built-in `go test -mod=readonly -count=3 -timeout=180s ./internal/actions` | Passed. |
| Built-in `go test -mod=readonly -race -count=3 -timeout=240s ./internal/actions` | Passed. |
| Focused `go vet -mod=readonly ./internal/actions` | Passed. |
| Six independent R1-R4 regression probes, individually under `-race -count=10` | Expected regressions failed 10/10 each, reproducing the findings above. |
| Product/handoff/checkpoint identities, ancestry, scoped drift, clean worktree, and `git diff --check` | Passed. |
| Generated/import-boundary inspection | Passed; no generated DTO or direct transport leakage in `internal/actions`. |
| Offline `./scripts/check-guardrails.sh --ci` | Baseline passed in 334.60s, including generation, staged generation, all Vacuum contracts, architecture, lint, root and nested tests/race/vet/mod verification, and cross-builds. Root storage race completed in 230.514s. |

## Disposition

Do not integrate W-03 as complete. Correct R1-R4 and add permanent regressions
for partial effect preservation, post-call uncertainty, same-root native rename,
dynamic linked references, and shared client reservations. Re-review must use
the exact correction product and handoff SHAs.

G-01 remains open and native Arr writes must stay fail-closed. F-05 remains
capability-blocked where its port reports unsupported. This receipt does not
authorize release, deployment, live upstream writes, or live filesystem/media
mutation.
