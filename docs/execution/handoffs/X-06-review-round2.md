# X-06 independent review, round 2

## Decision and scope

Decision: **changes_requested**. R2 remains open as two P1 cases. R1, R3 and
R4 have their requested correction mechanisms; the original collision and
foreign-Radarr R2 reproductions now fail closed. Incomplete or contradictory
Sonarr associations still produce false public `already_satisfied` results.

- Reviewer: `/root/x05_reviewer`; not the product author.
- Product: `8e811aa460d12eb374a623e87fb1d7531e015b3d`.
- Product tree: `49937191f5c3a4caa5459e092649336e5e720685`.
- Correction handoff: `da6b0aedfe27fe3a113974bd2744964b5e6c2214`.
- Handoff tree: `cedcf1f25528b781efa770481e2da4d5e945ee9a`.
- Prior receipt: `fbf2ecd106e50d89bf20c93cd926a52cd8e86f42`.
- Reviewed paths: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`,
  existing root ports and the standalone Sonarr compatibility boundary.
- Acceptance: A-12/A-13/A-16/A-17/A-33.

Review used a detached isolated checkout at the exact handoff. Independent
synthetic probes used a separate archive of the exact product. No product,
state, shared checkout or live-service files were changed. This commit contains
only this receipt; coordinator records its exact SHA separately.

## R2a — P1: missing Sonarr parent identity permits public success

Locations: `internal/adapters/arr/write/client.go:492` and `client.go:939`.
Both checks validate nested series identity only when `SeriesID != 0`.
The non-pointer DTO also decodes missing, null and zero seriesId identically.
Each becomes accepted evidence for the requested series. This contradicts the
prior R2 correction requirement to validate parent identity presence, and the
handoff claim that missing nested file identity is rejected.

Deterministic public-constructor reproduction:

1. `New` with connection `arr-main`, kind Sonarr, a synthetic httptest endpoint
   and RootPaths mapping downloads to `/synthetic/downloads`.
2. GET `/api/v3/episode` returns the following array; GET `/api/v3/history`
   returns an empty array. No mutation route is supplied.

```json
[{"id":301,"seriesId":201,"episodeFileId":801,"episodeFile":{"id":801,"path":"/synthetic/downloads/one.mkv","size":10}}]
```

3. Import registered ID `201`, revision `synthetic-preview-revision`, transfer
   copy, source `downloads:one.mkv`, association `301`.
4. Result: nil error, file 801 associated with episode 301, and effect outcome
   `already_satisfied`; zero writes. Replacing the omitted nested seriesId with
   null or zero produces the same result.

All three variants reproduced on all three race-enabled repetitions in
`TestReviewerSonarrIncompleteOrDuplicateEpisodeCannotSucceed`.
This is public observation behavior before G-01's write gate, not synthetic
capability enablement. The standalone client already requires nested
`id, seriesId, path, size` in `clients/sonarr/openapi.yaml:464` and
`clients/sonarr/client.go:1053`; the handwritten write read-back must not weaken
those identity requirements.

Required correction: require present, non-null, positive nested series identity
matching the requested series before success; preserve uncertainty for missing
or malformed required evidence. Cover missing/null/zero/foreign cases through
public Import and retain a fully populated native-shaped positive fixture.
Do not manufacture a movie association for a Sonarr file: current mapping also
puts requested series ID 201 into `ports.MediaFile.MovieID`.

## R2b — P1: one episode can claim two distinct files without rejection

Location: `internal/adapters/arr/write/client.go:477-522`.
The duplicate-episode check applies only inside one file's EpisodeIDs. There is
no series-wide episode identity index. An episode repeated under different
file IDs at different paths is accepted into both associations. The requested
first path can then satisfy Import despite contradictory evidence elsewhere
in the same response.

Use the same public constructor, request and history fixture as R2a; return:

```json
[{"id":301,"seriesId":201,"episodeFileId":801,"episodeFile":{"id":801,"seriesId":201,"path":"/synthetic/downloads/one.mkv","size":10}},{"id":301,"seriesId":201,"episodeFileId":802,"episodeFile":{"id":802,"seriesId":201,"path":"/synthetic/downloads/two.mkv","size":10}}]
```

Result: nil error, two files each claiming episode 301, effect
`already_satisfied`, and zero writes. This reproduced on all three race-enabled
repetitions of the independent probe. Existing same-file duplicate handling
and same-path distinct-file handling do not cover this different-path case.

Required correction: validate each episode identity across the whole response
before constructing success evidence; reject duplicate or contradictory
associations. Keep legitimate different episodes sharing one fully populated
file accepted. Apply the same strict aggregation before and after dispatch.

## Corrections verified and remaining evidence bounds

R1 now has a root-owned trusted PreviewResolver seam, exact revision comparison,
selected-file/association/subtitle metadata set binding and selected-rejection
refusal. Missing resolver also fails closed. The old forged-token probe was
adapted to supply a fixed trusted server revision, rather than the synthetic
helper's request-echo resolver; it now makes zero commands. Committed omitted
selection and selected-rejection regressions pass. Revision mismatch covers
stale-token disagreement. A nonzero ObservedAt alone does not prove recency;
actual current native observation, revision authority, connection/title/transfer
binding and workflow approval remain obligations of the trusted root resolver.
No production resolver/native compatibility is established by the echo helper.
G-01 remains explicit and public changed-state writes remain unsupported.

The original R2 same-ID changed-path/size, distinct-IDs same-path and foreign
Radarr title probes now return failures without success. Radarr also requires
its nested movie association. The new R2a/R2b cases above prevent approval of
the broader identity-safety claim.

R3 canonicalizes wrapped canceled/deadline errors while preserving errors.Is.
Independent active-parent transport and body probes for both causes pass and
exclude original endpoint/private wrapper text. R4 applies unmonitored false
only when unspecified. Independent Radarr and Sonarr payload tables for nil,
true and false monitoring pass with no search options enabled. An initial
reviewer Sonarr payload probe lacked its required series type and correctly
failed invalid_input; rerunning with standard series type passed.

No generated/standalone DTO was added to root ports or public API. Root go.mod
and all nested manifests contain no replace directive, and fast module
isolation accepts no go.work. This lane retains handwritten blocked writes;
X-20 migration and native no-overwrite evidence remain separate work.
Source-path success fixtures do not prove mapped library placement, source
preservation or native subtitle/anime behavior. Command acceptance, imported
file identity, history, Jellyfin availability and Seerr state remain distinct.
The earlier receipt's registration lost-response/history/native-compatibility
limits are not closed by these four corrections. No live upstream was used.

## Independent checks

| Command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.801s, fixtures 1.375s, read 26.403s. |
| Focused vet and `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=180s ./...` | Exit 0; full root suite, storage 12.243s. |
| `GOWORK=off go vet -mod=readonly ./...` | Exit 0. |
| Exact-product archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/write` | Exit 1; only remaining identity probe's four cases fail each repetition; prior R1/R2/R3/R4 probes pass; no race report. |
| Archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewerBodyContextsAndMonitoringDefaults -v ./internal/adapters/arr/write` | Exit 0 after supplying required Sonarr series type; both body context causes and both products' monitoring defaults pass. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; deterministic generation/candidate checks, root and staged standalone API/Vacuum validation, architecture, isolation, formatting and targeted tests; final fast success. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue modules, import boundaries pass. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; links resolve. |
| Linux amd64 and arm64, `CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 for both root cross-builds. |

Receipt commit runs the versioned pre-commit hook without bypass. Full --ci,
remote CI, release/deployment and native write compatibility are not approved
here; C-06 owns the full aggregate. Keep X-06 changes_requested until R2a/R2b
are corrected and independently re-reviewed. Keep G-01 open.
