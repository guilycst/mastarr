# W-03 independent review, round four

## Decision

`approved`.

Round three closes R2c. A returned aggregate filesystem outcome now terminalizes
only affected effects that were pending at the required pre-dispatch read. A
target already proven `EffectAlreadySatisfied` remains unchanged in both the
direct handler result and the durable SQLite journal. No P1 or P2 finding
remains in the reviewed W-03 scope.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Exact product: `a4ad5a0fcfee724491513224f6b082a25be82e0a`.
- Product parent: `0d48147504dd73623d65b3e79c6d6d85eea465a4`.
- Product tree: `e1a6c6e96f66cffe8e69a929f99b03132863145d`.
- Exact handoff: `d1fb6c37f8254455e6dc773a57ec707639004dcd`;
  its direct parent is the product commit.
- Handoff tree: `851e1d8cb481fce34d6b6a1dfa259b58875f1f55`.
- Exact review-dispatch checkpoint:
  `395413fe60878715e89195501e34bd4f8bf1031e`.
- Checkpoint tree: `c255ba4b1fe6dfbb54a71792ac6ce407e752000a`.
- Reviewed product scope: `internal/actions/` and the correction handoff only.
  The approved W-02 executor and filesystem ports were exercised solely as
  dependencies.
- Acceptance reviewed: A-13, A-14, A-17, A-18, A-20, A-24, A-33, and A-60.

The shared checkout HEAD was confirmed as the exact state checkpoint before
review. Review then ran in a clean detached worktree at that checkpoint.
`git diff --exit-code a4ad5a0 395413f -- internal/actions` passed. Product,
state, task, schema, generated output, and unrelated files were not edited.
Independent probes ran in a temporary archive using synthetic ports and
temporary SQLite journals. No live service, credential, private coordinate,
inventory, or media was used.

## R2c closure

`mapFilesystemAffected` retains the observed effect and now changes its state
only when that state is `EffectPending`. This matches the established
`terminalEffects` rule:

- a pre-dispatch absent delete target remains `EffectAlreadySatisfied`;
- a present target reported as affected by an applied aggregate becomes
  `EffectApplied`;
- returned observation time and evidence remain attached to both exact effects;
- an errored dispatch remains dispatched and uncertain, enters reconciliation,
  and cannot trigger blind redispatch.

The prior failing mixed-delete direct-handler and real SQLite executor probes
passed 10/10 under race. The producer regressions independently exercise the
same two boundaries.

## Preserved behavior

- Trash/delete match returned entries against recursively flattened
  `intent.Manifest`; mapping actions use expanded `intent.Files`.
- Nested children map to the correct top-level manifest effect. Duplicate,
  foreign, ambiguous, incomplete, and digest-conflicting reports fail closed.
- Returned partial effects, state, observation time, and evidence persist on
  errored dispatches. Omitted planned targets become explicit unknown effects
  with `dispatch_result_unreported`.
- Read-before-write idempotency, cancellation and uncertainty handling remain
  intact.
- F-05 unsupported filesystem capabilities remain fail-closed with no fallback.
- Linked-client actions retain exact `client:<ref>` reservations and stopped
  prerequisites. Native-client scope validation occurs before calls.
- G-01 remains fail-closed; no Arr mutation capability, generated DTO exposure,
  direct transport, or live write was introduced.

## Independent checks

All Go commands used `GOWORK=off`; the aggregate command also used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Product/handoff/checkpoint identity, ancestry, exact HEAD, scoped drift, `git diff --check`, no tracked `go.work`, and no local `replace` | Passed. |
| Prior independent nested-manifest, malformed-report, partial-delete, mixed-state handler, and real SQLite executor probes under race, `-count=10` | Passed. |
| Producer mixed-state handler/SQLite, manifest mapping, partial effect, F-05 capability, linked-client reservation, idempotency, and native-scope regressions under race, `-count=10` | Passed. |
| Full `internal/actions` tests, normal and race, each `-count=3` | Passed. |
| `go vet ./internal/actions` and root `go mod verify` | Passed. |
| Architecture and lint boundaries | Passed through the aggregate gate. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0. Reproducible and staged generation, all Vacuum contracts, architecture, lint, nine-module tests/race/vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Disposition

W-03 correction round three is approved for coordinator integration and
execution-state recording. This approval covers the reviewed local product tree
only. It does not authorize release, deployment, live upstream writes, or live
filesystem/media mutation. Separate lane findings remain outside this receipt.
