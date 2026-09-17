# U-02 correction round four handoff

## Identity

- Task: U-02 correction round four, discovery, inventory and matching views.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Correction base and dispatch checkpoint: `6748085db3cd6dd5be9155ccd7d7a0b544300f12`.
- Prior product: `d832964b35cf2cfe2adf2b39d91e62c2f46bd745`.
- Prior correction handoff: `90d6238dfd408f3e599d7450bb5bac6a300d17d9`.
- Review receipt: `1683d40afd2c8364bab511f4009ebf609fe6be59`.
- Product commit: `6c3b1d819601fedd14b2c080b84bc5632e01b494`.
- Handoff commit: this file, committed separately after the product.
- Owned product path: `ui/internal/inventory/`.

## Correction

### R5c-a — transport-safe rendered-form bounds

The inventory contract now fits every accepted maximum collection form through
the existing production `net/http` server with its default request-header
limit. The normalized/rendered scalar bound is 128 bytes, the accepted
discovery collection ceilings are 257 files and 33 candidates, and the
derived dynamic control bound is 2,063 fields. The encoded query limit is
768 KiB, leaving room below the default 1 MiB header budget for the request
path and headers. Candidate episode lists retain the separate 256-entry
bound and are included in the transport-size calculation.

The direct handler regression submits every association and candidate control
at the 257-file/33-candidate boundary, including the final indexes. A second
regression sends the escaped maximum-value form through a real default
`httptest.NewServer`; it receives HTTP 200 and increments the fake reader,
proving the request reached the handler rather than being rejected with HTTP
431. Both checks preserve reload, list context and stable control ordering.

All earlier corrections remain in place: exact requested/detail identity
binding, complete candidate/association/media draft round-trip, 256-episode
and score precision, coverage window retention and zero-time rejection,
strict dynamic grammar and response-index binding, evidence/unknown
rendering, valid option markup, route-scoped fixed draft vocabulary, escaped
private no-store/noindex HTML, GET/HEAD-only routing and no mutation paths.
No `ui/cmd`, API contract, generated client, root internals, database,
upstream, media path, credential or live-service files were changed.

## Verification

Commands were run from the product tree with the stated working directory and
environment.

| Check | Result |
| --- | --- |
| `cd ui && gofmt -w internal/inventory/*.go` | PASS |
| `GOWORK=off go test ./internal/inventory -count=1 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go test -race ./internal/inventory -count=1 -timeout=240s` in `ui/` | PASS |
| `GOWORK=off go vet ./internal/inventory` in `ui/` | PASS |
| `GOWORK=off go test ./... -count=1 -timeout=240s` in `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` in `ui/` | PASS |
| `GOWORK=off go vet ./...` in `ui/` | PASS |
| `GOWORK=off go mod verify` and `GOWORK=off go mod tidy -diff` in `ui/` | PASS; verified modules and no manifest diff |
| Product commit staged hook | PASS; generation/API/Vacuum, architecture, root/UI checks and fast guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; final `guardrail checks passed (ci)`, including generation, six-contract Vacuum, lint, architecture, all module ordinary/race/vet checks, module verification and Linux amd64/arm64 CGO-free builds |
| `git diff --check` | PASS |

The synthetic regressions use in-memory readers, temporary values and a
loopback `httptest` server only. Browser-level composition and live-service
behavior remain outside this package's verification scope.
