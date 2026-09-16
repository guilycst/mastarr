# W-03 independent review, round three

## Decision

`changes_requested`.

Round two closes both prior P1 findings: manifest actions now map exact affected
entries, and the approved W-02 executor durably records returned effects on an
errored dispatch while marking omissions unknown. One P2 remains. The mapper
replaces a read-proven `already_satisfied` target with the aggregate port
outcome, so a mixed manifest batch loses its exact per-effect state in both the
handler result and durable journal.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- First product commit:
  `533313c5d1f91e4573078bf5fa1d2939e21c17b0`.
- First product tree: `42696cdd486fea6c72ebe5dcef717119e556cbea`.
- First product parent:
  `1130fbd75e0be84063642c72faf19f7a970ed723`.
- Final product commit:
  `4871bc4999f18f2d6aa119e366ca949b6886ac45`.
- Final product tree: `8328f47a3e0d98177113d40c872bde4d5c114ea9`.
- Exact handoff: `344b9be5010d9bec7666602dc5d67049ba727850`;
  its direct parent is the final product commit.
- Handoff tree: `554856e2c26f37a78805a6c240c9e8438be19ed7`.
- Exact review-dispatch checkpoint:
  `bdcdcce0d6186fd1d47403516e4f5cdc60d4bdc9`.
- Checkpoint tree: `864aab3c89ecac30d3aeca3fd3623058b7751f25`.
- Reviewed product scope: `internal/actions/`. The approved W-02 executor and
  filesystem ports were inspected and exercised only as dependencies.
- Acceptance reviewed: A-13, A-14, A-17, A-18, A-20, A-24, A-33, and A-60.

Review ran in a clean detached worktree at the exact checkpoint.
`git diff --exit-code 4871bc4 bdcdcce -- internal/actions` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran in a temporary archive of the checkpoint using synthetic
ports and temporary SQLite journals. No live service, credential, private
coordinate, inventory, or media was used.

## Finding

### R2c — P2: aggregate outcome overwrites a read-proven per-target state

`mapFilesystemAffected` copies the exact observed effect at
`internal/actions/filesystem.go:693`, but lines 697-704 then unconditionally
replace its state with the aggregate `FilesystemEffect.Outcome` for every
matched affected entry.

A mixed permanent-delete manifest can legitimately contain:

1. a source already absent during the required pre-dispatch observation, which
   creates `EffectAlreadySatisfied`; and
2. a source still present and then removed by the action port.

The organizer includes already-satisfied entries in `Affected`, and an applied
entry makes the aggregate outcome `applied`. The mapper therefore returns
`applied, applied` instead of preserving `already_satisfied, applied`.

An independent direct-handler probe reproduced this in 10/10 race repetitions.
A second probe ran the real `FilesystemHandler` through the approved W-02
executor and a temporary SQLite journal; the persisted effects were also
`applied, applied` in 10/10 race repetitions. The action remained dispatched
and reconciling, so this does not permit a blind retry. It does misstate which
file was changed by this attempt and violates the required exact per-effect
state and A-60 audit evidence.

Required correction: mirror `terminalEffects` semantics when mapping an affected
subset. Preserve an observation already proven `EffectAlreadySatisfied`; apply
the returned aggregate outcome only to matched effects that were pending. Add
handler-level and executor-level mixed delete regressions. The same rule should
cover mapping actions whose destination was already satisfied before another
file changed.

## Closed prior findings and preserved behavior

- **R2a closed:** trash/delete derive approved identities from the recursively
  flattened `intent.Manifest`; mapping actions use expanded `intent.Files`.
- A child of the second top-level manifest maps to that top-level effect.
  Parent-plus-child reports remain one approved top-level effect.
- Duplicate, foreign, ambiguous, incomplete, and digest-conflicting returned
  entries fail closed rather than yielding an accepted effect set.
- An independent real-executor delete probe preserved one returned applied
  manifest effect, marked the omitted target unknown with
  `dispatch_result_unreported`, entered reconciliation, and called delete once.
- **R2b closed:** handler effects, observation time, and evidence returned with
  an error reach the durable journal; omissions remain explicitly unknown.
- F-05 unsupported capabilities remain fail-closed with no fallback mutation.
- Linked-client references select the exact normalized port, share
  `client:<ref>` reservations, and require the stopped prerequisite.
- Cancellation, read-before-write idempotency, native scope fences, descriptor
  acknowledgement, refresh acceptance separation, and G-01 Arr write blocking
  remain intact.
- Malformed or foreign reports never authorize a retry. The approved W-02
  executor retains reconciliation and no-blind-redispatch behavior.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| Product focused manifest, mapping, malformed-report, and executor regressions under race, `-count=10` | Passed. |
| Independent nested-second-manifest, duplicate-child, foreign-child, and partial-delete executor probes under race, `-count=10` | Passed. |
| Independent mixed delete handler-state probe under race, `-count=10` | Failed as expected 10/10: `already_satisfied` became `applied`. |
| Independent mixed delete real-executor/SQLite probe under race, `-count=10` | Failed as expected 10/10 with the same persisted state loss; action remained reconciling. |
| F-05 capability, linked-client reservation/scope, native routing, observe-before-write, and filesystem reconciliation regressions under race, `-count=10` | Passed. |
| Full `internal/actions` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/actions` and root `go mod verify` | Passed. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

Do not mark W-03 complete. Preserve the closed manifest matching and durable
executor behavior, correct R2c, add the mixed-state regressions, and re-review
the exact correction. This receipt does not authorize release, deployment,
live upstream writes, or live filesystem/media mutation.
