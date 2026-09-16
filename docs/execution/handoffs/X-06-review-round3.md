# X-06 independent review, round 3

## Decision and exact scope

Decision: **changes_requested**. R2a is closed, and the original distinct-file
R2b reproduction is closed. R2b remains open for contradictory duplicate
fileless/file-bearing episode rows. Public Import still returns false
`already_satisfied` from those inputs. No native write capability is enabled.

- Reviewer: `/root/x05_reviewer`; independent of the product author.
- Product: `ca242c9fcab7abb63912402c62edae4ff56136c1`.
- Product tree: `dbcbd996f72cffb6611aa65a24f1c094d5f1ea09`.
- Correction handoff: `8434df6ff26ce79751528ca327e641a1c78bb699`.
- Handoff tree: `96aef896f2b547e6ecedf5a47567334fbee8321c`.
- Prior receipt: `cf856714579f54c1cfe06fbc071d8ce89c8f6fe6`.
- Scope: `internal/adapters/arr/write/`, `tests/fixtures/arr/write/`, root
  ports and standalone-client boundary; A-12/A-13/A-16/A-17/A-33.

Review ran in a detached isolated checkout at the exact handoff. Independent
synthetic probes ran in a separate archive of the exact product. Shared main,
product paths, execution state and live services were untouched. Only this
receipt is committed; coordinator records its exact review SHA separately.

## R2b1 — P1: contradictory fileless duplicate episode is ignored

Locations: `internal/adapters/arr/write/client.go:488-493`, `client.go:506-510`
and the initial success predicate at `client.go:302-307`.

The new byEpisode index sees only file-bearing rows. A row with
`episodeFileId=0` and `episodeFile=null` skips the identity index entirely.
The same episode can therefore claim both no file and a complete file in one
response. Either order succeeds. This leaves the prior R2b requirement to
validate every episode identity across the whole response incomplete.

Deterministic public-constructor reproduction:

1. New a Sonarr client with connection arr-main, a synthetic httptest endpoint
   and RootPaths mapping downloads to `/synthetic/downloads`.
2. GET `/api/v3/episode` returns the following; GET `/api/v3/history` returns
   an empty array. Count any non-GET request as a failure.

```json
[{"id":301,"seriesId":201,"episodeFileId":0,"episodeFile":null},{"id":301,"seriesId":201,"episodeFileId":801,"episodeFile":{"id":801,"seriesId":201,"path":"/synthetic/downloads/one.mkv","size":10}}]
```

3. Import registered ID 201, preview revision synthetic-preview-revision,
   transfer copy, source downloads:one.mkv, episode association 301.
4. Result: nil error, one file 801 with episode 301, and effect
   `already_satisfied`. Zero writes. Reverse the two rows: identical result.

Independent probe
`TestReviewerSonarrIncompleteOrDuplicateEpisodeCannotSucceed` reproduced both
`duplicate-episode-no-file-first` and `duplicate-episode-no-file-last` on all
three race-enabled repetitions. Assertions explicitly refuse nil error or an
already-satisfied effect, and separately enforce zero writes. These are
semantic assertion failures, with no race detector report. G-01 cannot prevent
this false public success because desired-state observation runs before its
dispatch gate.

Required correction: validate uniqueness/consistency of every episode ID before
any fileless-row continue. Duplicate identities with conflicting file presence
must return unknown/conflicted evidence and no success effect in either order.
A series-wide seen-episode check, consistent with the standalone Sonarr client
at `clients/sonarr/client.go:535-546`, can reject duplicates before aggregation.
Keep valid different episodes sharing one complete file accepted. Add both
orders and retain the existing foreign/collision and season-pack controls.

## Verified corrections and boundaries

Missing, null, zero and foreign nested Sonarr seriesId now fail closed through
public New. The same episode under distinct complete file IDs/paths now fails
closed. Both required nested-series checks are unconditional and require a
positive exact ID. mapNativeFile no longer populates Sonarr MovieID.

An independent public positive control uses episodes 301/302, both linked to
complete file 801, series 201, pack.mkv and size 20. It returns one file with two
episode associations, empty MovieID, already_satisfied and zero writes on all
three race repetitions. The committed synthetic season-pack control also
passes after its fixtures gained complete nested series identity.

Prior independent forged-revision versus fixed trusted resolver, same-file
contradictory details, same-path distinct-file identity, foreign Radarr title,
wrapped canceled/deadline transport/body error and explicit monitoring probes
all pass three race repetitions. Both Arr payloads preserve nil-default false
and explicit true/false without search. Committed preview omission/rejection,
lost-command response and partial-read-back tests pass. Public New retains
independent unsupported/unknown registration and import capability gates.

PreviewResolver is still a trusted root composition seam: its actual native
recency and immutable connection/title/transfer revision authority are not
proven by the synthetic request-echo helper. No production native resolver,
source-preserving mapped library placement, exact command/history provenance,
Jellyfin availability or Seerr status is established by these protocol tests.
The earlier receipts' native compatibility and registration lost-response
bounds remain separate work. G-01 stays open; X-20 remains the standalone write
migration. No live upstream was used. No generated DTO/standalone client import
was introduced into root domain/ports or public API. Fast isolation checks
accept no go.work or local replace directive.

## Independent checks

| Command | Outcome |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s ./internal/adapters/arr/write ./tests/fixtures/arr/write ./internal/adapters/arr/read` | Exit 0; write 1.528s, fixtures 1.726s, read 29.587s. |
| `GOWORK=off go test -mod=readonly -count=1 -timeout=180s ./...` | Exit 0; full root suite, storage 15.966s. |
| `GOWORK=off go vet -mod=readonly ./...`; `GOWORK=off go mod verify` | Exit 0; all modules verified. |
| Exact-product archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewer -v ./internal/adapters/arr/write` | Exit 1; only the two fileless/file-bearing duplicate variants fail in each repetition; original identity, preview, error and monitoring probes pass. |
| Archive: `GOWORK=off go test -mod=readonly -race -count=3 -timeout=120s -run TestReviewerPublicCompletePack -v ./internal/adapters/arr/write` | Exit 0; public complete season-pack control, 1.394s. |
| Each of ui, tools and all six clients: offline `GOWORK=off GOPROXY=off GOSUMDB=off go test -mod=readonly -count=1 -timeout=120s ./...`, then same with `-race`; module-local vet/mod verify | Exit 0 for all eight nested modules. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | Exit 0; generation/candidate generation, root and staged standalone API/Vacuum, formatting, isolation, architecture and targeted tests; final fast success. |
| `GOWORK=off ./scripts/check-lint.sh` | Exit 0; nine zero-issue module results and import boundaries. |
| `python3 scripts/check_planning.py` | Exit 0; 53 tasks, 60 acceptance cases; links resolve. |
| Linux amd64/arm64, `CGO_ENABLED=0 GOWORK=off go build -mod=readonly ./...` | Exit 0 for both root builds. |

The receipt commit runs the versioned pre-commit hook without bypass. Full
--ci remains C-06-owned and was not rerun; remote CI, release, deployment and
native write compatibility are not approved. Correct R2b1 and independently
re-review before clearing X-06. Preserve G-01 and blocked native capabilities.
