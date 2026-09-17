# U-02 independent review round one

## Decision

`changes_requested`.

The product has a sound read-only HTTP boundary, bounded pagination, sanitized
errors, escaped output, private response headers, and no mutation surface. The
standard focused, module, generation, architecture, cross-build, and full
offline guardrail checks pass. Independent adversarial probes nevertheless
found three P1 correctness gaps and two P2 rendering/protocol gaps. U-02 does
not yet satisfy A-01, A-08, A-09, A-10, A-11, or A-49 for its assigned UI
contribution.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; the reviewer did not author U-02.
- Product commit: `0e0e66d16c4211e20d4b773262165f7e5b4e3dc9`.
- Product tree: `ade33d3f8fb4cdb563518c7ea9979e1ced81ebd3`.
- Product base: `5e688a4470a11e117a548e81c09f924b506a26a2`.
- Handoff commit: `f832b664104f2f0d07523a15cf019bd87cbf793a`.
- Handoff file SHA-256:
  `c8a18dbaa3f3ed097e993a0df24878cf31e6f34e7792986517bf99bb757cdf73`.
- Clean review checkpoint: `db1670aa8fe8dedeb974fe0a9961b6db4fefda45`.
- Checkpoint tree: `fd30f0b9c19c1e7a9a94da3f7f0330638bd38aae`.
- Product diff SHA-256 for `ui/internal/inventory/`:
  `014c5c083d1c8fdd2401559512ea2a95917d71b313aed454234b1732e38d9cf1`.
- Git proves product -> handoff -> checkpoint ancestry. Product changes are
  confined to the five files under `ui/internal/inventory/`.

Review used only synthetic in-memory readers, loopback `httptest` servers, and
temporary reviewer tests removed before this receipt. It used no live service,
credential, private coordinate, or media data. This receipt is the only
persistent review change.

## Findings

### R1 — P1 — Detail responses are not bound to the requested deep-link ID

`Handler.ServeHTTP` passes the path ID to the reader, but after a successful
read it validates only the returned object's intrinsic shape. It never compares
the returned `item.ID` with the requested `id` before rendering
(`inventory.go:359-418`). `HTTPReader.Get*` likewise decodes and returns a valid
foreign ID without checking it against the requested UUID (`http.go:160-187`,
`214-241`, `268-295`, and `322-349`). The renderers then use the foreign ID in
the page and form action (`render.go:113-190`).

An independent normalized-reader matrix requested `/discoveries/requested`,
`/media/requested`, `/downloads/requested`, and `/descriptors/requested` while
returning otherwise valid objects with ID `foreign`. All four returned HTTP 200,
displayed `foreign`, and the media form changed its action to `/media/foreign`.
This is the stale-detail reuse A-49 forbids and can associate later review state
with the wrong resource.

Required correction: bind every detail response to the exact requested ID at
the HTTP adapter and handler boundary, fail closed on mismatch, and regress all
four resource kinds using two similar valid UUIDs.

### R2 — P1 — Accepted GET draft state is overwritten or dropped

`parseQuery` accepts `selection`, `identity`, `providerId`, `episode`,
`subtitleLanguage`, `subtitleForced`, `subtitleSDH`, `subtitlePair`, `kind`, and
dynamic association/candidate fields (`inventory.go:617-653`). The media form
reads only `identity` from that state; `providerId` and `kind` are reset to the
API observation, and the selection, episode, and subtitle fields are not
rendered at all (`render.go:129-144`). Candidate kind/year are also absent, and
the candidate and association drafts live in separate GET forms which do not
carry the other form's successful controls (`render.go:224-292`). Submitting one
form therefore discards accepted draft state from the other form and its list
context.

The independent probe requested
`/media/media-1?identity=Draft&providerId=tmdb%3A2&kind=anime&episode=3&selection=episode-3`.
The response retained `identity=Draft`, reset the inputs to `providerId=tmdb:1`
and `kind=movie`, and emitted no episode or selection control. This contradicts
the U-02 deliverable and A-10/A-11/A-49 exact selection and draft-preservation
requirements.

Required correction: round-trip every accepted draft field, preserve all draft
families and list context across each GET form submission, show candidate kind
and other authority-bearing fields, and add reload/back/deep-link regressions.

### R3 — P1 — The views discard evidence needed to distinguish observations

The normalized models retain the required evidence, but the HTML omits it:

- discovery files omit `File.ObservedAt` (`render.go:224-244`);
- discovery provenance omits `Hash` and `CompletedAt`
  (`render.go:295-313`);
- per-instance tracking omits `ExternalID`, `ObservedAt`, and `CoverageID`
  (`render.go:328-351`);
- download detail omits `DeprecatedID` and `DescriptorID`
  (`render.go:160-174`); and
- coverage rendering reduces the observation to only `Completeness`, omitting
  source/connection/root, timestamp, count, revision, and reason codes
  (`inventory.go:798-813`).

An independent discovery probe supplied distinct file-observed and
client-completed timestamps plus a torrent hash; none appeared. A media probe
supplied an Arr external record ID and coverage ID; neither appeared. A-01
requires filesystem and client dates to remain separate, A-08 requires distinct
dimension timestamps, and A-09 requires explicit per-instance identity. The
current pages cannot support those reviews and can make incomplete evidence
look context-free.

