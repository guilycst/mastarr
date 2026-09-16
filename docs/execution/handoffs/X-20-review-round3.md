# X-20 independent review, round 3

## Decision and exact scope

Decision: **approved** for the corrected, explicitly partial X-20 product. No
P1/P2 finding remains in the reviewed scope. The separately recorded R4
standalone-client contract/release blocker remains open; this approval does not
claim full Arr transport migration, resolve G-01 or authorize native writes.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `8e4c722eae49051eabc4572f8ba7fb1176445f0f`.
- Product tree: `521874d566f33be4e956d4a69a66978af2e1500f`.
- Handoff: `ac4370b4a6b434010a7fda67ffe834115c9c567e`.
- Handoff tree: `e1eb4f64ecd287f12276100c198ccc81d5814ee4`.
- Prior receipt: `c55d73bacb004a4a8238342a019e4b42b01151b4`.
- Scope: Arr write adapter/fixtures, public Sonarr/Radarr boundary,
  A-12/A-13/A-16/A-17/A-33 and the explicit partial-transport blocker.

Review ran in an isolated detached checkout at the exact handoff. Independent
probes ran in a separate archive of the exact product. Product, execution state,
shared main and live services were untouched. This commit contains only this
receipt; coordinator records its exact SHA separately.

## Provider finding closure

Provider evaluation is now tri-state: found, no match or contradiction.
Register checks contradiction for every catalog title before capability
assessment or payload construction, returning conflict immediately. It applies
the same check to post-write read-back before accepting an effect.

The exact prior independent reproduction now passes: Radarr title 101 with
TMDB primary 9999 and tmdbId alias 4242, registration request 4242 and a
synthetic supported capability returns conflict with zero POST/PUT and no
effect on all three race repetitions. The correction therefore closes the
unsafe conversion of contradiction to an ordinary nonmatch.

The implementation uses one authoritative numeric namespace per manager:
Radarr TMDB and Sonarr TVDB. Primary and case-insensitive same-namespace aliases
must agree. Foreign TMDB/TVDB/TVMaze values remain nonmatching evidence and
cannot satisfy the other manager. Nondecimal IMDb primary/aliases are checked
case-insensitively and also require consistency. Committed command-capable
regressions cover contradictory Radarr TMDB, Sonarr TVDB and IMDb with zero
POSTs; valid same-namespace and IMDb alias-only controls match. Public foreign
namespace registration reaches G-01 unsupported with zero effect/write.
Any contradictory authoritative evidence in the catalog conservatively blocks
the registration, which is the requested fail-closed behavior.

## Retained safety and bounded partial scope

Request-local context attribution remains correct. Each standalone read gets a
private context bucket; sequential bridge cancellation followed by typed 401
produces canceled then unauthorized, while concurrent canceled/deadline calls
retain their own errors.Is identity. Returned errors contain no endpoint,
credential, response body or transport detail. No global raw-error FIFO exists.
Independent sequential and committed concurrent probes pass under race.

Presence-aware metadata merge preserves typed Sonarr SeriesType and
SeasonFolder for omission or null, rejects present contradictions before
mutation, and retains unselected anime/season-folder settings in a
monitoring-only payload. Monitoring, root/profile and seasons follow non-null
presence handling. Independent prior omission/payload and committed
omission/null/change tests pass.

Strict client catalog/title/episode/file identity, complete coverage/no cursor,
provider maps, exact preview selection/rejection binding, read-before-write
idempotency, ambiguity/collision rejection, per-file read-back, lost-response
reconciliation and explicit monitoring/no-search controls remain intact.
Adapted X-06 regressions and focused root race suites pass. Public New keeps
registration/import capabilities unknown; changed state remains unsupported
with zero native writes while G-01 is open. Command-capable calls exist only in
package-local synthetic httptest coverage.

R4 remains an honest blocker. Published clients currently own typed catalog,
title and episode/file reads. Root still owns a temporary bounded private
metadata/history/mutation path because published contracts lack those methods.
The handoff labels this partial, records the required future client
contract/release, and makes no full-migration claim. The bridge uses private
models, strict bounded decoding, redirect refusal and sanitized errors; no
generated DTO crosses into domain/ports/public API. There is no replace or
go.work. This review approves integration of that declared partial state, not
completion of the blocker or native compatibility.

Published read dependencies stay at
`v0.0.0-20260915230044-873e53ef6729`, origin
`873e53ef67292549bcd3e42f8246d10c81496132`, and verify without local module
substitution. No live service, credential, private inventory or real media was
used.

## Independent checks

| Reviewer command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.595s, fixtures 1.669s, read 34.366s. |
| Exact archive prior reviewer probes, race count 3 | Exit 0, 1.395s; contradiction zero-write, foreign namespace, sequential context attribution and metadata preservation all pass. |
| Committed provider tri-state tables in the focused race suite | Exit 0; Radarr TMDB, Sonarr TVDB and IMDb contradiction zero-POST cases plus valid aliases. |
| Full root tests, root vet and root mod verify | Exit 0; storage 12.269s; all modules verified. |
| All eight nested modules: offline GOWORK=off tests/race, vet/mod verify | Exit 0 for ui, tools and all six clients. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate generation, root and staged standalone API/Vacuum, formatting, architecture/isolation and targeted tests. |
| Lint/architecture and planning | Exit 0; nine zero-issue module results/import boundaries; 53 tasks, 60 acceptance cases, links resolve. |
| Linux amd64/arm64 CGO-free readonly builds in root, clients/sonarr and clients/radarr | Exit 0 for all six module/architecture pairs. |

The receipt commit runs the versioned pre-commit hook without bypass. Full --ci
remains C-06-owned and was not rerun. Remote CI, live compatibility, G-01 and
full transport migration are separate gates. Coordinator may integrate this
reviewed partial product while preserving the recorded R4 blocker.
