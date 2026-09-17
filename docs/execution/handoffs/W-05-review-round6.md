# W-05 independent review, round six

## Decision

`changes_requested`.

R3d and R3e are closed. A terminal item whose filesystem state is no longer
exact trash-only now holds the entry across restart, blocks explicit retry and
cannot contribute to aggregate completion. Read-only reconciliation also keeps
the exact preceding action envelope, including item-bound result, error,
timestamp and unresolved-scope evidence.

One P2 remains. Every later read-only reconciliation embeds the complete
current journal as a new `priorOutcome`. An unresolved entry processed by the
periodic janitor therefore creates an unbounded recursive JSON history even
when no state changes. This eventually threatens restart/reconciliation and
resource bounds required by A-25 and A-57.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- State dispatch parent:
  `d778d5a576ec31d56ddab681937a11b4a5a73718`.
- Exact product: `73c55183faef67d20286b7656886b3e672cf8ef9`,
  whose direct parent is the state dispatch parent.
- Product tree: `54160f721ba34703121b7fc20348a181f38178b5`.
- Exact handoff: `e65b3603821154df77790845e7d6b4bd14cce58c`,
  whose direct parent is the product.
- Handoff tree: `b8decb22512c15b0a72e68fb69d1fc911ef60d6d`.
- Exact review checkpoint:
  `3ceaaa21761a327922f521b41e39f0dfa5141423`, whose direct parent is the
  handoff.
- Review-checkpoint tree:
  `a0badcb36fab9b6f551582542f944ea949500b1b`.
- Scoped correction diff SHA-256 from round-five product
  `2ba78ca03c1b3f2afa31949782f75af012d8aab6`:
  `1d31c8b4d187efe3fdb91b1f49ab2224a933fc2ae0d0eda06b77df1948a35bc9`.
- Product-scope archive SHA-256:
  `5d7e278ef055c1154ba82c47fecd6d2bfa7d6a52db4326a760ef0e31ec89d8de`.
- Reviewed product paths: `internal/trash/` only. There is no product-path
  drift from the exact product to the review checkpoint.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30 and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used only synthetic manifests, temporary SQLite databases and fake
normalized ports. Their archived source SHA-256 is
`63ebd3175562b61a060389fbbe9aa07bdab7394c2fe0785a817ca2e37cd1b9b3`.
No product, state, schema, API, adapter, client, worker, live service or real
media data was changed.

## Finding

### R3f - P2 - Repeated reconciliation grows an unbounded recursive journal

`persistTrashReconciliation` assigns the complete current trash journal to
`priorOutcome` at lines 1189-1195. `encodeTrashReconciliation` inserts that
document as a nested raw JSON value at lines 1235-1244. Once the current
journal is itself a reconciliation envelope, it already contains the preceding
`priorOutcome`. The next observation nests the entire chain again. There is no
depth, byte, count, deduplication or compaction limit.

This path is automatic. `Tick` read-only reconciles a planned source-only
entry, so an unchanged entry gains another nested copy on every janitor pass.
The recursive unresolved-scope checks must then decode and traverse the growing
tree. Database writes and future reads grow continuously without new evidence.

Independent reproduction:

1. Seed one exact planned trash entry with its source present and destination
   absent.
2. Call `ReconcileTrash` 128 times without mutating the filesystem or invoking
   a filesystem action.
3. Parse the stored purge-journal outcome after each observation.
4. The first envelope is 967 bytes. The final envelope is 125,808 bytes and has
   127 nested `priorOutcome` objects.

The result reproduced in ordinary and race runs. This is roughly 130 times the
initial record size for unchanged evidence and has no terminal bound. A normal
periodic janitor can therefore exhaust storage/CPU or exceed JSON nesting and
record limits, leaving the durable entry unrecoverable.

Preserve the immutable action result once. Store the current observation in a
replaceable bounded field, or use a bounded history with explicit count/size
limits and compaction. The fix must retain the original item-bound action and
exact foreign/duplicate/ambiguous evidence while avoiding recursive copies.

## Closed correction behavior and preserved boundaries

- Source-only, both-present, neither-observable and changed-identity states for
  a terminal item all produce `terminal_item_stale`, persist item evidence,
  return held/non-retryable from a fresh service and cause zero retry actions.
- Exact trash-only terminal items remain excluded from the retry manifest.
  Pending items remain independently visible.
- Reconciliation preserves the prior applied item effect, action error,
  timestamp, action evidence and exact foreign/duplicate/ambiguous scope
  evidence. It appends current read-back instead of silently replacing those
  fields.
- R3b source/trash/both/neither/identity/restart reconciliation and R3c partial
  retry item accounting remain passing. Foreign, duplicate and contradictory
  effects stay held and cannot authorize a second delete.
- R1a approval-bound early purge, R2a exact affected-set validation, R2b
  directory-leaf accounting and R3a no-blind planned replay remain passing.
- Default/custom retention, qBittorrent stop-before-payload, metadata-only
  removal and restore without re-add/resume remain behind normalized ports.
- F-05 trash/restore/permanent-delete capability remains unsupported and
  fail-closed. G-01 Arr writes remain disabled. X-14 qBittorrent metadata
  removal remains distinct and uses `deleteFiles=false`.
- No generated DTO, direct upstream transport, credential, private coordinate,
  live service, local `replace` or tracked `go.work` entered the reviewed scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off`; the aggregate additionally used
`GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| Independent terminal-stale matrix, ordinary `-count=10` | Passed; all four stale states held across a fresh service with zero retry actions. |
| Independent terminal-stale and recursive-history probes, `-race -count=5` | Passed; package time `66.755s`. The unbounded history reproduced each run. |
| Independent recursive-history probe, 128 observations | Reproduced R3f: depth `127`, bytes `967` to `125808`. |
| `go test ./internal/trash -count=10 -timeout=300s` | Passed; package time `22.663s`. |
| Focused correction and prior W-05 regressions, `-count=10` | Passed; package time `21.215s`. |
| `go test -race ./internal/trash -count=3 -timeout=420s` | Passed; package time `176.530s`. |
| `go test ./... -count=1 -timeout=300s` | Passed. |
| `go vet ./internal/trash` and root `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases and local links resolved. |
| Full offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in `293.63s`; final marker `guardrail checks passed (ci)`. Aggregate race, storage race (`200.816s`), trash race (`62.099s`), generation, staged generation, Vacuum, lint, architecture, module tests/vet/verification and Linux amd64/arm64 cross-builds passed. |

## Acceptance disposition

- A-25: blocked by R3f. The periodic janitor can grow an unchanged planned
  record indefinitely instead of maintaining bounded recoverable state.
- A-26: accepted for W-05 scope. Approval-bound early purge, retention override
  and direct hard-delete regressions pass.
- A-27: accepted for W-05 scope. Changed terminal identity/state now holds and
  cannot authorize retry or false completion.
- A-29: accepted for W-05 scope. Whole-client stop and metadata-only removal
  remain separate and ordered around selected payload handling.
- A-30: accepted for W-05 scope. Restore performs no automatic re-add or resume
  and preserves association evidence.
- A-57: blocked by R3f. Restart/read-back evidence is retained, but the durable
  representation grows without a resource bound on every unchanged read-back.

This receipt does not approve integration, release, deployment, live
filesystem mutation or live client control.
