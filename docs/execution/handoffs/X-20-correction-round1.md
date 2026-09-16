# X-20 Arr write-adapter correction handoff, round 1

## Assignment and ownership

- Task: X-20 correction round 1, after independent receipt
  `ad0302c1c309cb8cac4a714a415e15c70a99fcbc`.
- Implementer: `/root/x05_implementer`.
- Branch/worktree: shared `main`; coordinator owns execution state.
- Dispatch checkpoint: `578f7c2457c1f536a034b025db8c39a09c677465`.
- Review source product: `c20315353bab7e0545204d21a5fd9e1362964b49`.
- Owned product paths: `internal/adapters/arr/write/` and
  `tests/fixtures/arr/write/`.
- Owned handoff path: this file only.
- `docs/execution/state.json`, client modules, root scripts, API contracts,
  review receipts and unrelated lanes were not edited by this correction.

## Product result

The correction product is committed in two atomic commits. The final product
tree is `ba56aaf0f7b2aafc15923c2c92765f89f42a0007`:

- `923e31b` (`fix(arr): fence provider and read error identity`) scopes bare
  numeric provider matching to the selected manager's authoritative namespace
  and moves context-error attribution to a request-local context marker and
  bucket.
- `ba56aaf` (`fix(arr): preserve metadata evidence presence`) preserves the
  same namespace/attribution behavior and adds explicit metadata field
  presence, null handling and contradiction checks.

### R1 — provider namespace collision

`nativeHasProviderID` now treats a decimal registration identity as a
manager-specific identity: Radarr uses its TMDB primary ID and `tmdbId`
aliases; Sonarr uses its TVDB primary ID and `tvdbId` aliases. Foreign
`tvdbId`, `tmdbId` or `tvmazeId` values cannot satisfy a request for the other
manager. An IMDb identity is considered only for a nonnumeric provider
request. Primary and same-namespace alias values must agree; contradictory
values fail closed rather than selecting one arbitrarily. Same-namespace
aliases remain accepted when the primary field is omitted.

`TestRadarrProviderIdentityKeepsNamespacesDistinct` exercises the public
constructor with a Radarr movie whose TMDB ID is 9999 and foreign TVDB ID is
4242, then verifies that a request for 4242 remains G-01 blocked with zero
native writes. `TestNativeProviderIdentityRejectsContradictorySameNamespaceEvidence`
covers contradictory Radarr/Sonarr primary aliases, valid same-namespace
aliases and a foreign Sonarr namespace.

### R2 — context attribution is request-local

The shared transport wrapper no longer stores a client-wide error FIFO. Each
typed standalone read receives a context marker carrying its own bounded
context-error bucket. Transport and response-body failures are recorded only
in that bucket; the adapter consumes only the bucket belonging to the failed
call. Root-owned requests do not create a bucket and continue to sanitize
their own context errors directly. The returned error preserves
`errors.Is(context.Canceled)` or `errors.Is(context.DeadlineExceeded)` without
including endpoint, credential, body or transport text, and buckets do not
retain causes after the operation returns.

`TestArrWriteContextErrorsAreRequestLocal` runs concurrent typed reads with
different wrapped cancellation causes and verifies each caller receives its
own context identity and no private detail. Existing transport/body and
caller-deadline tests continue to cover sequential and cancellation paths.

### R3 — metadata projection preserves known settings

`nativeRegistrationMetadata` records non-null JSON member presence while
decoding the private, bounded metadata projection. `mergeRegistrationMetadata`
updates a field only when that projection supplies a non-null value. Omitted
or null `seriesType`, `seasonFolder`, root, profile, monitoring and season
members therefore cannot erase values already validated by the typed
standalone projection. Non-null shared Sonarr settings that contradict the
typed projection are rejected before the destination title is changed;
monitoring contradictions retain the existing rejection.

