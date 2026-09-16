# X-20 independent review, round 1

## Decision and exact scope

Decision: **changes_requested**. One P1 and three P2 findings remain open.
The typed read migration is present and existing safety regressions pass,
but registration identity, error attribution, settings preservation and
transport ownership do not satisfy the lane's exit gate.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `c20315353bab7e0545204d21a5fd9e1362964b49`.
- Product tree: `913e5556668712c66cd2b0c35f63d69fc0f6a69b`.
- Handoff: `c111c546429f83d4322e08e7edd495f8de1b29c4`.
- Handoff tree: `d16137356e650a625e15d23580ebcc345e2619b4`.
- Paths: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`, root
  ports/dependency manifests and standalone Sonarr/Radarr read boundary.
- Acceptance contributions: A-12/A-13/A-16/A-17/A-33 and X-20 transport scope.

Review used an isolated detached checkout at the handoff. Independent synthetic
probes ran in a separate archive of the exact product. Shared main, product,
execution state and live services were untouched. This commit contains only
this receipt; coordinator records its exact SHA separately.

## R1 — P1: foreign provider namespace authorizes wrong registration identity

Location: `internal/adapters/arr/write/client.go:1355-1363`.
The new provider-map loop accepts numeric IDs from all recognized namespaces,
regardless of manager kind or the title's primary ID. Equal numeric strings
from different providers do not identify the same title.

Public-constructor reproduction: Radarr GET movie catalog, for both typed and
metadata reads, returns a complete title 101, title Synthetic, path
`/synthetic/library/Synthetic`, monitored false, tmdbId 9999,
providerIds `{"tvdbId":"4242"}`, rootFolderPath `/synthetic/library` and
qualityProfileId 7. Register movie ProviderID 4242 with no field changes.
Result: nil error, ExternalID 101, Record.ProviderID 4242 and
already_satisfied, despite the movie's TMDB identity being 9999. Zero writes.
`TestReviewerForeignProviderNamespaceCannotSatisfyRegistration` reproduces
this on all three race repetitions through public New.

Required correction: retain provider namespace when matching; for this bare-ID
registration contract, numeric movie matching must use authoritative TMDB and
series matching authoritative TVDB, with explicitly supported same-namespace
aliases only. Foreign TVDB/TMDB/TVMaze values must not satisfy another
namespace. Reject contradictory primary/alias evidence rather than selecting
an arbitrary equal string. Preserve other IDs for cross-service correlation,
as connectors.md requires. Cover both manager kinds, foreign numeric collisions,
conflicting aliases and valid primary/same-namespace aliases.

## R2 — P2: shared context-error queue contaminates unrelated operations

Locations: `native.go:28-67`, `native.go:364-374`, and root `request`.
The tracker stores raw errors in a client-wide FIFO without request identity.
Private bridge failures are recorded, but root request handles them directly
without draining the queue. A later typed error consumes that stale cause.
Concurrent requests can likewise consume another request's cause. The queue
also retains unbounded raw wrappers when terminal paths do not consume them.

Deterministic public New reproduction with an active background context and a
synthetic RoundTripper:

1. First Register's typed catalog read succeeds with complete movie 101, TMDB
   4242, path/title and monitored false.
2. Its second, metadata read returns `fmt.Errorf("synthetic private bridge
   detail: %w", context.Canceled)`. First Register correctly reports cancellation.
3. A new Register call using the same client receives HTTP 401 on its typed
   catalog read, with no context cancellation.
4. Actual result is `Arr request canceled: context canceled`, errors.Is canceled
   true, rather than a root unauthorized error retaining status 401.

`TestReviewerContextCauseDoesNotCrossRequests` fails its attribution assertion
on all three race repetitions. No mutation is involved. This can incorrectly
cancel an unrelated durable action and hides its actual authentication failure.

Required correction: preserve canonical context cause within each client
request/error boundary, not via a global FIFO. Avoid retaining raw URL/body/
transport wrappers; cover sequential bridge/typed failures, success/error
terminal paths and concurrent requests with different causes. Prefer fixing
the standalone modules' typed error/context contract and consuming a published
version, keeping transport/error ownership in those modules.

## R3 — P2: metadata projection erases known unselected Sonarr settings

Location: `native.go:252-253`.
SeriesType and SeasonFolder already exist in the normalized public Sonarr
Series model and are copied into nativeTitle. The metadata bridge nevertheless
unconditionally replaces them with empty/nil when its second projection omits
those fields. This loses known settings before an unrelated registration patch.
It also accepts conflicting present values instead of fencing the two snapshots.

Independent probe `TestReviewerMetadataDoesNotEraseTypedSonarrSettings` starts
with title 201, SeriesType anime, SeasonFolder true and monitored false. Merge
metadata with the same ID/monitoring, valid root/profile, but no seriesType or
seasonFolder. Build a monitoring-only existing-title payload with true.
Actual payload has SeriesType empty and SeasonFolder nil, dropping both
unselected typed settings. All three race repetitions fail the preservation
assertion. Native writes remain gated; this is a synthetic payload defect,
not a claim of an actual upstream setting change.

Required correction: merge only fields absent from the normalized public model,
or validate/preserve all shared fields with explicit presence and snapshot
consistency. Missing/null metadata must not erase known values. Reject
contradictory shared evidence; add omitted/null/changed projections and a
monitoring-only patch positive control preserving anime/season-folder settings.

## R4 — P2: root private bridge leaves required transport migration incomplete

Locations: `native.go:203-235`; `client.go:1014-1065`; history and
mutateJSON/postJSON/putJSON paths. The root constructs upstream URLs, adds the
API key itself and owns deadlines, response-body decoding and status/error
normalization for metadata/history and synthetic mutation transport.
Authenticating a previous typed read does not transfer ownership of a later
HTTP request. A DTO being private does not change this boundary.

X-20 in implementation.md explicitly moves registration/manual-import transport
behind the standalone modules. connectors.md:27-50 assigns authentication,
deadlines, bounded decoding, response normalization and typed errors to each
client module. The handoff instead declares a root private transport because
the frozen published models/contracts lack fields/history/write methods. That
is an unresolved contract limitation, not completion of the required migration.

Required correction: extend the independently reviewed per-service client
contracts/models/transport for needed registration metadata/history and, when
the explicit control lane reaches them, write operations; consume published
versions and keep root-only policy/translation. If unavailable, retain the
legacy adapter as an explicitly bounded temporary path and record the client
contract/release blocker and partial X-20 scope. Do not claim module transport
ownership or completed transport migration while this bypass remains. G-01
must stay open regardless of this correction.

## Checks and verified boundaries

Typed reads use the published Sonarr ListSeries/GetSeries/ListEpisodesWithFiles
and Radarr ListMovies/GetMovie clients. Normalized public types are translated
into private root models; no generated package reaches domain/ports/public API.
Page completeness and no continuation are checked before accepting array reads.
Metadata reads enforce byte/record bounds, strict UTF-8/duplicate-key decoding,
exact ID sets and noncontradictory present monitoring. Those checks do not close
the findings above or prove the handoff's broader snapshot consistency claim.

Published Sonarr/Radarr dependency resolution is exact:
`v0.0.0-20260915230044-873e53ef6729`, origin
`873e53ef67292549bcd3e42f8246d10c81496132`, expected module subdirectories,
no replace/go.work. go list/mod download metadata and module verification
succeed; no bootstrap blocker for these existing read versions was observed.
The missing transport/model scope remains a separate contract blocker.

Prior independent X-06 probes were adapted with required native episode
numbering/hasFile, title path/monitoring and synthetic API keys. They all pass
three race repetitions: trusted preview disagreement, strict foreign/collision
identity, missing/null/zero/foreign nested series, distinct-file duplicate,
both fileless row orders, sanitized wrapped canceled/deadline transport/body
errors, monitoring defaults/opt-in and complete public season pack with empty
MovieID and zero writes. Public changed-state native writes remain blocked.
Source-path fixtures still do not prove native mapped placement/provenance;
production trusted resolver authority, G-01 and live compatibility remain gates.
Initial reviewer probes omitted the newly required API key and correctly failed
setup; the findings above were reproduced only after supplying synthetic keys.

| Reviewer command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.897s, fixtures 1.346s, read 32.759s. |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=180s ./...`; root vet/mod verify | Exit 0; full root suite, storage 11.757s; all modules verified. |
| All eight nested modules: offline GOWORK=off tests and race with count 1/timeout 120s, vet/mod verify | Exit 0 for ui/tools and all six clients. |
| Exact archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run 'TestReviewer(ContextCause\|ForeignProvider\|MetadataDoes)' -v ./internal/adapters/arr/write` | Exit 1; R1/R2/R3 assertions fail all three repetitions, package 0.460s; no race report. |
| Archive adapted X-06 controls: race count 3 with `-run 'TestReviewer(Forged\|Conflicting\|Wrapped\|Explicit\|Sonarr\|Body\|Public)'` | Exit 0; 1.533s. |
| Offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate checks, root/staged standalone API/Vacuum, isolation, architecture, formatting and targeted tests. |
| `GOWORK=off ./scripts/check-lint.sh`; planning checker | Exit 0; nine zero-issue module results/import boundaries; 53 tasks, 60 acceptance cases, links resolve. |
| Linux amd64/arm64, CGO_ENABLED=0 GOWORK=off readonly go build in root, clients/sonarr and clients/radarr | Exit 0 for all six module/architecture pairs. |

Receipt commit runs the versioned hook without bypass. Full --ci remains
C-06-owned and was not rerun; remote CI/native compatibility are not approved.
No live upstream or private inventory was used. Keep X-20 changes_requested
until corrections/blockers are explicit and independently re-reviewed.
