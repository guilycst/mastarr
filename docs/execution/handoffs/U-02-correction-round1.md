# U-02 correction round one handoff

## Identity

- Task: U-02 correction round one, discovery, inventory and matching views.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base and dispatch checkpoint: `bc86603258fbcbbb0c284fcabba4dcd515a80510`.
- Prior product: `0e0e66d16c4211e20d4b773262165f7e5b4e3dc9`.
- Prior handoff: `f832b664104f2f0d07523a15cf019bd87cbf793a`.
- Review receipt: `c4ea224451e2fd96e6128745a82b2adc8a261106`.
- Product commit: `0da6087590d1cc4a426da9a449fa61ea3d4b913c`.
- Handoff commit: this file, committed separately after the product.
- Owned product path: `ui/internal/inventory/`.

## Corrections

### R1 — exact detail identity binding

The normalized handler now compares every detail response ID with the
requested route ID before validation or rendering. `HTTPReader` performs the
same comparison immediately after strict decoding for discoveries, media,
downloads and descriptors. A foreign valid response is therefore a sanitized
protocol failure and cannot alter a detail form action or displayed identity.
Synthetic handler and loopback HTTP tests cover all four resource kinds.

### R2 — complete GET draft round-trip

Media forms preserve identity, provider ID, kind, selection, episode, subtitle
language, forced, SDH and pair values. Discovery file-association and candidate
forms preserve each other's accepted fields and the list cursor, limit and
root context. Candidate kind, season, episodes, year, score and authority IDs
are rendered, and every detail back link retains accepted draft state. Hidden
controls carry the other form family's values through each GET submission;
values remain escaped and are never overwritten by observed API values.

### R3 — evidence visibility and unknown states

Discovery detail now renders file observation time, provenance hash and
completion time, and complete coverage evidence: completeness, connection,
root and source IDs, observation time, observed count, snapshot revision and
reason codes. Media tracking renders per-instance external ID, observation
time and coverage ID alongside its connection and dimension. Download detail
renders deprecated and descriptor IDs plus coverage. Missing optional evidence
is displayed as `unknown`; filesystem and client timestamps remain separate.
List pages also render their page coverage observations.

### R4 — valid option markup

Media-kind filters and file-role selectors close every `value` attribute before
conditionally adding `selected`. The regression checks every option and the
selected value in both controls.

### R5 — strict nested semantic validation

Conversion and normalized validation now enforce generated enum membership,
required candidate kind/title, bounded provider/external/evidence values,
valid identities, root-relative targets and optional timestamp presence across
files, provenance, candidates, coverage, tracking, downloads and descriptors.
Malformed nested list/detail fixtures fail with sanitized protocol errors;
unknown or incomplete evidence cannot render as a successful page.

The lane remains read-only and HTTP-only. No API contract, generated client,
root internals, database, upstream, media path, mutation, credential or live
service changes were made.

## Verification

Commands were run from the correction product tree with the stated environment
where shown.

| Check | Result |
| --- | --- |
| `gofmt -w ui/internal/inventory/*.go` | PASS |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=120s` in `ui/` | PASS |
| `GOWORK=off go test -race ./internal/inventory -count=1 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go vet ./internal/inventory` in `ui/` | PASS |
| `GOWORK=off go test ./... -count=1 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1 -timeout=240s` in `ui/` | PASS |
| `GOWORK=off go vet ./...` in `ui/` | PASS |
| `GOWORK=off go mod verify` in `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` in `ui/` | PASS; no manifest diff |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS |
| `GOWORK=off ./scripts/check-lint.sh --architecture-only` | PASS; architecture and client boundaries |
| `python3 scripts/check-architecture.py` | PASS |
| `python3 scripts/check_planning.py` | PASS; 53 tasks and 60 acceptance cases |
| `git diff --check` and staged commit checks | PASS |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; final `guardrail checks passed (ci)`, including generation, six-contract Vacuum, lint, architecture, root/UI/client ordinary and race tests, vet, module verification and Linux amd64/arm64 CGO-free builds |

The full aggregate was run after the product commit from a clean worktree and
completed successfully. Synthetic regressions use only in-memory readers and
loopback `httptest` servers. Browser-level composition remains a later UI
integration gate outside this owned package.
