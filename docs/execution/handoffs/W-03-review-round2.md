# W-03 independent review, round two

Decision: **changes requested**. R1, R3, and R4 from round one are closed at
the handler boundary. R2 remains open in two places: manifest-based filesystem
actions do not map returned affected entries, and the durable executor discards
all handler effect evidence whenever dispatch returns an error. Both are P1
because the reviewed runtime can lose exact per-file evidence after a partial
mutation.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`; independent of product authorship.
- Exact correction product: `0c120a1bf5e377510d332ebdadac61e40f77e652`.
- Product tree: `db6ffb007d6f609f5605446ec2b0ed940107c474`.
- Product parent: `0d81a75477afd9825b126a90e2791b2c596d8ee7`.
- Exact correction handoff: `735e87f1de11e592cd8b3bea485e2f92d5b3ac86`;
  its direct parent is the product commit.
- Handoff tree: `45e5375450322e59979d1efbe794ef32c4173a06`.
- Exact review-dispatch checkpoint:
  `2c27698ba1a7521b0a86819d86472c2d379aac00`.
- Dispatch tree: `ef68d49b1b907d77c0560dd57c211216b75262b1`.
- Prior review receipt: `577a15a61947ac44ec7621d381b63a4a6cb87a1a`.
- Reviewed product scope: `internal/actions/`; the frozen
  `internal/execution/` and filesystem ports were inspected only to verify the
  handler contract in the real runner.
- Acceptance reviewed: A-13, A-14, A-17, A-18, A-20, A-24, A-33, and A-60.

Review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code 0c120a1 2c27698 -- internal/actions` passed. Product,
state, generated output, and unrelated files were not edited. Independent
probes ran only in a temporary archive of the checkpoint. All fixtures were
synthetic; no live service, credential, private coordinate, inventory, or media
was used.

## Findings

### R2a — P1: trash/delete affected entries are not mapped

`FilesystemHandler.Dispatch` correctly retains the `FilesystemEffect` returned
together with an error and passes it to `filesystemDispatchResult` at
`internal/actions/filesystem.go:238-246`. The mapper, however, builds its
identity set only by expanding `intent.Files` at lines 592-595. Trash and delete
are manifest actions: `callAction` sends `intent.Manifest`, while
`intent.Files` is empty. `expandedMappings` fails, the mapper returns early,
and every returned `Affected` entry and its evidence are ignored.

An independent synthetic trash probe used one exact manifest. The port returned
that entry in `Affected`, `OutcomeApplied`, evidence
`trash_object_published`, and `placement.ErrSourceChanged`. Dispatch truthfully
returned a dispatched/uncertain failure, but its sole effect remained `pending`
with only the pre-call `source_present` evidence. The required regression failed
10/10 race repetitions.

This is not theoretical for the frozen action ports. The organize delete path
accumulates removed entries and returns them with an error at
`internal/filesystem/organize/organize.go:343-360`. Trash is even harder to
recover from a later ordinary read: after the source disappears,
`observeTrash` deliberately reports unknown because its retained destination is
not exposed through the read port. Dropping the returned effect therefore loses
the only exact per-file mutation evidence and violates A-24/A-33.

Required correction: derive the approved source identity set from
`intent.Manifest` for trash/delete and from expanded `intent.Files` for mapping
actions. Match every returned affected entry to exactly one approved effect,
reject duplicate/foreign/ambiguous entries, and preserve its state, observation
time, and evidence.

### R2b — P1: the durable runner discards every errored dispatch result

Even for copy, where the new mapper returns the correct affected effect, the
runtime does not persist it. `Executor` receives both values at
`internal/execution/execution.go:1705`, but on any non-nil error lines 1707-1708
call `finishDispatchError` without the `DispatchResult`. The uncertain path at
lines 2334-2351 then calls `recordUnknownEffectsOwned` with no reported effects.
That helper rewrites every planned target to `unknown`; handler state and
handler evidence never reach the journal.

An independent executor probe returned one exact `applied` effect with
`first_file_published` evidence together with a dispatched/uncertain error. The
action correctly entered reconciliation, but the journal stored the target as
`unknown` with `{}` evidence in 10/10 race repetitions. This directly disproves
the correction handoff's claim that returned effect evidence is retained for
later reconciliation. The permanent correction test invokes the handler
alone, so it cannot detect this contract loss.

Required correction: extend the durable dispatch-error path to accept and
strictly validate a returned partial effect set against the pre-created exact
targets, then persist those states/evidence while marking all unreported targets
unknown. Invalid, duplicate, foreign, incomplete-identity, or contradictory
reports must fail closed and must never authorize mutation retry. Add an
executor-level regression using the real handler contract. Until the shared
W-02/W-03 contract supports this, A-17/A-24/A-33/A-60 remain incomplete.

## Closed round-one findings and preserved gates

- **R1 closed:** mixed Arr import and filesystem observations retain their
  handler-level per-target states and return `SafeToRetry=false`. The five
  focused R1-R4 correction tests passed 10/10 under the race detector. The
  executor cannot blindly redispatch these mixed batches.
- **R3 closed:** native rename validates the same root, containing directory,
  and destination basename before linked-client observation. The cross-root
  probe made zero observe/rename calls in every repetition.
- **R4 closed:** the immutable action reference selects the expected normalized
  control port, returned reference identity is checked, and linked filesystem
  plus client handlers emit the same `client:<ref>` reservation.
- Observe-before-write, exact preview binding, cancellation checks, bounded
  intent decoding, descriptor acknowledgement, refresh acceptance separation,
  normalized native-client routing, and read-before-write idempotency remain
  intact by inspection and focused tests.
- G-01 remains fail-closed with zero native Arr writes. F-05 remains
  capability-blocked where the reviewed filesystem port reports unsupported.
  No generated upstream DTO, direct transport, arbitrary command, local
  `replace`, or `go.work` dependency was introduced.

## Independent checks

Go commands used `GOWORK=off`; offline commands also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| `go test -mod=readonly -count=10 -timeout=180s ./internal/actions` | Passed. |
| `go test -mod=readonly -race -count=10 -timeout=300s ./internal/actions` | Passed. |
| `go vet -mod=readonly ./internal/actions` | Passed. |
| Root `go mod verify` | Passed. |
| Five focused R1-R4 permanent regressions under `-race -count=10` | Passed. |
| G-01, exact preview, F-05, descriptor, refresh, and native-client focused regressions under `-race -count=10` | Passed. |
| Independent manifest-action affected-effect regression under `-race -count=10` | Failed as expected 10/10; returned applied/evidence remained pending and was discarded. |
| Independent durable-executor returned-effect regression under `-race -count=10` | Failed as expected 10/10; journal stored unknown with empty evidence. |
| Product/handoff/checkpoint ancestry, scoped drift, `git diff --check`, and clean worktree | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in 318.13s. Generation, staged generation, root and standalone Vacuum, architecture, lint, all module tests/race/vet/mod verification, and Linux amd64/arm64 cross-builds passed; root storage race completed in 216.997s. |

## Disposition

Do not integrate W-03 as complete. Correct R2a in the action mapper and R2b in
the durable handler/executor contract, add direct and executor-level permanent
regressions, and re-review exact correction SHAs. This receipt does not
authorize release, deployment, live upstream writes, or live filesystem/media
mutation.
