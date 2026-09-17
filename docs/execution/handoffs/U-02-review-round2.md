# U-02 independent review round two

## Decision

`changes_requested`.

The correction closes the stale-detail identity bug, preserves the ordinary
media/association/candidate draft fields, renders the evidence named in the
round-one receipt, fixes option markup, and rejects the covered malformed API
fixtures. Independent probes found one remaining P1 exact-selection failure and
two P2 protocol/evidence failures. U-02 therefore does not yet clear A-07,
A-10, A-49, or its strict-query contribution.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; reviewer did not author U-02.
- Corrected product commit:
  `0da6087590d1cc4a426da9a449fa61ea3d4b913c`.
- Product tree: `61c92a274f581eb2a82dd497ee62b83c7b644987`.
- Product base: `bc86603258fbcbbb0c284fcabba4dcd515a80510`.
- Correction handoff commit:
  `f8a087ce1a03b502b630fdd644d74fd72de423f5`.
- Clean review checkpoint:
  `b4d334e3cad676a4fd47c6439d79738f1f7aaddd`.
- Checkpoint tree: `b2b38f6fa815c03d7a7170429720ba2def6c83a6`.
- Handoff SHA-256:
  `52a98f61f212ef71c487b3ec044fbf0771bbce4f6a491290d9b6e6244ae4f16d`.
- Product diff SHA-256 for `ui/internal/inventory/`:
  `9aa671a23f30232b1fb49b499be68111f5a78e4e69747f44d13e131ddeb9471d`.

The dispatch message included an extra trailing `9` in the product identity.
The coordinator confirmed the exact 40-hex commit above; the handoff, Git
ancestry, and checkpoint agree. Product changes are confined to the five owned
files under `ui/internal/inventory/`. Review used synthetic readers and
loopback HTTP only. Temporary reviewer probes were removed before this receipt.

## Findings

### R2a — P1 — A valid season-pack candidate generates a draft the handler rejects

`validateCandidate` explicitly permits `MaxAssociationInputs` (256) episode
numbers (`inventory.go:1084-1102`), and `writeCandidates` serializes the entire
set into the `candidate-N-episodes` input (`render.go:302-329`). `parseQuery`
applies the unrelated 240-byte `MaxInputLength` limit to every detail value
before field-specific parsing (`inventory.go:588-601`). The UI can therefore
render a valid API candidate whose own GET form cannot round-trip.

The independent probe supplied one valid season candidate with episodes
1000..1255. The initial detail response was 200 and rendered the complete
1,279-byte CSV value. Submitting that exact generated value returned 400
`Invalid inventory query` before reading the discovery. This loses an exact
candidate episode set required by A-10 and violates the U-02 promise that
accepted candidate fields survive reload and deep links.

The same fallback conversion also rounds an observed `float32(0.9137)` score
to `0.91` through `floatPointerValue(..., 'f', 2, 32)`
(`inventory.go:887-894`). A form submission silently changes that candidate
evidence even when the reviewer did not edit it.

Required correction: use field-specific bounded parsing/serialization whose
maximum accepted representation covers every candidate the normalized model
accepts, and serialize score losslessly. Regress boundary-size season/anime
packs plus non-two-decimal scores through initial render, form submission,
reload, and back link.

### R5a — P2 — Coverage window timestamps are discarded and not validated

The public `Coverage` contract includes `startedAt` and `completedAt`, and the
HTTP specification names both as principal coverage evidence
(`docs/specs/spec-001-media-reconciliation/http-api.md:91`). The normalized UI
`Coverage` type omits both fields (`inventory.go:147-157`),
`convertCoverage` drops them (`convert.go:419-444`), and coverage rendering
cannot display them (`inventory.go:1296-1314`).

One loopback response supplied valid distinct start, completion, and observed
times. The reader returned success, but neither coverage-window timestamp
appeared in the rendered descriptor page. A second response supplied explicit
`startedAt: "0001-01-01T00:00:00Z"`; it also returned success because the
present-but-zero timestamp was discarded before semantic validation. Other
optional timestamps in this package correctly reject present zero values.

This leaves the R3/R5 claim of complete, strict coverage evidence false and
removes information needed to assess a catalog observation window. Preserve,
validate, and render both optional timestamps with explicit unknown states;
cover page and item coverage in list and detail responses.

### R5b — P2 — Dynamic draft query grammar accepts unknown and foreign controls

