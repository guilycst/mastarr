# X-06 Arr write-adapter correction round 2

## Assignment

- Task ID and title: X-06 correction round 2, close the remaining Sonarr
  read-back identity findings.
- Owner/agent: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `28ce5e069802403e4a5052e23e2920207f912f50`.
- Prior product under review: `8e811aa460d12eb374a623e87fb1d7531e015b3d`.
- Prior review receipt: `cf856714579f54c1cfe06fbc071d8ce89c8f6fe6`.
- Branch/worktree: shared `main`; coordinator owns
  `docs/execution/state.json`.
- Owned product paths: `internal/adapters/arr/write/` and
  `tests/fixtures/arr/write/`.
- Owned handoff path: this file.
- Acceptance contributions: A-12, A-13, A-16, A-17 and A-33.
- Product commit: `ca242c9fcab7abb63912402c62edae4ff56136c1`.

## Correction result

Sonarr observation now requires every nested `episodeFile` to carry a
positive `seriesId` that exactly matches the requested series. Missing, JSON
`null`, zero and foreign values are malformed/incomplete evidence and cannot
produce `already_satisfied`, even through the public G-01-blocked constructor.
The adapter fails before history is treated as sufficient and before any
mutation path is reachable. Sonarr files are represented with episode
associations only; the series identity is not fabricated as a `MovieID`.

The aggregation keeps independent indexes for native file ID, mapped content
path and episode ID. A single episode may not be claimed by distinct file IDs
or paths anywhere in one response. Such evidence returns an unknown result
with no effect and no write. Contradictory same-file identity and distinct
file IDs sharing one path remain rejected from round one. A valid season pack
where multiple episodes share the same complete file ID, path and size still
aggregates to one file with all episode IDs.

The existing round-one guarantees remain intact: exact preview revision and
selection binding before command dispatch, foreign/collision identity checks,
sanitized context errors preserving `errors.Is`, explicit monitoring opt-in,
read-before-write idempotency and the public G-01 fail-closed mutation gates.
No live Arr service, credential, private coordinate or real media inventory was
used; fixtures are synthetic only.

## Verification

All commands ran from the product tree ending at
`ca242c9fcab7abb63912402c62edae4ff56136c1`.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Focused Arr write and fixture packages |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0; three race repetitions | Arr write and fixture tests |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Focused packages |
| `TestSonarrReadbackRequiresNestedSeriesIdentity` | Exit 0; missing, `null`, zero and foreign nested `seriesId` cases all returned unknown with zero writes | `internal/adapters/arr/write/client_test.go` |
| `TestSonarrReadbackRejectsEpisodeClaimingMultipleFiles` | Exit 0; one episode on two file IDs/paths returned unknown with zero writes | `internal/adapters/arr/write/client_test.go` |
| `TestSonarrMultiEpisodeFileReadbackRemainsOneAssociation` | Exit 0; valid two-episode shared file remained one association and no fabricated `MovieID` | `internal/adapters/arr/write/client_test.go` |
| `GOWORK=off go test -mod=readonly -timeout=180s ./...` | Exit 0; full root suite | Root module |
| `GOWORK=off go vet -mod=readonly ./... && GOWORK=off go mod verify` | Exit 0; all modules verified | Root module |
| `GOWORK=off ./scripts/check-lint.sh && python3 scripts/check-architecture.py && python3 scripts/check_planning.py` | Exit 0; all lint/import boundaries and architecture checks passed; planning reported 53 tasks, 60 acceptance cases and resolved local links | Root checkers |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation, staged generation, API/Vacuum, architecture, formatting and targeted tests passed; Vacuum reported 100/100 with zero warnings/errors | Fast guardrail runner |
| `clients/{qbittorrent,nzbget,sonarr,radarr,jellyfin,seerr}`, `tools` and `ui`: `GOWORK=off go test -mod=readonly ./...`, vet and mod verify | Exit 0 for every nested module | Nested module matrix |
| Same eight nested modules: `GOWORK=off go test -mod=readonly -race -count=1 ./...` | Exit 0 for every nested module | Nested race matrix |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux amd64 root build |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux arm64 root build |
| Product pre-commit hook during `ca242c9` | Exit 0; generation, staged generation, staged-byte Vacuum and fast guardrails passed | `.githooks/pre-commit` |
| `gofmt -w internal/adapters/arr/write/*.go tests/fixtures/arr/write/*.go` and `git diff --check` | Exit 0; owned files formatted with no whitespace errors | Owned product paths |

The full `./scripts/check-guardrails.sh --ci` aggregate was not rerun in this
correction lane; C-06 owns the repository-wide CI matrix. No native Arr write
capability is enabled. G-01 remains open until versioned no-overwrite and
coordination evidence is independently accepted.

## Review and integration

- Findings addressed: R2a P1 missing/null/zero/foreign nested Sonarr
  `episodeFile.seriesId` could yield public `already_satisfied`; R2b P1 one
  episode could be associated with distinct file IDs and paths.
- Regression evidence: the four nested-series identity variants, the
  multi-file episode ambiguity case, and the valid multi-episode season-pack
  control pass under repeated race execution.
- Final independent review: pending against product
  `ca242c9fcab7abb63912402c62edae4ff56136c1`.
- Coordinator state update: pending; this lane did not edit `state.json`.

## Resume checkpoint

- Current state: correction product is committed and all requested local
  checks pass; public writes remain read-before-write and fail-closed.
- Next safe action: commit this handoff separately, then run independent
  round-three review against the exact product tree.
- Outstanding blocker: G-01 native Arr no-overwrite evidence remains open.
- No conflicting files or unrelated work were removed.
