# W-05 independent review, round four

## Decision

`changes_requested`.

R3b is closed for durable `planned` intents: source-only, trash-only,
collision, missing, changed-identity, mixed directory-leaf, restart and Tick
paths now perform bounded read-only reconciliation without blind dispatch.
One P1 remains in the newly introduced explicit retry path. A partial,
identity-valid filesystem effect is not mapped back to its exact item, so the
durable item state and returned effects lose which file was moved. W-05 cannot
close A-27 or A-57 until this is corrected.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Correction dispatch/state base:
  `6c2079a8e5812cd57866e31fdd22d00d5465e1a2`.
- Exact product: `985be793b184f090599b2ee7aea3860993091139`,
  whose direct parent is the dispatch/state base.
- Product tree: `11ae43a736274040116cf115cdc9f265dcef1d80`.
- Exact handoff: `735086cd7123ba2527f02dc7346a606ab79c6ed0`,
  whose direct parent is the product.
- Handoff tree: `3ed671bc9045feb1dcf46c4e649c1737228d40be`.
- Exact review checkpoint:
  `1c7361828c2520ce55bf1a841f054fb315b6a2de`, whose direct parent is the
  handoff.
- Scoped correction diff SHA-256 from round three product
  `730b3774fb412c2d9dcecce5a5dec292d11f63eb`:
  `eddcb8b2a67ba164d06b284000d8864b1bc9a49689318c95d653dbdb17ef4e0f`.
- Product-scope archive SHA-256:
  `c9753ed6064af9bf3f490e84cff94d73a89d4df019afc384fd355d2344434e5f`.
- Reviewed product paths: `internal/trash/` only. There is no product-path
  drift from the exact product to the review checkpoint.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30 and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes ran from a temporary archive with synthetic SQLite state, manifests and
normalized ports. Final probe source SHA-256:
`dab89e02013f9e942e656e85a35f0d4d60f0d4bf0d7b622beb91bbdfc7ae9e45`.
No product, state, schema, API, adapter, client, worker, live service or real
media data was changed.

## Finding

### R3c - P1 - Explicit trash retry loses an exact partial applied effect

`finishClaimedTrash` correctly computes `matched` and `scopeIssues` at
`internal/trash/trash.go:1199-1200`. When the effect is incomplete, however,
the non-complete branch at lines 1227-1229 emits one aggregate `unknown`
effect with no `ItemID`. The transaction updates `trash_items` only inside the
`complete` branch at lines 1239-1244. The held branch at lines 1254-1263
therefore leaves every item `selected`, including an identity-valid item named
in `FilesystemEffect.Affected`.

This differs from the original trash-finalization path, which maps the exact
affected set and marks each matched item `trashed` even when the entry is held
for an incomplete effect (`internal/trash/trash.go:1767-1785` and
1796-1803).

Independent reproduction:

1. Seed a durable planned trash intent with two exact source files.
2. Reconcile both as `source_only`, then call the explicit `RetryTrash` path.
3. Return `OutcomeApplied` for the first exact file only, with the second file
   omitted.
4. Observe one filesystem call and `ErrHeld`, as required for safety.
5. Reload the entry: both items remain `selected`; the returned effect is one
   `unknown` aggregate with an empty `ItemID`.
6. `ReconcileTrash` and another `RetryTrash` both reject the now-held entry,
   while no second filesystem call occurs.

The defect reproduced 20/20 ordinary runs and 10/10 race runs. The no-blind-
redispatch boundary holds, but the service cannot identify the applied item or
derive the exact remaining retry scope from its item state. That violates
A-27's recoverable per-item remaining state and A-57's visible pending-effect
requirement after an uncertain or partial filesystem result.

Preserve each trustworthy matched item and its item-bound effect atomically
when an explicit retry returns a partial result, while keeping the entry held
and all unmatched, foreign, duplicate or contradictory scope unresolved.
Provide a read-only reconciliation path for the resulting held state so a
fresh process can resolve exact remaining objects without resubmitting the
whole manifest.

## R3b and prior safety gates

- `planned` replays now inspect every exact original and mapped trash path.
  Source-only stays planned and requires explicit retry; trash-only finalizes
  as already satisfied without an action call.
- Both-present, neither-observable and changed-identity observations hold.
  Mixed directory leaves persist both item observations and directory-leaf
  evidence. Recover and Tick rediscover planned entries without mutation.
- Source-only explicit retry repeats reconciliation, takes a durable lease,
  preflights the stored manifest and performs at most one dispatch. A lost
  retry response becomes held and does not redispatch after restart.
- R1a approved early-purge recovery, R2a unresolved foreign/duplicate effect
  scope, R2b directory leaf accounting and R3a planned replay protections
  retained passing regressions.
- Default/custom retention, expiry, qBittorrent stop-before-payload,
  metadata-only client removal, and restore-without-re-add/resume remain
  bounded by the existing normalized ports.
- F-05 filesystem trash/restore/permanent-delete remains unsupported and
  fail-closed. G-01 Arr writes remain disabled. X-14 metadata removal remains
  separate from payload deletion and retains `deleteFiles=false` semantics.
- No generated DTO, direct upstream transport, credential, private
  coordinate, live service, local `replace` or tracked `go.work` entered the
  reviewed scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| `go test ./internal/trash -count=10 -timeout=300s` | Passed; package time `19.625s`. |
| `go test -race ./internal/trash -count=3 -timeout=420s` | Passed; package time `149.102s`. |
| Focused prior planned/approved/partial/directory regressions, `-count=10` | Passed; package time `8.174s`. |
| Independent planned matrix, restart/Tick, directory partial and lost-retry probes, `-count=20` | Passed; package time `13.693s`. |
| Independent R3c reproduction, `-race -count=10` | Passed; package time `17.956s`; the exact per-item loss reproduced every run. |
| Initial assertion expecting the first partial applied item to be `trashed` and item-bound | Failed as expected: states were `selected, selected`; this established R3c before the deterministic reproduction assertion was recorded. |
| `go test ./... -count=1 -timeout=300s` | Passed. |
| `go vet ./internal/trash` and root `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolved. |
| `./scripts/check-guardrails.sh --ci` under full offline environment | Passed, exit 0 in `295.86s`; final marker `guardrail checks passed (ci)`. Aggregate race, root storage race (`202.475s`), generation, staged generation, Vacuum, lint, architecture, module tests/vet/verification and Linux amd64/arm64 cross-builds passed. |

## Acceptance disposition

- A-25: accepted for W-05 scope. Default/custom retention, no-early-purge and
  startup/periodic janitor regressions pass.
- A-26: accepted for W-05 scope. Exact approval-bound early purge and direct
  hard-delete behavior retain passing evidence.
- A-27: blocked by R3c. Planned-state reconciliation is repaired, but a
  partial explicit retry loses its exact applied item from durable item state.
- A-29: accepted for W-05 scope. Whole-client stop and metadata-only removal
  remain separate and ordered around selected payload handling.
- A-30: accepted for W-05 scope. Restore preserves association evidence and
  performs no automatic re-add or resume.
- A-57: blocked by R3c. Blind retry is prevented, but the pending/applied
  per-item effect is not represented honestly after a partial explicit retry.

This receipt does not approve integration, release, deployment, live
filesystem mutation or live client control.
