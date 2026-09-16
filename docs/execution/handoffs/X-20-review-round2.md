# X-20 independent review, round 2

## Decision and exact scope

Decision: **changes_requested**. R2 and R3 are closed, and R4 is now honestly
bounded as an open client-contract/release blocker. The foreign-namespace part
of R1 is closed, but contradictory same-namespace provider evidence still
fails open in the command-capable protocol. One P1 remains.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Final product: `ba56aaf0f7b2aafc15923c2c92765f89f42a0007`.
- Product tree: `9f946ef966f8b48153a7586646cf72d639200333`.
- First correction product: `923e31bbc22aac9782a9d72ca0d59cb8d857f1b8`.
- Correction handoff: `afabf91e3cedca9e4f594578a209de3116869493`.
- Handoff tree: `d6a6e5da4072979e4ab920a8b6a4345fb27cbc87`.
- Prior receipt: `ad0302c1c309cb8cac4a714a415e15c70a99fcbc`.
- Scope: root Arr write adapter/fixtures, published Sonarr/Radarr boundary,
  A-12/A-13/A-16/A-17/A-33 and the explicit partial-transport blocker.

Review ran in an isolated detached checkout at the exact handoff. Independent
probes ran in a separate archive of the exact final product. Product, execution
state, shared main and live services were untouched. This commit contains only
this receipt; coordinator records its exact SHA separately.

## R1a — P1: contradictory authoritative provider evidence becomes “not found”

Locations: `internal/adapters/arr/write/client.go:1343-1356`, provider helpers
at `client.go:1371-1409`, and Register's catalog matching loop.

The new helpers detect primary/alias contradiction and return
`consistent=false`, but nativeHasProviderID reduces match, presence and
consistency to one bool. Register therefore cannot distinguish contradictory
evidence from an ordinary nonmatching title. With a supported synthetic
capability it proceeds to create a second title for the requested provider.
This is not the handoff's claimed fail-closed behavior.

Deterministic exact-product reproduction:

1. Use the command-capable package-local synthetic constructor for Radarr.
2. GET `/api/v3/movie` twice returns one complete movie: ID 101, TMDB primary
   9999, providerIds `{"tmdbId":"4242"}`, complete path/root/profile and
   monitored false.
3. Register movie provider 4242 with root `/synthetic/library`, profile 7.
4. Observe POST `/api/v3/movie` and return synthetic ID 102.
5. Actual: one duplicate registration POST. Later read-back returns unsupported
   only because the fixture has no movie 102 route; that post-write error does
   not undo the unsafe dispatch.

`TestReviewerContradictoryProviderEvidenceMustNeverWrite` reproduces one write
on all three race-enabled repetitions. There is no race report. Public New
still blocks the same changed state under G-01, but the reviewed command-capable
protocol is the evidence intended for later enablement and must reject before
any write.

Required correction: preserve a tri-state provider evaluation: match,
nonmatch, and malformed/contradictory. Register must return unknown/conflict on
any relevant title whose authoritative primary and same-namespace aliases
disagree, before the capability check or payload construction. Apply this to
Radarr TMDB, Sonarr TVDB and shared IMDb evidence. Test contradictory evidence
through Register with a supported synthetic capability and assert zero writes;
retain foreign namespace nonmatch and valid primary/alias-only positive cases.

## Closed findings and retained blocker

R1 foreign namespace isolation is correct: numeric Radarr registration uses
TMDB only and numeric Sonarr registration uses TVDB only. Foreign TVDB/TMDB or
TVMaze values cannot match the other manager. Nondecimal IMDb matching is
separate. The old public foreign-namespace probe now reaches G-01 unsupported
with no effect and zero writes. Same-namespace alias-only positive controls
pass. The remaining issue is the lost contradiction state described above.

R2 request-local attribution is closed. Each typed call creates a private bucket
carried by its request context; the transport/body wrapper records only there,
and translation consumes only that call's bucket. Root bridge requests do not
create or retain a typed bucket. The prior sequential bridge cancellation then
typed 401 probe now reports the first canonical cancellation and the second
unauthorized, without cross-request attribution. Producer concurrent
cancel/deadline probes and independent sequential probes pass three race
repetitions without URL/body/transport detail or race reports. The old shared
unbounded FIFO no longer exists.

R3 presence-aware merge is closed for the corrected scope. Omitted and null
seriesType/seasonFolder preserve normalized typed anime/true settings. Present
contradictory values return unknown before mutating the destination object.
The prior monitoring-only payload probe retains both unselected settings.
Root/profile/monitoring/seasons use the same non-null-presence rule; monitoring
and shared typed settings are checked before merge writes. Independent old R3
and new omitted/null/changed controls pass.

R4 is accurately bounded, not closed as a transport migration. The handoff
states that root still owns metadata/history/mutation URLs, API-key transport,
bounded decoding and status normalization because published client contracts
do not expose those operations. It records the future standalone
client-contract/release blocker, does not claim complete ownership, does not
add a local replace/go.work, and keeps G-01 capabilities unknown. The private
metadata/history structs do not leak generated DTOs into domain/ports/public
API. Strict decoding rejects invalid UTF-8, duplicate keys, trailing input and
bounds; returned root bridge errors omit credential, body, URL and transport
text. This review permits integrating that explicitly partial state after R1a;
it does not approve full X-20 transport completion or native writes.

Prior X-06 controls were adapted to current required title/episode shape and
API keys. Preview mismatch, collision/foreign file identity, nested-series
variants, episode ambiguity, complete pack with empty MovieID, wrapped context
errors, monitored opt-in/defaults, read-before-write, lost-response read-back
and G-01 zero-write behavior pass. Trusted PreviewResolver production
authority, native placement/provenance, registration lost-response behavior,
G-01, the R4 contract extension and live compatibility remain explicit gates.
No live service or private inventory was used.

Published read dependencies remain exact public pseudo-versions
`v0.0.0-20260915230044-873e53ef6729`, with origin
`873e53ef67292549bcd3e42f8246d10c81496132`. Module verification succeeds.
No generated client type reaches root domain/ports/API, and architecture checks
find no prohibited root/other-client dependency.

## Independent checks

| Reviewer command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.861s, fixtures 1.309s, read 33.684s. |
| Full root tests, root vet and root mod verify | Exit 0; storage 12.269s; all modules verified. |
| All eight nested modules: offline GOWORK=off tests/race, vet/mod verify | Exit 0 for ui, tools and all six clients. |
| Exact archive: race count 3 `-run TestReviewerContradictoryProviderEvidenceMustNeverWrite` | Exit 1; one duplicate POST in each repetition; package 0.470s; no race report. |
| Exact archive: prior R1/R2/R3 independent probes, race count 3 | Exit 0; sequential attribution, foreign namespace and metadata preservation, 3.800s. |
| Committed concurrent attribution, provider namespace, metadata presence/contradiction and X-06 regressions in focused/root suites | Exit 0, including race execution above. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate generation, root and staged standalone API/Vacuum, formatting, architecture/isolation and targeted tests. |
| Lint/architecture and planning | Exit 0; nine zero-issue module results/import boundaries; 53 tasks, 60 acceptance cases, links resolve. |
| Linux amd64/arm64 CGO-free readonly builds in root, clients/sonarr and clients/radarr | Exit 0 for all six module/architecture pairs. |

The receipt commit runs the versioned pre-commit hook without bypass. Full --ci
remains C-06-owned and was not rerun; remote CI, live compatibility, G-01 and
full transport migration are not approved. Correct R1a and independently
re-review before integrating this partial X-20 product.