`allowedDetailInput` accepts every alphanumeric key beginning `association-`
or `candidate-` (`inventory.go:659-670`). It does not require a numeric index,
a known field suffix, or an index that belongs to the returned discovery.
Those unrecognized values are then preserved into back links and both forms as
hidden controls (`render.go:245-252`, `309-316`, `449-465`).

Independent requests using `candidate-x-kind`, `candidate-0-unknown`,
`association-x-role`, `association-0-unknown`, and valid-looking index 999
variants all returned 200 and retained the values. This contradicts the strict
unknown-query gate and leaves authority-shaped draft state unbound to any
displayed file or candidate. Invalid values for select-backed fields similarly
receive a successful page while no option represents the accepted value.

Required correction: parse the exact dynamic-key grammar and field vocabulary,
validate enum-backed values, then bind indexes to the returned file/candidate
collections before rendering. Reject unknown, malformed, duplicate, and
out-of-range controls with a safe 4xx page.

## Closed round-one findings and positive evidence

- R1 closed: handler and `HTTPReader` reject foreign detail IDs for discovery,
  media, download, and descriptor records before rendering.
- Ordinary R2 fields close: media identity/provider/kind/selection/episode and
  subtitle fields, association fields, candidate authority fields, and list
  context survive the covered sequential GET-form flow.
- R3 closes for file observation time, provenance hash/completion, per-instance
  tracking identity/time/coverage, and download deprecated/descriptor IDs.
  R5a remains for the contract-defined coverage interval.
- R4 closed: Python `html.parser` found every representative option value and
  exactly the expected selected role and candidate kind. No malformed value
  attribute remained.
- Most R5 semantics close: generated enums, required nested fields, IDs,
  root-relative targets, bounds, optional timestamps outside coverage, strict
  JSON, duplicate keys, redirects, cancellation, and sanitized errors pass.
- Read-only GET/HEAD routing, one-page API pagination, private headers, escaped
  HTML, generated DTO containment, and UI/root architecture boundaries remain
  intact. No mutation route or live-service access was found.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits/trees, scope diff, ancestry, `git diff --check`, and clean worktree before receipt | PASS; identities above. |
| Temporary requested-vs-response identity matrix through existing handler and loopback HTTP regressions | PASS for all four detail resources. |
| Temporary full media/association/candidate draft and evidence rendering probes | PASS for ordinary bounded values; R2a fails at the valid 256-episode boundary and score precision. |
| Temporary dynamic-key matrix for malformed suffixes/indexes and index 999 | FAIL as described in R5b; every case returned 200 and was retained. |
| Temporary coverage-window loopback fixtures | FAIL as described in R5a; valid times disappeared and explicit zero `startedAt` was accepted. |
| Python `html.parser` over representative rendered controls | PASS; all eight options had values and two intended selections were present. |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s` in `ui/` | PASS; package `1.984s`. |
| `GOWORK=off go test -race ./internal/inventory -count=1 -timeout=180s` in `ui/` | PASS; package `3.129s`. |
| `GOWORK=off go test ./... -count=1 -timeout=180s` and `GOWORK=off go test -race ./... -count=1 -timeout=240s` in `ui/` | PASS. |
| `GOWORK=off go vet ./...`, `go mod verify`, and offline `go mod tidy -diff` in `ui/` | PASS; no module diff. |
| Offline generation snapshot check | PASS; `generation checks passed`. |
| Architecture-only lint, standalone boundaries, `check-architecture.py`, and `check_planning.py` | PASS; 53 tasks and 60 acceptance cases. |
| CGO-free UI builds for Linux amd64 and arm64 | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0, final `guardrail checks passed (ci)`, including root storage race `199.247s`. |

## Acceptance disposition

- A-01, A-08, A-09, and A-11: corrected U-02 contribution passes the scoped
  probes; end-to-end closure remains with their other lanes.
- A-07: not accepted for U-02 while coverage start/completion evidence is
  discarded.
- A-10 and A-49: not accepted because a valid exact episode set and candidate
  score do not survive the UI's own draft round-trip.
- Strict query/protocol gate: not accepted because unknown dynamic controls are
  treated as successful draft state.

## Integration recommendation

Do not mark U-02 complete. Correct R2a, R5a, and R5b, then submit the exact new
product for another independent review. Green standard gates do not exercise
the field-specific draft boundary, coverage-window retention, or dynamic query
grammar above.
