# X-20 Arr write-adapter correction handoff, round 2

## Assignment and ownership

- Task: X-20 correction round 2, after independent receipt
  `c55d73bacb004a4a8238342a019e4b42b01151b4`.
- Implementer: `/root/x05_implementer`.
- Branch/worktree: shared `main`; coordinator owns execution state.
- Dispatch checkpoint: `6d71570728edba2c6d9d90b07faeb9ce6117b46c`.
- Correction-round-one product tree:
  `ba56aaf0f7b2aafc15923c2c92765f89f42a0007`.
- Owned product paths: `internal/adapters/arr/write/` and
  `tests/fixtures/arr/write/`.
- Owned handoff path: this file only.
- `docs/execution/state.json`, client modules, root scripts, API contracts,
  review receipts and unrelated lanes were not edited.

## Product result

Product commit: `8e4c722eae49051eabc4572f8ba7fb1176445f0f` (`fix(arr): reject
contradictory provider evidence`). The final product tree preserves the
round-one commits `923e31bbc22aac9782a9d72ca0d59cb8d857f1b8` and
`ba56aaf0f7b2aafc15923c2c92765f89f42a0007`.

### Provider matching is tri-state

`nativeProviderMatch` now returns one of `providerMatchFound`,
`providerNoMatch` or `providerMatchContradiction`. Registration examines that
state for every catalog title. A contradiction returns an explicit conflict
before capability evaluation, payload construction or any native write. The
same state is checked on registration read-back; a contradictory read-back is
also rejected rather than being reduced to an ordinary nonmatch.

For decimal identities, Radarr uses only its TMDB primary field and
case-insensitive `tmdbId` aliases; Sonarr uses only its TVDB primary field and
`tvdbId` aliases. Foreign namespaces cannot satisfy the selected manager.
Primary and same-namespace aliases must agree. IMDb primary and `imdbId`
aliases are validated for consistency and are used only for nonnumeric
identities. This preserves valid aliases while preventing a contradictory
primary/alias pair from being interpreted as either an already-satisfied
title or a new title that may be POSTed.

`TestCommandCapableRegistrationRejectsProviderContradictionsBeforePost`
exercises synthetic command-capable Radarr TMDB, Sonarr TVDB and Radarr IMDb
contradictions. Each returns a conflict and records zero POSTs even though the
synthetic capability gate permits writes. `TestNativeProviderIdentityAcceptsValidAliases`
covers valid same-namespace and IMDb aliases. The earlier public-constructor
foreign namespace regression remains in
`TestRadarrProviderIdentityKeepsNamespacesDistinct`.

Round-one protections remain in the final tree: request-local context error
attribution with preserved `errors.Is` identity, explicit metadata presence
and null preservation, contradictory Sonarr settings rejection, strict
standalone typed reads, exact import read-back, preview binding, and the G-01
public write block.

## Transport boundary and blocker

The explicit round-one R4 blocker remains open. Published Sonarr and Radarr
modules still expose the frozen read methods only; registration-only metadata,
history and registration/manual-import mutation methods have not been added
to a published client contract. Their root adapter bridge therefore remains a
temporary bounded path. No client module was changed and no local `replace` or
`go.work` was introduced.

The bridge uses private root DTOs, strict bounded decoding, request-scoped API
keys, refusal of redirects and sanitized status/context errors. Generated
standalone DTOs do not enter root ports, domain types or public adapter
methods. Existing migration and sanitization tests provide synthetic evidence
that credentials, raw response bodies, URLs and transport details do not leak.
G-01 remains open, so public native registration/import writes stay unknown
and fail closed.

## Verification

Commands were run from the final product tree. Successful checks exited 0;
the product commit's versioned pre-commit hook also passed.

| Command or scenario | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/adapters/arr/write -race -count=1` | Passed. |
| `GOWORK=off go test -mod=readonly ./tests/fixtures/arr/write -race -count=1` | Passed. |
| Focused tri-state provider/zero-POST/valid-alias regressions with `-race -count=20` | Passed. |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Passed. |
| `GOWORK=off go mod verify` | Passed; all modules verified. |
| `GOWORK=off go test -mod=readonly ./internal/adapters/arr/... -race -count=1` | Passed. |
| Root aggregate `GOWORK=off go test -mod=readonly ./... -count=1` | Passed on immediate rerun; one earlier concurrent invocation exposed an unrelated transient W-02 lease-renewal test failure, which was not reproduced. |
| Nested qBittorrent, NZBGet, Sonarr, Radarr, Jellyfin, Seerr, UI and tools tests/race/vet/mod verify | Passed. |
| `GOWORK=off ./scripts/check-lint.sh`; architecture and planning checkers | Passed: zero lint/import issues; 53 tasks, 60 acceptance cases and links valid. |
| `GOWORK=off ./scripts/check-guardrails.sh --fast` and staged pre-commit hook | Passed: generation, staged generation, API/Vacuum, architecture, formatting and targeted checks. |
| Linux amd64/arm64 CGO-free builds for root, Sonarr and Radarr | Passed for all six combinations. |
| `git diff --check` and owned-file `gofmt` | Passed. |

All fixtures are synthetic. No live Arr service, credential, private
coordinate, tracker data, media inventory, filesystem mutation or upstream
write was used.

## Checkpoint

- Final product SHA: `8e4c722eae49051eabc4572f8ba7fb1176445f0f`.
- Handoff commit: pending; this file is committed separately.
- Coordinator state update: pending; this lane does not edit state.
- R4 client-contract/release blocker remains open for a future separately
  reviewed standalone metadata/history/mutation contract.
- Next safe action: commit this handoff, record its exact SHA in coordinator
  state, and assign independent review against the final product tree.
