# X-19 independent correction review, round 2

## Decision and source

Decision: **changes_requested**. R1 is closed. R2's duplicate-key and invalid
UTF-8 paths are repaired, but a P1 scope-validation bypass remains in preview
compatibility selection (R2a below).

- Reviewer: `/root/x05_reviewer`, independent of the product author.
- Exact product: `4384a873d54df46b6d0f996a3bc03f6a2bde2edd`.
- Product tree: `2d4e83d82afba047d72d90dde5ecfb3ca9c10dd6`.
- Exact handoff: `3de67477461bbd67f34a4a77964a9094ae138cc1`.
- Handoff tree: `8ef18c79472965c08929fbbe740f9ef5b9623a18`.
- Prior receipt: `3d744e835290d372ccdecca37a4a19660e3239b8`.
- Scope: corrected `internal/adapters/arr/read/`, fixture changes and unchanged
  root/standalone module boundaries, against A-07/A-08/A-09/A-10/A-16.
- Review checkout: isolated, detached at the exact handoff. Additional probes
  used a separate archive of the exact product. Product and state were untouched.

## Closed findings and supported improvements

The shared native file registry compares file IDs against mapped path and size
and rejects a distinct ID claiming an existing mapped path. Both catalog and
ObserveImport use it. Catalog conflicts leave partial coverage and omit the
contradictory entry; import conflicts return unknown without a successful file
observation. The original reviewer probes for same-ID contradictory details,
distinct-ID path collision and valid multi-episode identity reuse now pass on
all three race-enabled repetitions. The included native Radarr/Sonarr tests
exercise these paths rather than selecting legacy fixture decoding.

Strict root decoding now rejects duplicate object members, invalid UTF-8,
trailing data and excessive nesting. The original duplicate Radarr `id` probe
now passes its rejection assertion. Included malformed catalog/preview and
invalid UTF-8 tests pass. Generic unknown/unsupported fallback was replaced with
operation-specific response-shape checks. File-array read-back fallback is
closed. This improves R2, but the preview gate does not validate all required
nested identities before selecting the older decoder.

## R2a — P1: unrelated legacy rejection marker masks missing episode identity

Locations: `internal/adapters/arr/read/native.go:162-191` and
`internal/adapters/arr/read/client.go:1690-1704`. Native contract reference:
`clients/sonarr/openapi.yaml:518-527`, where EpisodeReference requires
`id`, `seriesId`, `seasonNumber`, and `episodeNumber`; standalone enforcement
is at `clients/sonarr/client.go:1091`.

`legacyPreviewShape` checks candidate ID/path and the presence of name, size and
relativePath, then accepts the whole response when any candidate has a rejection
without `type`. It does not verify the nested episode's required series ID. The
legacy preview mapper explicitly skips a missing nested series ID. A failure
from missing native identity consequently becomes an accepted exact-import
preview when a different, unselected candidate carries the legacy marker.

Deterministic reproduction: use the package's `newSyntheticClient` helper with
Sonarr, connection `reviewer-sonarr`, page limit 2 and bounds 10/50/50. Every GET
`/api/v3/manualimport` returns the following unchanged synthetic response:

```json
[
  {"id":901,"path":"/downloads/incoming/episode.mkv","relativePath":"episode.mkv","name":"episode.mkv","size":10,"series":{"id":201},"episodes":[{"id":301,"seasonNumber":1,"episodeNumber":1}],"rejections":[]},
  {"id":902,"path":"/downloads/incoming/other.mkv","relativePath":"other.mkv","name":"other.mkv","size":10,"series":{"id":201},"episodes":[{"id":302,"seriesId":201,"seasonNumber":1,"episodeNumber":2}],"rejections":[{"reason":"Legacy synthetic rejection"}]}
]
```

Call `PreviewImport` for registered series `201`, transfer `copy`, with only
`library:incoming/episode.mkv` and `MovieOrEpisodeID: "301"` selected. The adapter
makes two requests, returns nil error, one accepted file, no rejections, and a
preview revision. The selected episode's series association was never present.
The legacy marker on `other.mkv` is enough to downgrade the complete response.

Reviewer probe `TestReviewerLegacyAliasCannotHideMissingEpisodeIdentity` failed
its safety assertion on all three race-enabled repetitions. There was no race
detector report. Removing the unrelated marker prevents this compatibility
selection; the missing identity must remain unknown regardless of that marker.

Required correction: make compatibility validation preserve required nested
identity and scope invariants for every candidate. Allow only the explicitly
supported alias differences; an unrelated legacy field cannot authorize dropping
required episode/series evidence. Validate nested series/file/episode IDs before
fallback or normalize narrowly into the strict module validation path. Add this
two-candidate regression, missing/null identity variants and a valid mixed
accepted/rejected compatibility positive control. Retain duplicate-key/UTF-8 and
file-collision regressions. Keep R2 open until this bypass is closed.

## Independently executed checks

| Command | Outcome |
| --- | --- |
| Root: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/arr/read ./internal/domain ./internal/ports` | Exit 0; adapter 23.792s, domain 1.999s, ports 1.407s. |
| Root focused `go vet -mod=readonly ./internal/adapters/arr/read` and `go mod verify`, with `GOWORK=off` | Exit 0; all modules verified. |
| Each Sonarr/Radarr module: `GOWORK=off go test -mod=readonly -race -count=1 -timeout=120s ./...`, vet and module verification | Exit 0; Sonarr 1.443s, Radarr 1.723s; all modules verified. |
| Each Sonarr/Radarr module: `GOWORK=off GOPROXY=off GOSUMDB=off ./check-generation.sh` | Exit 0; committed output reproduced offline using installed pinned tool dependencies. |
| Exact-product reviewer probes: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/read` | Exit 1 because R2a fails on all three repetitions. All original R1/R2 probes and consistent multi-episode positive control pass. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; final `guardrail checks passed (fast)`, including generation, candidate generation/Vacuum, architecture, formatting and targeted tests. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and import-boundary checks pass. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; local links resolve. |

Root dependency versions and both standalone module trees are unchanged from the
previously reviewed product. No generated DTO leaks or root/other-client imports
were introduced. The correction does not add upstream writes; G-01 remains
open. Lookup/history/reprocess continue through the documented root read-only
compatibility transport; live upstream version compatibility remains unproved.
Full root/CI matrix and live service checks are not claimed by this receipt.

## Handoff

Coordinator should record this receipt's exact Git commit SHA separately and
keep X-19 open for R2a correction and independent re-review. No product, state,
deployment, real media or live service mutation is part of this review.
