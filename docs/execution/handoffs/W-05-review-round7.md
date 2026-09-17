# W-05 independent review, round seven

## Decision

`approved`.

No P1 or P2 finding remains in the reviewed W-05 scope. R3f is closed. Repeated
unchanged reconciliation now retains one immutable base action/intent envelope
and one replaceable current observation. It does not recursively copy previous
observations. Exact item results, action error, action timestamp and hard-scope
evidence survive byte-for-byte while the current filesystem evidence remains
visible.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`, independent of product author
  `/root/x05_implementer`.
- Correction state parent:
  `5d4ab8279cf5349aaf9e998ee15128e2497966e0`.
- Exact product: `3f2d4e9e13540f21474a68f1d4d729ba232f3f50`,
  whose direct parent is the correction state parent.
- Product tree: `9c003481c509920952a214e9aca1c7106ee77642`.
- Exact handoff: `3ce4caefe4ce9f6f1b65c64bfb4f89b4cfc3f65e`,
  whose direct parent is the product.
- Handoff tree: `4a519eed97d047e29017f12c1dac751400dd7868`.
- Exact review checkpoint:
  `ac2b6e586c7f3780b69c9b1ae8b7549867aaa940`, whose direct parent is the
  handoff.
- Review-checkpoint tree:
  `1ad6050146867d960cfe0a03428a1c256f5fe39c`.
- Scoped correction diff SHA-256 from round-six product
  `73c55183faef67d20286b7656886b3e672cf8ef9`:
  `93024a0d0edb43f57421a03ef540366b74c0bea09051dfd5823a79534c9cc455`.
- Product-scope archive SHA-256:
  `b2e69cdacefbbac245110bc0bfe25b71ddcaa5ecc4969429049534bfaad50fd9`.
- Reviewed product paths: `internal/trash/` only. There is no product-path
  drift from the exact product to the review checkpoint.
- Acceptance reviewed: A-25, A-26, A-27, A-29, A-30 and A-57.

Review ran from a clean detached worktree at the exact checkpoint. Independent
probes used only synthetic manifests, temporary SQLite databases and fake
normalized ports. Their archived source SHA-256 is
`f6341f084a769abd463670b29ccacbf6f049b6c6a005fa8dd642313df1e5ea4e`.
No product, state, schema, API, adapter, client, worker, live service or real
media data was changed.

## R3f closure evidence

`persistTrashReconciliation` now calls `compactTrashPriorOutcome` before
encoding the new observation. The compactor follows a package-owned
`priorOutcome` chain to its immutable oldest envelope. The encoder retains that
envelope as raw JSON, while the new top-level `items`, `effects`, `evidence`
and `observedAt` replace the preceding read-only observation.

Independent probes established the boundary directly:

- Foreign, duplicate and changed-identity action reports were each persisted
  as hard unresolved scope, then reconciled 512 times from a fresh service.
  Every final `priorOutcome` was byte-for-byte equal to the action journal
  captured before the first read-back. The exact action result, item IDs,
  error, timestamp, evidence and scope reason remained present.
- All three cases stayed at prior count/depth `1/1`. Their final record sizes
  were stable at 3,776, 3,755 and 3,517 bytes respectively. Reconciliation
  issued zero filesystem actions.
- A synthetic 256-level journal produced by the former behavior was 23,417
  bytes. One fresh-process read-back compacted it to 1,099 bytes and retained
  the exact original intent envelope.
- Another 512 periodic `Tick` calls left that record at 1,099 bytes with prior
  count/depth `1/1`, current source-only evidence present and zero filesystem
  actions.
- The matrix passed 10 ordinary repetitions and five race repetitions. The
  race run exercised 7,680 scope observations plus five 512-Tick legacy-chain
  sequences without growth, data loss or race reports.

This closes the round-six P2. Historical round-five chains compact on their
next observation; newly written outcomes remain bounded to the immutable base
plus the current observation.

## Prior behavior and safety boundaries

- R3b planned source/trash/both/neither/identity/restart reconciliation remains
  read-only. Trash-only finalizes without an action; source-only requires an
  explicit retry.
- R3c partial retry persists exact item-bound outcomes, keeps unmatched scope
  pending and does not resubmit a terminal item after restart.
- R3d terminal source-only, both-present, neither-observable and changed
  identity states remain held/non-retryable and cannot contribute to aggregate
  completion.
- R3e action result, error, timestamp and exact foreign/duplicate/ambiguous
  evidence remain distinct from the latest observation. Current read-back
  replaces only the prior observation.
- R1a approval-bound early purge, R2a affected-set equality, R2b directory
  leaf accounting and R3a no-blind planned replay retain passing evidence.
- Retention/clock holds, client-stop prerequisite, partial payload scope,
  metadata-only removal and restore without re-add/resume remain behind the
  existing normalized ports and durable claims.
- F-05 filesystem trash/restore/permanent-delete capability remains
  unsupported and fail-closed. G-01 Arr writes remain disabled. X-14
  qBittorrent metadata removal remains separate and uses `deleteFiles=false`.
- No generated DTO, direct upstream transport, credential, private coordinate,
  live service, local `replace` or tracked `go.work` entered the reviewed scope.

## Independent checks

All Go and aggregate checks used `GOWORK=off GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Exact ancestry, trees, clean worktree, product-to-checkpoint no-drift, scoped hashes, `git diff --check`, no tracked `go.work`, no local `replace` | Passed. |
| Independent bounded action/scope and legacy-chain/Tick probes, `-count=10` | Passed; package time `19.276s`. |
| Same independent probes, `-race -count=5` | Passed; package time `319.451s`. |
| Focused R3b-R3f product regressions, `-count=20` | Passed; package time `9.982s`. |
| `go test ./internal/trash -count=10 -timeout=420s` | Passed; package time `24.807s`. |
| `go test -race ./internal/trash -count=3 -timeout=600s` | Passed; package time `182.625s`. |
| `go test ./... -count=1 -timeout=420s` | Passed; elapsed `20.68s`. |
| `go vet ./internal/trash` and root `go mod verify` | Passed; all modules verified. |
| `python3 scripts/check-architecture.py` | Passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases and local links resolved. |
| Full offline `./scripts/check-guardrails.sh --ci` | Passed, exit 0 in `295.92s`; final marker `guardrail checks passed (ci)`. Aggregate race, storage race (`202.667s`), trash race (`66.091s`), generation, staged generation, Vacuum, lint, architecture, root/UI/tools/client tests and vet/module verification, and Linux amd64/arm64 cross-builds passed. |

## Acceptance disposition

- A-25: accepted for W-05 scope. Default/custom retention, no-early-purge and
  startup/periodic janitor paths remain bounded and recoverable.
- A-26: accepted for W-05 scope. Approval-bound early purge, retention override
  and explicit exact hard-delete regressions pass.
- A-27: accepted for W-05 scope. One durable claim governs purge/restore;
  collision, changed identity and partial effects remain held and recoverable.
- A-29: accepted for W-05 scope. Whole-client stop and metadata-only removal
  remain separate and ordered around selected payload handling.
- A-30: accepted for W-05 scope. Restore preserves association evidence and
  performs no automatic re-add or resume.
- A-57: accepted for W-05 scope. Unresolved effects and clock anomalies hold;
  repeated observation remains bounded and never becomes a blind delete.

This receipt approves the reviewed W-05 product for integration. It does not
authorize release, deployment, live filesystem mutation or live client
control.
