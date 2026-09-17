# U-02 independent review round four

## Decision

`changes_requested`.

R2b and R5d close, and direct-handler coverage now accepts the producer's 257
file and 33 candidate form. One P1 remains in R5c: the parser advertises query
bounds above the HTTP server's transport limit. A handler-valid maximum draft
therefore receives HTTP 431 before inventory code or its read port runs. U-02
cannot claim that every accepted/rendered collection and control is submittable.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; reviewer did not author U-02.
- Corrected product commit:
  `d832964b35cf2cfe2adf2b39d91e62c2f46bd745`.
- Product tree: `5b5ea536dd187b2915d06a126cfb4343ed31048f`.
- Product base: `1900d1f67e3ec7a5d0a383ab9fec30fc5da99af9`.
- Correction handoff commit:
  `90d6238dfd408f3e599d7450bb5bac6a300d17d9`.
- Handoff tree: `cc15b00a67f97e2c94a32b6f10e4da1077fe3dff`.
- Clean review checkpoint:
  `24cca8438d7a66a968523a0a2720c478eb702566`.
- Checkpoint tree: `24fe2e19c89087c290054115ac413ec3e2a3a95a`.
- Handoff SHA-256:
  `5651672b3521c7c7d91285b9fee96a27fe31e045dcaaf0e97eb77089d0a677ef`.
- Product diff SHA-256 for `ui/internal/inventory/`:
  `f002ea59ac69e547d3d640e91cdd546c2e67ee1ac43784c75ae0f80d03472a4f`.

Git ancestry binds product to handoff and handoff to checkpoint. Product changes
only `inventory.go`, `inventory_test.go`, and `render.go` under the owned
package. Review used synthetic in-memory readers and local `httptest` HTTP
servers only. Temporary reviewer probes were removed before this receipt.

## Finding

### R5c-a — P1 — Maximum accepted draft exceeds the serving transport's request limit

The correction raises `MaxQueryValues` to 7,816 and `MaxQueryRawLength` to 8
MiB so the handler can parse seven fields for 1,000 files plus eight fields for
100 candidates (`inventory.go:27-48`, `inventory.go:593-603`). The production
UI constructs a standard `http.Server` without setting `MaxHeaderBytes`
(`ui/cmd/mastarr/main.go:181-191`), so Go's lower default request-header limit
applies before `inventory.Handler` sees the URL.

Independent reproduction constructed a valid maximum discovery with 1,000
files and 100 candidates, then submitted all 7,800 dynamic controls plus
`rootId`, `limit`, and `cursor` through `httptest.NewServer`. Every text value
was 240 plain ASCII alphanumeric bytes, candidate episode lists contained the
accepted 256 non-negative integers, enums and booleans were valid, and both
aggregate checks were inside the product's bounds:

- unique query fields: 7,803, below `MaxQueryValues` 7,816;
- encoded query length: 1,793,691 bytes, below `MaxQueryRawLength` 8,388,608.

The real HTTP request returned 431 and the fake inventory reader recorded zero
calls. A second probe using URL-escaped but still handler-valid text produced
the same 431 at 3,857,691 bytes. In contrast, the producer's regression calls
`Handler.ServeHTTP` directly with shorter values, so it bypasses the server
boundary that rejects the claimed maximum.

This is not only a theoretical parser limit: the page renders each accepted
control as a native GET form, and sequential association/candidate edits retain
the other family's query state. At accepted collection and field bounds, the
browser cannot reach application validation or preserve the exact draft.

Required correction: align normalized collection limits, per-field bounds,
form composition, and the deployed HTTP transport so every accepted form has a
reachable finite representation. A fix must exercise an actual HTTP server at
the chosen maximum, not only direct `ServeHTTP`; it should also account for the
documented HTTPRoute deployment boundary rather than assuming an 8 MiB URL is
portable.

## Closed prior findings and positive evidence

- R2b closes. Discovery and media back links retain only valid list state,
  omit detail draft fields, and follow successfully. Independent download and
  descriptor back-link probes also passed. Draft values remain available in
  their detail forms before navigation.
- R5c closes for the prior nominal probes. File index 256 and candidate index
  32 submit and reload in stable order, and the direct handler accepts every
  field in that 257-file/33-candidate form. R5c-a remains for actual transport
  and true accepted maximums.
- R5d closes. Downloads and descriptors reject all fixed media draft keys with
  400 before a read. Valid media identity/provider/kind/selection/episode and
  subtitle drafts round-trip; invalid provider identities, booleans, and kinds
  fail closed.
- R1-R5, R2a, and R5a regressions pass: requested/returned identities bind,
  256 candidate episodes and float32 scores retain exact order/precision,
  coverage windows and unknown states remain explicit, malformed dynamic keys
  and absent response indexes fail closed, and nested API evidence stays strict.
- Read-only GET/HEAD routing, one-page API pagination, safe errors, cancellation,
  private headers, escaped HTML, generated DTO containment, and UI/root import
  boundaries remain intact. No mutation or live-service call was found.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned diff, SHA-256s, `git diff --check`, and clean worktree before receipt | PASS. |
| Actual default Go HTTP server, maximum accepted alphanumeric draft | FAIL as R5c-a describes: HTTP 431, 7,803 fields, 1,793,691 raw bytes, zero reader calls. |
| Actual default Go HTTP server, maximum accepted URL-escaped draft | FAIL consistently: HTTP 431, 3,857,691 raw bytes, zero reader calls. |
| Independent download/descriptor/media back-link and route-scope matrix | PASS; links followed with 200, unrelated fixed fields returned 400. |
| `GOWORK=off go test ./internal/inventory -run 'TestDetailBackLinksFollowUsableListRoutes\|TestMaximumRenderedCollectionsAcceptFullDraftSubmission\|TestFixedDetailDraftVocabularyIsRouteScopedAndValidated\|TestCandidateDraftBoundaryAndScoreRoundTrip\|TestCoverageWindowsRoundTripAndZeroRejection\|TestDynamicDraftKeysAreStrictAndBoundToEvidence' -count=10 -timeout=180s` | PASS. |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s`, race equivalent, and package vet | PASS; ordinary `0.528s`, race `1.667s`. |
| Full UI ordinary/race tests, `go vet ./...`, `go mod verify`, and offline `go mod tidy -diff` | PASS; no module diff. |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; `generation checks passed`. |
| Architecture-only lint, `check-architecture.py`, and `check_planning.py` | PASS; standalone boundaries and 53 tasks/60 acceptance cases valid. |
| CGO-free UI builds for Linux amd64 and arm64 | PASS; both ELF artifacts built from `./cmd/mastarr`. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0, final `guardrail checks passed (ci)`, including root storage race `200.165s`. |

## Acceptance disposition

- A-01, A-07, A-08, and A-09: corrected U-02 contribution retains its scoped
  passing evidence; end-to-end closure remains with other lanes.
- A-10 and A-11: not accepted for U-02 at maximum supported collections because
  exact identity/episode/subtitle drafts can be rejected by HTTP transport
  before application validation.
- A-49: not accepted while an accepted form cannot preserve input through its
  real HTTP submission path. R2b's generated list links themselves now pass.
- Strict query/protocol gate: parser grammar and route scoping pass, but parser
  bounds are not an honest reachable transport contract.

## Integration recommendation

Do not mark U-02 complete. Correct R5c-a and submit the exact new product for
another independent review. Green direct-handler and aggregate guardrails do
not cover maximum GET request transport.