Required correction: render the retained evidence with explicit unknown states,
including observation timestamps and source scope, and cover the orphan,
stale/offline manager, same-ID/different-instance, torrent, NZB, descriptor, and
seeding cases in synthetic detail tests.

### R4 — P2 — Unselected `<option>` elements are malformed

Both the media-kind filter and per-file role selector open `value="` but append
the closing quote only inside the selected branch (`inventory.go:1017-1025` and
`render.go:258-266`). A normal discovery response rendered, for example,
`<option value="subtitle>subtitle</option>`. Browser parsing can swallow later
options or submit a corrupted value, so the kind filter and role association
are not reliable semantic controls.

Required correction: always close the value attribute before optionally adding
`selected`, then parse the rendered HTML in a regression and assert every option
value and selected state.

### R5 — P2 — Strict decoding stops before nested semantic validation

`decodeStrict` rejects unknown and duplicate JSON fields, but required enum and
nested evidence checks are incomplete. `convertCandidate` cannot return an
error and validates none of its authority fields (`convert.go:120-147`),
`convertProvenance` validates neither IDs nor its `FileTarget`
(`convert.go:109-117`), and `validateDiscovery` checks only file entries and
collection lengths (`inventory.go:889-906`). Other normalized enum values are
accepted whenever non-empty.

An independent loopback API returned a syntactically valid discovery with a
candidate whose required `kind` was the empty string. `HTTPReader.ListDiscoveries`
accepted it as a successful page instead of returning `ErrorProtocol`. This
contradicts the handoff's strict-response claim and lets malformed candidate or
provenance evidence reach review controls.

Required correction: validate generated enum membership, required nested
candidate fields, bounded values, provenance identities, and root-relative
targets before translation/rendering. Add malformed list and detail fixtures
for every nested collection.

## Positive evidence

- The handler exposes only GET/HEAD; unsupported methods return 405 with
  `Allow: GET, HEAD` before reader dispatch.
- List reads issue one API request, preserve the supported cursor/limit/root,
  connection, or kind filter, and render the API `nextCursor`; there is no
  BFF fetch-all loop.
- Empty or malformed top-level responses use sanitized 503 pages rather than
  successful empty inventory. API 404 bodies and requested IDs are not echoed.
- Duplicate query values, unknown fields, oversized values, unsafe manifest
  paths, redirects, timeouts, and cancellation are bounded or rejected by the
  reviewed paths.
- HTML values use escaping, response headers are private/no-store/noindex, and
  descriptor content is not fetched or embedded.
- Generated DTOs remain inside `ui/internal/inventory/http.go`; architecture
  checks found no root, database, standalone-client, upstream, or media-mount
  import into the UI module.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, scope hashes, `git show --check`, and clean worktree before receipt | PASS; identities above. |
| Temporary detail-ID binding matrix across all four resources | FAIL as expected; all four foreign IDs rendered with HTTP 200 (R1). |
| Temporary media draft/evidence and discovery evidence probes | FAIL as expected; corrected values and required evidence were absent (R2/R3). |
| Temporary malformed nested candidate loopback fixture | FAIL as expected; invalid required enum was accepted (R5). |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s` in `ui/` | PASS; `0.397s`. |
| `GOWORK=off go test -race ./internal/inventory -count=1 -timeout=180s` in `ui/` | PASS; `2.350s`. |
| `GOWORK=off go vet ./internal/inventory` in `ui/` | PASS. |
| `GOWORK=off go test ./... -count=1 -timeout=180s` in `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1 -timeout=240s` in `ui/` | PASS. |
| `GOWORK=off go vet ./...` in `ui/` | PASS. |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` in `ui/` | PASS; verified and no diff. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; working-tree and staged generation checks passed. |
| `GOWORK=off ./scripts/check-lint.sh --architecture-only` | PASS; architecture and standalone-client boundaries passed. |
| `python3 scripts/check_planning.py` | PASS; 53 tasks, 60 acceptance cases, links resolve. |
| Linux amd64 and arm64 CGO-free `GOWORK=off go build ./...` in `ui/` | PASS. |
| Full offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 and final `guardrail checks passed (ci)`. Generation, Vacuum, lint, architecture, all module ordinary/race/vet/verify checks and Linux cross-builds passed; root storage race completed in `199.027s`. |

## Acceptance disposition

- A-01: not accepted for U-02; file and client timestamps are not shown.
- A-07: the bounded page-by-page/no-fetch-all contribution passes, but this does
  not close the other inventory findings.
- A-08: not accepted; tracking observation timestamps and coverage identity are
  omitted.
- A-09: not accepted; stale detail identity is accepted and external record
  identity is hidden.
- A-10 and A-11: not accepted; accepted correction/selection fields are lost and
  the role selector emits malformed HTML.
- A-49: not accepted; requested and displayed identity can differ, and GET draft
  state does not round-trip.

## Integration recommendation

Do not mark U-02 complete. Correct R1-R5 and submit the exact corrected product
for another independent review. The standard green gates do not cover the
identity-binding, draft-round-trip, evidence-visibility, HTML-structure, or
nested semantic cases above. Browser accessibility and interaction-ledger
closure remain later U-05/U-03 gates and are not claimed here.
