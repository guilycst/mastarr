# U-02 independent review round five

## Decision

`approved`.

No P1 or P2 finding remains in the reviewed U-02 scope. R5c-a closes at the
actual default Go HTTP transport: the largest handler-valid collection form,
including worst accepted URL expansion and 64-bit episode numbers, reaches the
inventory reader with HTTP 200. Prior back-link, route-scope, identity,
evidence, coverage, and strict-query corrections remain intact.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; reviewer did not author U-02.
- Corrected product commit:
  `6c3b1d819601fedd14b2c080b84bc5632e01b494`.
- Product tree: `619941d9a35fa659c48e48c1bfe4dbe836cf5dc4`.
- Product base: `6748085db3cd6dd5be9155ccd7d7a0b544300f12`.
- Correction handoff commit:
  `0e96c0ec7d84bab3efcd566b6352ab65cf12a607`.
- Handoff tree: `09902f5287127b70fcdc54a0c39a5456f6a4d1c4`.
- Clean review checkpoint:
  `2525f2e17ee2fbae5c60e7ad93f704962401a08e`.
- Checkpoint tree: `bc0565f93184e287043a0248a927136e51286c03`.
- Handoff SHA-256:
  `9b3011a0dd51c0b5792f620fb0f37698d02731d9415687eb52ec38f9f17436ba`.
- Product diff SHA-256 for `ui/internal/inventory/`:
  `bbe4d9ba45e29147bd774fd461ffbacc9dccc70d3f2d839a18b38ef6d309f7ef`.

Git ancestry binds product to handoff and handoff to checkpoint. Product changes
only `inventory.go` and `inventory_test.go` under the owned package. Review used
synthetic in-memory readers and local `httptest` servers only. Temporary
reviewer probes were removed before this receipt.

## R5c-a transport review

The corrected contract sets the scalar draft bound to 128 bytes, collection
ceilings to 257 files and 33 candidates, dynamic-control capacity to 2,063,
and total query capacity to 2,079 fields. `MaxQueryRawLength` is 768 KiB, below
the default Go server's 1 MiB request-header limit and with room for path and
ordinary headers (`inventory.go:21-55`).

The product regression exercises the maximum collections and escaped scalar
values through `httptest.NewServer`, then requires both HTTP 200 and a new
reader call. Independent review strengthened that case with the largest valid
native `int` on this 64-bit target for season, year, and every one of the 256
candidate episodes. Ampersand-filled 128-byte scalar values retained the full
three-byte URL-escape expansion.

Across three ordinary and three race-enabled independent repetitions:

- fields: 2,066, within the 2,079 bound;
- encoded raw query: 671,459 bytes, within the 786,432-byte parser bound;
- response: HTTP 200;
- reader calls: one per request.

This closes the prior 431 reproduction. The request reaches the inventory
handler and read port under production-equivalent default Go server parsing;
the parser bound also leaves 114,973 bytes below its own ceiling and materially
more room below the server limit for path/header overhead.

## Prior finding regression

- R1 remains closed: requested and returned discovery, media, download, and
  descriptor IDs must match before rendering.
- R2/R2a remain closed: fixed and dynamic drafts preserve identity, episode,
  subtitle language/forced/SDH/pair, candidate fields, all 256 episode entries,
  stable ordering, and lossless float32 score text.
- R2b remains closed: detail back links contain only route-valid list state,
  omit detail drafts, and follow successfully; detail forms retain usable
  drafts before navigation.
- R3 remains closed: file/provenance/download/descriptor IDs and timestamps,
  per-instance tracking, scope, completeness, revisions, counts, and reasons
  remain distinct and visible without inferred absence.
- R4 remains closed: option values and selected state remain valid escaped HTML.
- R5/R5a/R5b/R5d remain closed: coverage start/completion timestamps survive and
  present zero values fail; dynamic keys require canonical indexes, known
  fields/enums and returned evidence; fixed fields are media-only and
  field-validated; downloads/descriptors reject authority-shaped draft keys.
- Read-only GET/HEAD routing, one-page API pagination, timeout/cancellation,
  sanitized retriable errors, no-store/noindex/nosniff/no-referrer headers,
  private HTML escaping, generated DTO containment, and HTTP-only UI boundaries
  remain intact. No mutation route or live-service access was found.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned diff, SHA-256s, `git diff --check`, and clean worktree before receipt | PASS. |
| Independent maximum default-server probe with 64-bit integers and worst URL-escaped scalar values, `-count=3` | PASS; each run HTTP 200, one reader call, 2,066 fields, 671,459 raw bytes. |
| Same independent maximum probe under `-race -count=3` | PASS with identical transport evidence. |
| `GOWORK=off go test ./internal/inventory -run 'TestMaximumDraftFitsDefaultHTTPServerHeaderBudget\|TestMaximumRenderedCollectionsAcceptFullDraftSubmission\|TestDetailBackLinksFollowUsableListRoutes\|TestFixedDetailDraftVocabularyIsRouteScopedAndValidated\|TestCandidateDraftBoundaryAndScoreRoundTrip\|TestCoverageWindowsRoundTripAndZeroRejection\|TestDynamicDraftKeysAreStrictAndBoundToEvidence\|TestHandlerRejectsForeignDetailIdentity\|TestHTTPReaderRejectsForeignDetailIdentity\|TestHTTPReaderRejectsMalformedNestedEvidence' -count=10 -timeout=240s` | PASS. |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=180s`, race equivalent, and package vet | PASS; ordinary `2.552s`, race `1.515s`. |
| Full UI ordinary/race tests, `go vet ./...`, `go mod verify`, and offline `go mod tidy -diff` | PASS; no module diff. |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; `generation checks passed`. |
| Architecture-only lint, `check-architecture.py`, and `check_planning.py` | PASS; standalone boundaries and 53 tasks/60 acceptance cases valid. |
| CGO-free UI builds for Linux amd64 and arm64 | PASS; both ELF artifacts built from `./cmd/mastarr`. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0, final `guardrail checks passed (ci)`, including root storage race `202.297s`. |

## Acceptance disposition

- A-01, A-07, A-08, and A-09: U-02's scoped read-only inventory, complete
  evidence, unknown-state, and per-instance identity contributions pass.
- A-10 and A-11: U-02's exact identity/episode/season-pack/subtitle draft and
  transport contributions pass at the declared maximum boundaries.
- A-49: U-02 deep-link identity and input-state preservation contribution passes;
  later review/workflow mutation flows remain owned by their dependent lanes.
- Strict query/protocol contribution passes for route vocabulary, duplicate and
  unknown fields, dynamic grammar, evidence-bound indexes, scalar/cardinality
  limits, and the reachable default-server transport ceiling.

## Integration recommendation

Integrate the exact product and this receipt, then mark U-02 complete in the
coordinator ledger. Approval is limited to the reviewed local product and does
not claim browser/U-05, deployment, or live media-stack verification.
