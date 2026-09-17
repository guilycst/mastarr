# U-02 independent review round three

## Decision

`changes_requested`.

The correction closes the original 256-episode value-length and score-precision
defects, retains and validates coverage window timestamps, and rejects the
malformed dynamic-key matrix covered by the producer. Independent probes found
two remaining P1 exact-draft failures and one P2 route-scope failure. A rendered
detail page can still generate URLs or forms that its own handler rejects, so
U-02 does not yet clear A-10, A-11, A-49, or its strict-query contribution.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; reviewer did not author U-02.
- Corrected product commit:
  `6bd6c9c188074ec8c5e05b33f3723f39aad16927`.
- Product tree: `0daa160b9227bc1f8a1ed0bfeac40e06c50a6e05`.
- Product base: `ab42ce0dd43eb4bfc5145b3d6549ec7463928684`.
- Correction handoff commit:
  `55c957eec5d2f7c68ca5eb25b5043f82522f6471`.
- Clean review checkpoint:
  `6f3ce9ea49c6c6c6f1bbbedec12a053d47614dfd`.
- Checkpoint tree: `22390a6f585ea1997e32b5fe93338780bb613aea`.
- Handoff SHA-256:
  `b8f87e5526b0bf2ff281e7ee56b783b68b15fd967db7e2d1b75574b552613598`.
- Product diff SHA-256 for `ui/internal/inventory/`:
  `4afc37930f0ebf4ab1137524e6337b90afd80dc6c11f8ea45a0715c94f535c93`.

Git ancestry binds product to handoff and handoff to checkpoint. Product changes
only `convert.go`, `inventory.go`, and `inventory_test.go` under the owned
package. Review used synthetic in-memory readers and loopback HTTP only.
Temporary reviewer probes were removed before this receipt.

## Findings

### R2b — P1 — Detail back links retain draft keys on a list route that rejects them

`detailWriter.start` builds every back link from the list route and
`query.encoded(true)` (`render.go:219-225`). That explicitly retains detail-only
candidate and association values. List parsing rejects every such value as an
unknown list query (`inventory.go:652-687`). The required back-link round trip
therefore cannot succeed.

Independent reproduction:

1. GET
   `/discoveries/discovery-1?candidate-0-episodes=1%2C2&candidate-0-title=Draft&limit=7`
   against a discovery with candidate index 0 returned 200.
2. The rendered link was
   `/discoveries?candidate-0-episodes=1%2C2&candidate-0-title=Draft&limit=7`,
   so it retained the draft exactly.
3. GET of that generated link returned 400 `Invalid inventory query`.

This also affects fixed media and association drafts. Retaining values in an
unusable URL is not the handoff's claimed render, GET submit, reload, and back
link round trip. Use a route that accepts the retained draft, or omit the draft
from a list back link while preserving it through the documented usable flow.

### R5c — P1 — Valid returned collections exceed parser index and form-control bounds

The normalized response accepts up to 1,000 discovery files and 100 candidates
(`inventory.go:27-32`, `convert.go:35-73`). Rendering emits seven controls for
every file and eight controls for every candidate (`render.go:238-332`). Query
parsing instead rejects every dynamic index at or above 256 and rejects more
than 256 total detail inputs (`inventory.go:652-679`). The global 512-field cap
also cannot represent the complete rendered form at the accepted collection
limits (`inventory.go:38`, `inventory.go:591-592`).

Two independent reproductions failed before any write:

- A valid discovery with 257 returned files rendered an
  `association-256-role` control, but GET with
  `association-256-role=subtitle` returned 400.
- A valid discovery with 33 returned candidates has 264 successful controls.
  Submitting all eight controls for each candidate returned 400 because the
  detail-input count exceeded 256. All indexes were present in the response.

The same mismatch affects larger file forms and the documented maximum of 100
candidates. Bind indexes to the actual returned collection and set aggregate
bounds high enough for every form the package renders, or lower normalized
collection limits so the UI never emits an unsubmitable form. Regress maximum
accepted files/candidates, partial edits at the last index, full browser-style
form submissions, reload, and stable ordering.

### R5d — P2 — Fixed detail draft keys are accepted on unrelated resource routes

Dynamic draft families are now correctly restricted to discovery detail, but
`allowedDetailInput` admits every fixed draft key on every detail route
(`inventory.go:673-708`). No per-route binding follows for media, downloads, or
descriptors (`inventory.go:399-470`).

