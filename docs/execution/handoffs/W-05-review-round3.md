# W-05 independent review, round three

## Decision

`changes_requested`.

R1a, R2a, and R2b are closed by this correction. R3a closes blind
redispatch, but it does not provide the required read-only reconciliation path
for a durable `planned` trash intent. One P1 finding remains, so W-05 cannot
close A-27 or A-57.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Correction dispatch base:
  `8df72860d4ef56e69393753a3814c288bb9860d2`.
- Dispatch record:
  `918346ee5dca4c08681970c84f308dda45b4c809`, whose parent is the dispatch
  base.
- Exact product: `730b3774fb412c2d9dcecce5a5dec292d11f63eb`,
  whose parent is the dispatch record.
- Product tree: `68d921b9df952e7982422ab2fdf04861364b439d`.
- Exact handoff: `daa7df6d2612439b07556e9e776e1dd4a17f0983`,
  whose direct parent is the product.
- Handoff tree: `5f5f6dac1838887b5ae3f0e83b1b6e64c24213ac`.
- Exact review/state checkpoint:
  `c3c2fd91d47dc2afaa77ea0c872029fa45783576`, whose direct parent is the
  handoff.
- Review checkpoint tree:
  `8f3a08ffa6ae4b218a5892f48310fd0a5556de55`.
- Scoped correction diff SHA-256 from the round-two product:
  `ac770caf55782eac622a61e34f72820ce8995df157b32fd3e3d5c2558a8679f0`.
- Product-scope archive SHA-256:
  `8ef80d619178b2fcf5bb85187e8eecc4651824472e821a2b8107166eb895ecd8`.
- Reviewed product paths: `internal/trash/trash.go` and
  `internal/trash/correction_round2_test.go` only.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30, and A-57.

Review ran from a clean detached worktree at the exact review checkpoint.
`internal/trash` has no delta between product and review checkpoint.
Independent probes ran from a temporary archive of that checkpoint with
synthetic SQLite databases, paths, media metadata, and normalized ports. Final
probe source SHA-256:
`c81c2d4193f0c67f820a628c9a3ed207fcfd26d8fd86905367967a52246d712a`.
No product, state, schema, API, adapter, client, worker, shared-worktree, live
service, or real media data was changed.

## Finding

### R3b - P1 - Durable planned trash intents have no read-only recovery path

The correction prevents the blind redispatch from round two. Every existing
entry now returns through `replayTrashResult` at
`internal/trash/trash.go:357-365`, and a `planned` entry returns `ErrClaimed`
at `internal/trash/trash.go:2516-2524` without preflight or another filesystem
mutation.

That branch does not perform the read-only reconciliation promised by its
comment and the correction handoff. `Reconcile` accepts only purge and restore
at `internal/trash/trash.go:731-740`; `operationTrash` is rejected as
`ErrInvalidRequest`. `Recover` only recovers running janitor claims, and
`Tick` skips a non-recovering entry whose state is not `trashed` at
`internal/trash/trash.go:568-573`. No API in this service can inspect the
original and mapped trash locations and resolve the durable `planned` intent.

Independent probes covered both crash windows:

- before dispatch, the approved source remained at its original path;
- after dispatch/lost response, the exact object existed only at its mapped
  trash path.

In both cases, an identical `Trash` replay returned `ErrClaimed`, performed
zero filesystem reads and zero action calls, and left the entry `planned`.
After advancing beyond retention, `Tick` processed zero work. Explicit
`Reconcile(..., operationTrash)` returned `ErrInvalidRequest`; another replay
remained `planned`. The behavior reproduced 10/10 ordinary runs and 5/5 race
runs for both crash windows.

This avoids a repeated mutation but strands the durable intent. In the
post-dispatch case, payload can already be inside application trash while its
entry never reaches the trash retention/restore lifecycle. In the
pre-dispatch case, authorized work can never progress under the same immutable
entry. This violates restart recovery and honest current-state requirements in
A-27 and A-57.

Add an explicit trash reconciliation operation that reads every exact original
and mapped destination before any decision. It must distinguish source-only,
trash-only, both-present collision, neither-observable, changed identity,
partial multi-file, and directory-leaf states; persist per-item evidence; and
either finalize already materialized trash or expose a separately authorized
retry. A replay must never infer safety from state alone.

