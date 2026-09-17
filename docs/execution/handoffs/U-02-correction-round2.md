# U-02 correction round two handoff

## Identity

- Task: U-02 correction round two, discovery, inventory and matching views.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base and dispatch checkpoint: `ab42ce0dd43eb4bfc5145b3d6549ec7463928684`.
- Prior product: `0da6087590d1cc4a426da9a449fa61ea3d4b913c`.
- Prior correction handoff: `f8a087ce1a03b502b630fdd644d74fd72de423f5`.
- Review receipt: `f9b4765d5e749232b9ccd237beb3984fc3986672`.
- Product commit: `6bd6c9c188074ec8c5e05b33f3723f39aad16927`.
- Handoff commit: this file, committed separately after the product.
- Owned product path: `ui/internal/inventory/`.

## Corrections

### R2a — candidate draft boundary and score precision

Candidate episode drafts now use a field-specific bound sized for all
non-negative `int` values and the full `MaxAssociationInputs` (256) episode
count, rather than inheriting the 240-byte scalar bound. The parser validates a
canonical numeric comma-separated grammar, count, and integer range while
retaining order and the original submitted text. A boundary season-pack
regression renders, submits, and reloads all 256 episodes with list context
intact.

Candidate score fallback rendering uses shortest lossless float32 formatting
instead of two-decimal rounding. A score such as `0.9137` therefore survives
the initial render, GET submission and reload without changing observed
precision. Invalid score values remain rejected.

### R5a — coverage windows

Normalized coverage now retains optional `StartedAt` and `CompletedAt` values.
Conversion and normalized validation reject present zero timestamps while
allowing absent values. Page and item coverage render both fields with
explicit `unknown` when absent, alongside completeness, scope IDs, observed
count/time, snapshot revision and reason codes. Synthetic HTTP and handler
regressions cover page windows, item windows, absent windows and present-zero
rejection.

### R5b — strict dynamic draft grammar and binding

Dynamic association and candidate controls now accept only the exact known
field vocabulary and a canonical numeric index. Candidate kind, season/year,
episode lists and score, plus association role and boolean fields, receive
field-specific validation. Malformed suffixes, nonnumeric or leading-zero
indexes, unknown fields, invalid enums, duplicate values and oversized input
are rejected with sanitized 4xx responses. Valid keys are bound to the
returned discovery file/candidate counts before rendering; valid-looking
indexes absent from the response are rejected with 400 and cannot become
hidden state. Dynamic controls are rejected outside discovery details.

All prior corrections remain in place: exact requested/detail identity
binding, full draft-family/list-context round-trip, evidence visibility and
unknown states, valid option markup, strict nested API validation, escaped
private no-store/noindex HTML, GET/HEAD-only routing and no mutation paths.
No API contract, generated client, root internals, database, upstream, media
path, credential or live-service files were changed.

## Verification

Commands were run from the product tree with the stated working directory and
environment.

| Check | Result |
| --- | --- |
| `gofmt -w internal/inventory/*.go` in `ui/` | PASS |
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
| `git diff --check` and product staged commit checks | PASS |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; final `guardrail checks passed (ci)`, including generation, six-contract Vacuum, lint, architecture, root/UI/client ordinary and race tests, vet, module verification and Linux amd64/arm64 CGO-free builds |

The synthetic regressions use in-memory readers and loopback `httptest`
servers only. No browser-level composition or live-service behavior is claimed
by this package; those remain later integration gates.
