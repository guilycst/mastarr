# X-06 Arr write-adapter correction round 3

## Assignment

- Task ID and title: X-06 correction round 3, close the remaining duplicate
  Sonarr episode identity finding.
- Owner/agent: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `f7eefef1abae951cb25c1883e84e6249b8b20e5f`.
- Prior product under review: `ca242c9fcab7abb63912402c62edae4ff56136c1`.
- Prior review receipt: `2d366197a16517eab8abc8dcd975a167d0f8307e`.
- Branch/worktree: shared `main`; coordinator owns
  `docs/execution/state.json`.
- Owned product paths: `internal/adapters/arr/write/` and
  `tests/fixtures/arr/write/`.
- Owned handoff path: this file.
- Acceptance contributions: A-12, A-13, A-16, A-17 and A-33.
- Product commit: `0bbf8405859ac604d9d05237badef45dd0ee7fbc`.

## Correction result

Sonarr episode IDs are now registered as soon as each episode row passes the
requested-series identity check, before inspecting whether its
`episodeFile` is nil. A repeated episode ID is malformed regardless of row
order or file state: fileless then complete, complete then fileless, repeated
same-file rows and repeated distinct-file rows all fail closed. The adapter
therefore cannot treat a fileless row as absence and then accept a later file
row for the same episode as complete evidence or `already_satisfied`.

The correction preserves the prior strict gates. Nested episode-file
`seriesId` must be present, positive and equal to the requested series;
distinct file IDs cannot claim one mapped path; one episode cannot claim
different files; valid multiple episodes sharing one complete file remain one
association; and Sonarr `MediaFile.MovieID` remains empty rather than using a
series ID as a movie ID. Preview revisions and selected files remain bound
before any synthetic command, context errors remain sanitized with
`errors.Is` identity, explicit monitoring is preserved, and public Arr writes
remain blocked by G-01.

No live Arr service, credential, private coordinate or real media inventory was
used. The regression fixtures are synthetic `httptest` responses only.

## Verification

All commands ran from the product tree ending at
`0bbf8405859ac604d9d05237badef45dd0ee7fbc`.

| Command or scenario | Result / exit status | Evidence |
| --- | --- | --- |
| `GOWORK=off go test -mod=readonly -count=1 ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Focused Arr write and fixture packages |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run 'TestSonarrReadbackRejectsDuplicateEpisodeAcrossFilelessRows\|TestSonarrReadbackRequiresNestedSeriesIdentity\|TestSonarrReadbackRejectsEpisodeClaimingMultipleFiles\|TestSonarrMultiEpisodeFileReadbackRemainsOneAssociation' ./internal/adapters/arr/write` | Exit 0; three race repetitions | `client_test.go` duplicate row-order, identity and positive-control tests |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Exit 0 | Focused packages |
| `GOWORK=off go test -mod=readonly -timeout=180s ./...` | Exit 0; full root suite | Root module |
| `GOWORK=off go vet -mod=readonly ./... && GOWORK=off go mod verify` | Exit 0; all modules verified | Root module |
| `GOWORK=off ./scripts/check-lint.sh && python3 scripts/check-architecture.py && python3 scripts/check_planning.py` | Exit 0; lint/import boundaries and architecture passed; planning reported 53 tasks, 60 acceptance cases and resolved local links | Root checkers |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation, staged generation, API/Vacuum, architecture, formatting and targeted tests passed; Vacuum reported 100/100 with zero warnings/errors | Fast guardrail runner |
| `clients/{qbittorrent,nzbget,sonarr,radarr,jellyfin,seerr}`, `tools` and `ui`: `GOWORK=off go test -mod=readonly ./...`, vet and mod verify | Exit 0 for every nested module | Nested module matrix |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux amd64 root build |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 | Linux arm64 root build |
| Product pre-commit hook during `0bbf840` | Exit 0; generation, staged generation, staged-byte Vacuum and fast guardrails passed | `.githooks/pre-commit` |
| `gofmt -w internal/adapters/arr/write/*.go tests/fixtures/arr/write/*.go` and `git diff --check` | Exit 0; owned files formatted with no whitespace errors | Owned product paths |

The full `./scripts/check-guardrails.sh --ci` aggregate was not rerun in this
correction lane; C-06 owns the repository-wide CI matrix. No native Arr write
capability is enabled. G-01 remains open until versioned no-overwrite and
coordination evidence is independently accepted.

## Review and integration

- Finding addressed: duplicate Sonarr episode 301 could appear once without a
  file and once with a complete file, in either row order, and still produce
  public `already_satisfied`.
- Regression evidence: both fileless/complete row orders fail with unknown,
  zero effect and zero writes under three race repetitions; the valid shared
  season-pack control continues to pass.
- Final independent review: pending against product
  `0bbf8405859ac604d9d05237badef45dd0ee7fbc`.
- Coordinator state update: pending; this lane did not edit `state.json`.

## Resume checkpoint

- Current state: correction product is committed and all requested local checks
  pass; public writes remain read-before-write and fail-closed.
- Next safe action: commit this handoff separately, then run independent
  round-four review against the exact product tree.
- Outstanding blocker: G-01 native Arr no-overwrite evidence remains open.
- No conflicting files or unrelated work were removed.