Independent requests to `/descriptors/descriptor-1` with each of
`identity=Draft`, `episode=1`, `subtitleForced=true`, and `selection=foreign`
returned 200. The values were accepted as hidden state and appeared in generated
back links even though a descriptor has no such draft surface. The resulting
list links then returned 400 under the R2b behavior.

Strict unknown-query handling requires an explicit fixed-field vocabulary per
route, including field-specific boolean/identity/enum validation. Unrelated
authority-shaped fields must fail with the safe 4xx page rather than persist as
hidden state.

## Closed prior findings and positive evidence

- Original R2a direct detail flow closes for one valid candidate: all 256
  episodes retain order and count, and `float32(0.9137)` uses lossless shortest
  formatting. Targeted tests passed ten repetitions. R2b and R5c still block the
  complete form/back-link promise.
- R5a closes: `StartedAt` and `CompletedAt` survive conversion and rendering;
  present zero values fail protocol validation; absence renders `unknown`.
  Page and item evidence includes scope IDs, observed time/count, revision, and
  reasons.
- Core R5b grammar closes for tested dynamic controls: canonical numeric indexes,
  known suffixes, field enums, duplicate values, and returned-collection binding
  below the hard 256 cap behave as claimed. R5c is a distinct valid-collection
  limit mismatch.
- Prior requested-versus-returned identity, nested API validation, option markup,
  evidence visibility, read-only GET/HEAD routing, one-page pagination, private
  headers, escaped HTML, timeout/cancellation, and sanitized errors retain their
  existing passing coverage.
- UI package imports only its internal generated HTTP client; no Mastarr root,
  upstream client, DB, media mount, local `go.work`, or module `replace` was
  introduced. No mutation route or live-service access was found.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned diff, SHA-256s, `git diff --check`, and clean worktree before receipt | PASS. |
| Temporary back-link follow probe | FAIL: generated discovery list link retained candidate draft and returned 400, as R2b describes. |
| Temporary returned-index and full-form probes | FAIL: returned file index 256 and 264 controls for 33 returned candidates each produced 400, as R5c describes. |
| Temporary fixed-field route matrix | FAIL: four unrelated descriptor-detail fields each returned 200; first failure reproduced with `identity=Draft`, as R5d describes. |
| `GOWORK=off go test ./internal/inventory -run 'TestCandidateDraftBoundaryAndScoreRoundTrip\|TestCoverageWindowsRoundTripAndZeroRejection\|TestDynamicDraftKeysAreStrictAndBoundToEvidence\|TestHandlerRejectsForeignDetailIdentity\|TestHTTPReaderRejectsForeignDetailIdentity\|TestHTTPReaderRejectsMalformedNestedEvidence' -count=10 -timeout=120s` in `ui/` | PASS. |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s` and race equivalent with 180-second bound | PASS; ordinary `0.234s`, race `1.758s`. |
| Full UI ordinary/race tests, `go vet ./...`, `go mod verify`, and offline `go mod tidy -diff` | PASS; no module diff. |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; `generation checks passed`. |
| Architecture-only lint, `check-architecture.py`, and `check_planning.py` | PASS; standalone boundaries and 53 tasks/60 acceptance cases valid. |
| CGO-free UI builds for Linux amd64 and arm64 | PASS; both ELF artifacts built from `./cmd/mastarr`. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0, final `guardrail checks passed (ci)`, including root storage race `200.197s`. |

## Acceptance disposition

- A-01, A-07, A-08, and A-09: corrected U-02 contribution retains the scoped
  passing evidence; end-to-end closure remains with their other lanes.
- A-10 and A-11: not accepted for U-02 while valid returned file/candidate sets
  can generate controls that cannot survive submission and reload.
- A-49: not accepted because the page's own back link cannot carry the exact
  retained draft through a successful request, and unrelated detail routes can
  retain unbound authority-shaped fields.
- Strict query/protocol gate: not accepted until fixed fields are route-scoped
  and all normalized/rendered collection sizes have a submitable bound.

## Integration recommendation

Do not mark U-02 complete. Correct R2b, R5c, and R5d, then submit the exact new
product for another independent review. Green standard gates do not exercise
generated-link usability or maximum rendered-form cardinality.