`TestSonarrMetadataProjectionPreservesTypedFieldsWhenOmitted` verifies that
typed `SeriesType=anime` and `SeasonFolder=true` survive the second projection.
`TestSonarrMetadataProjectionNullPreservesAndChangeRejects` verifies null
preservation, contradictory setting rejection and no partial mutation on a
failed merge. The existing registration payload tests cover a monitoring-only
patch retaining unselected fields.

### R4 — explicit partial transport boundary and blocker

This correction does not claim that X-20 owns every Arr HTTP operation. The
published Sonarr and Radarr modules at
`v0.0.0-20260915230044-873e53ef6729` expose the frozen read methods used by the
adapter, but do not expose registration-only metadata, history, or the
registration/manual-import mutation methods. Consequently the root adapter's
private bridge still constructs the metadata/history/mutation requests and
owns their bounded body reads and status normalization. This is an explicit
client-contract/release blocker for a future scoped lane; no client module was
edited and no local `replace` or `go.work` was introduced.

The partial bridge remains bounded and fail closed. Its DTO is private to the
adapter, generated standalone DTOs do not enter root ports/domain/public
methods, API keys are sent only on the request, and sanitized status/context
errors never include URL, credential, body or transport detail. The existing
`TestArrWriteUsesStandaloneReadContractAndSanitizedErrorTranslation`,
`TestArrWriteSanitizesWrappedContextErrors` and
`TestArrWriteSanitizesWrappedContextBodyErrors` provide synthetic evidence.
The public constructor keeps registration and import capabilities unknown
while G-01 is open, so no live native write is enabled by this partial path.

## Verification

All commands below were run from the final product tree unless noted. The
working tree was clean before handoff creation, and the versioned pre-commit
hook passed for both product commits.

| Command or scenario | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/adapters/arr/write -race -count=1` | Passed after final product tree. |
| `GOWORK=off go test -mod=readonly ./tests/fixtures/arr/write -race -count=1` | Passed after final product tree. |
| `GOWORK=off go test -mod=readonly ./internal/adapters/arr/... -race -count=1` | Passed. |
| Focused provider, metadata and request-local context regressions with `-race -count=10` | Passed. |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/arr/write ./tests/fixtures/arr/write` | Passed. |
| `GOWORK=off go mod verify` | Passed; all modules verified. |
| `GOWORK=off go test -mod=readonly ./... -count=1` | Passed on immediate rerun in 12.3s. A concurrent first matrix invocation had one unrelated W-02 lease-renewal failure; all Arr packages passed in that invocation. |
| Nested module tests/race/vet/mod verify for qBittorrent, NZBGet, Sonarr, Radarr, Jellyfin, Seerr, UI and tools | Passed. |
| `GOWORK=off ./scripts/check-lint.sh`; `python3 scripts/check-architecture.py`; `python3 scripts/check_planning.py` | Passed: zero lint/import issues; 53 tasks, 60 acceptance cases and links valid. |
| `GOWORK=off ./scripts/check-guardrails.sh --fast` | Passed before product commit and again through each product commit's pre-commit hook; generation, staged generation, six standalone Vacuum contracts, API, architecture and targeted tests passed. |
| `GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` in root, Sonarr and Radarr | Passed for all six module/architecture combinations. |
| `git diff --check` and owned-file `gofmt` | Passed. |

No live Arr service, credential, private coordinate, tracker data, media
inventory, filesystem mutation or upstream write was used. All fixtures and
contexts are synthetic.

## Checkpoint

- Final product SHA: `ba56aaf0f7b2aafc15923c2c92765f89f42a0007`.
- Previous correction product SHA: `923e31bbc22aac9782a9d72ca0d59cb8d857f1b8`.
- Handoff commit: pending; this file is committed separately.
- Coordinator state update: pending; this lane does not edit state.
- R4 blocker remains open until a separately reviewed, published Sonarr/Radarr
  client contract owns metadata, history and the relevant writes.
- Next safe action: commit this handoff, record its exact SHA in coordinator
  state, and assign independent review against the final product tree.