## Closed findings and preserved boundaries

### R1a - closed

Approved early purge recovery now retains the immutable plan, revision, digest,
decision, action-run, entry-version, and action-run-version binding. Initial
and recovered approved claims perform read-only reconciliation before delete.
`Recover` plus a fresh worker and exact evidence reaches one delete through the
approved reconciliation CAS. Missing evidence releases the owner lease back to
durable reconciliation with `trash_source_unobservable`; no delete occurs.
Foreign approval cannot claim the stored binding. Independent ordinary and race
probes confirmed both fresh-worker Tick and lost-read behavior.

### R2a - closed

Foreign, duplicate, contradictory, and missing purge effect reports produce a
durable `scope_unresolved` hold. The approved item does not become purged from
an invalid response. `Reconcile`, `Retry`, and direct purge replay issue no
second delete and cannot replace unresolved evidence with aggregate success.
The full four-case matrix reproduced in ordinary and race probes.

### R2b - closed

Directory manifests persist only immutable leaves as actionable items. Root
effects are rejected, partial leaf effects stay held, and complete leaf purge
and restore reach honest terminal per-item states. Independent two-leaf partial
purge and partial restore probes preserved one completed and one remaining item,
retained unresolved scope, and made no blind second call.

### Other boundaries

- Default/custom retention and ordinary expiry behavior remain intact.
- qBittorrent stop remains before payload mutation. Metadata removal remains a
  distinct normalized-port step after selected payload completion; the port
  contract preserves X-14 `deleteFiles=false` semantics. Restore does not
  re-add or resume the client.
- F-05 trash, restore, and permanent-delete capabilities remain unsupported and
  fail closed on the reviewed platform implementation. This correction does
  not broaden them.
- `internal/trash` imports domain, ports, storage, and standard-library packages
  only. It imports no Arr adapter or upstream generated package, performs no
  Arr write, and leaves G-01 intact.
- No generated DTO, direct upstream transport, credential, private coordinate,
  live service, local `replace`, or tracked `go.work` entered this scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact base/dispatch/product/handoff/review identities, ancestry, trees, clean worktrees, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| `go test ./internal/trash -count=20 -timeout=300s` | Passed; package time `32.409s`. |
| `go test -race ./internal/trash -count=5 -timeout=420s` | Passed; package time `197.545s`. |
| `go vet ./internal/trash` and root `go mod verify` | Passed; all modules verified. |
| Final independent probe suite, `-count=10` | Passed; package time `9.459s`. R1a/R2a/R2b safety properties held and both R3b stuck states reproduced every run. |
| Independent probe suite before the additional pre-dispatch case, `-race -count=5` | Passed; package time `76.411s`. |
| Both planned-intent crash-window probes, `-race -count=5` | Passed; package time `20.451s`; both stuck states reproduced every run. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| `./scripts/check-guardrails.sh --ci` | Passed, exit 0; final marker `guardrail checks passed (ci)`. Deterministic and staged generation, root and standalone Vacuum, lint, architecture, root/UI/tools/client tests and race, vet, module verification, and Linux amd64/arm64 cross-builds passed. Root storage race completed in `214.690s`. |

## Acceptance disposition

- A-25: accepted for W-05 scope. Default/custom retention, ordinary no-early
  purge, and startup/periodic janitor behavior retain passing evidence.
- A-26: accepted for W-05 scope. Exact approved early purge works through
  initial and recovered approval-bound claims; missing evidence cannot delete.
- A-27: blocked by R3b. Planned intent remains durable but is not recoverable
  before or after a lost trash response. Corrected purge/restore partial state
  and scope behavior otherwise pass.
- A-29: accepted for W-05 scope. Whole-client stop precedes payload work and
  metadata-only removal follows exact payload completion.
- A-30: accepted for W-05 scope. Restore preserves association evidence and
  performs no client re-add or resume.
- A-57: blocked by R3b. Purge/restore uncertainty no longer retries blindly,
  but initial trash uncertainty has no read-only reconciliation path or honest
  terminal/pending-effect progression.

Correction should address R3b with explicit durable trash reconciliation and
focused pre-dispatch, post-dispatch, partial, collision, identity-change, and
restart probes before another independent review. This receipt does not approve
integration, release, deployment, live filesystem mutation, or live client
control.
