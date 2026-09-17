# U-01 independent review round two

## Decision

`approved`.

The combined product/support tree
`3e93e80503682f7c13de5a65a3a502af046b9de3` closes all five round-one P2
findings. Every non-GET/HEAD method is rejected before asset dispatch, invalid
readiness states stay unavailable, the BFF uses the typed env/v11 contract and
supports local HTTP origins without weakening URL validation, startup
diagnostics are actionable and sanitized, and the UI module is tidy. No P1 or
P2 finding remains in the reviewed U-01 scope.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; the reviewer did not author the
  correction.
- Product commit: `65a1c0e66764308ff95c2a70de10ace15712b695`.
- Product tree: `16e87587b674716627ee517073a61cf1ea654019`.
- Module-metadata support/combined source:
  `3e93e80503682f7c13de5a65a3a502af046b9de3`.
- Combined source tree: `ca5073fc9c7322bd4a2ecc3be4c40380cdfdd4d5`.
- Correction handoff commit:
  `f397aaf326ae18615e01d4d86be764da5502a158`.
- Clean coordinator checkpoint:
  `21c399d26c7a76fe4faf029e42d1e4bb019cc849`.
- Handoff file SHA-256:
  `618a9442a90b0468352200ed15fdc855786b035bf85c6c818ec034833b1fa845`.
- Corrected UI diff SHA-256:
  `3610ed32fd9fb21e1e2abbaa79e8370a15327b68c1b1ef11ae76129b8ce56540`.
- Git proves product -> handoff -> support -> checkpoint ancestry. The UI tree
  is byte-for-byte unchanged from the combined source through the checkpoint.
- Reviewed product scope: `ui/internal/client/`, `ui/internal/shell/`,
  `ui/internal/config/`, `ui/cmd/`, `ui/internal/assets/`, and the coordinated
  `ui/go.mod` / `ui/go.sum` support change needed to close R3/R5.

The review used synthetic `example.test` and loopback endpoints, in-memory HTTP
requests, temporary binaries and temporary reviewer tests removed before this
receipt. It did not use live services, credentials, private coordinates, media
data or browser/U-05 evidence. This receipt is the only persistent review
change.

## Round-one finding closure

### R1 closed: method policy precedes all asset handlers

`ServeHTTP` now validates GET/HEAD immediately after the nil request check and
before `/preview.svg`, `/assets/`, or `/consoleshell/assets/` dispatch. The
product regression covers POST on all three prefixes.

An independent matrix repeated POST, PUT, PATCH, DELETE, OPTIONS, CONNECT and
TRACE against the preview and both real stylesheet paths. Every response was
`405`, carried `Allow: GET, HEAD`, contained neither the request marker nor an
asset body, and made no API call. The ordinary matrix passed 20 repetitions and
the race matrix passed 10.

### R2 closed: normalized readiness is explicitly fail-closed

The handler now switches explicitly on `StateReady` and `StateDegraded`.
Anything else keeps the preinitialized unavailable state and HTTP 503.

An independent matrix supplied empty, `unknown`, `future`, uppercase and
whitespace-suffixed readiness values with nil errors. Each returned 503,
rendered the unavailable/retry message and did not render the successful
“API is ready” text. The matrix passed 20 ordinary and 10 race repetitions.

### R3 closed: typed parsing and local-origin policy agree

The UI module now pins `github.com/caarlos0/env/v11 v11.4.1` directly.
`Load` takes one process-environment snapshot and `Parse` uses
`ParseAsWithOptions[Environment]` with the typed required/default tags before
applying the fixed validation policy. The generated root environment reference
still names the same three UI variables, and deterministic envdoc generation
passes.

The independent matrix accepted an HTTP loopback public origin and HTTPS
origin, retained API application paths, and applied the `:8081` default. It
rejected API/public-origin credentials, query and fragment data, public-origin
paths and invalid ports without echoing synthetic secret markers. Local HTTP
and HTTPS rendering both produced configured-origin canonical, Open Graph,
X/Twitter and preview metadata. The handler remains immutable after startup;
later environment changes cannot change its configuration snapshot.

### R4 closed: executable diagnostics are actionable and sanitized

The executable now includes the sanitized config error and fixed
`StartupRestartGuidance`. Independent process probes built the exact product
and ran it with isolated synthetic environments:

- Missing variables exited 1, named only `MASTARR_UI_API_URL`, and instructed
  the operator to restart the BFF after changing `MASTARR_UI_*` values.
- A URL containing synthetic userinfo, query and secret markers exited 1,
  named only `MASTARR_UI_API_URL` plus the violated rules, retained the fixed
  restart guidance, and emitted none of the configured user, URL, token or
  secret values.

### R5 closed: the UI module is tidy

The coordinator-owned module support promotes env/v11 to a direct requirement,
removes stale indirect declarations and records the pinned toolchain's tidy
sums. Independent
`GOWORK=off GOPROXY=off GOSUMDB=off go mod tidy -diff` exits 0 with no output.
`go mod verify`, read-only tests, vet, generation and the aggregate offline gate
also pass. No `go.work` or `replace` directive is present.

## Regression review

- Authored UI packages expose normalized UI types only. Generated OpenAPI DTOs
  remain internal to `ui/internal/client`; they do not leak into shell/config
  or any public Mastarr API.
- `go list -deps ./...` in `ui/` contains no Mastarr root internal, standalone
  client, SQLite, database, adapter, worker or media package. The UI module
  consumes the Mastarr API over HTTP only.
- HTTPS metadata continues through the selected Goshtoso metadata component.
  The bounded local-HTTP fallback emits the same escaped fixed-name metadata
  and retains the Goshtoso layout/runtime. It does not use Host or forwarded
  headers.
- An independent HTTP/HTTPS all-route matrix covered the ten allowlisted
  routes. Every route used the configured canonical/OG/X/preview origin,
  omitted opaque child IDs, query values, request Host and forwarded Host, and
  retained `no-store`, `noindex`, `nosniff` and `no-referrer` response policy.
- The generic 1200x630 preview, private-safe shell 404, sanitized API outage,
  bounded response reader, redirect rejection, context identity and graceful
  cancellation tests remain green.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, combined-source drift and hashes | PASS; identities above. |
| `git diff --check` for product/support and clean review worktree before receipt | PASS. |
| Independent non-GET asset matrix, `-count=20`; race `-count=10` | PASS for preview and both mounted asset handlers. |
| Independent invalid-readiness matrix, `-count=20`; race `-count=10` | PASS; every invalid state remained unavailable/503. |
| Independent typed environment and sanitization matrix, `-count=20`; race `-count=10` | PASS. |
| Independent executable startup diagnostic probes | PASS; actionable fixed text, variable name only, no configured value leakage. |
| Independent HTTP/HTTPS all-route metadata/header matrix, `-count=10`; race `-count=5` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -mod=readonly ./... -count=5 -timeout=180s` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race -mod=readonly ./... -count=3 -timeout=300s` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go vet -mod=readonly ./...` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` in `ui/` | PASS; all modules verified. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod tidy -diff` in `ui/` | PASS; exit 0, no diff. |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; generation checks passed. |
| `./scripts/check-lint.sh --architecture-only` | PASS; root/UI and standalone-client boundaries passed. |
| `python3 scripts/check_planning.py` | PASS; 53 tasks, 60 acceptance cases, local links resolve. |
| Linux amd64 and arm64 CGO-free `-mod=readonly` UI builds | PASS; static ELF x86-64 and aarch64 binaries. |
| Full offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 and final `guardrail checks passed (ci)`. Generation, staged generation, OpenAPI/Vacuum, lint, architecture, all nine module ordinary/race/vet/verify checks and 18 Linux cross-builds passed. Root storage race completed in `197.895s`. |

## Acceptance disposition

- A-45: accepted for U-01. Generated output, envdoc, module isolation, clean
  tidy, read-only tests, race, vet, module verification, lint, architecture and
  both Linux builds pass with `GOWORK=off` and no local replacement.
- A-46: accepted for the U-01 contribution. No application credential is
  required, no actor is invented, and request/proxy identity does not affect
  generated metadata.
- A-47: accepted for the server-side U-01 contribution. Method policy is
  fail-closed before mounted handlers, configured origin controls metadata,
  invalid readiness is not successful state, and private response headers are
  retained. Browser CSRF/form behavior remains U-05 scope and is not claimed.
- A-51: accepted for the server-side U-01 contribution. All route metadata,
  unknown-ID suppression, safe preview and useful shell 404 pass for configured
  HTTPS and local HTTP origins. Browser loading remains U-05 scope.

## Integration recommendation

Integrate this receipt and mark U-01 complete for its v0.0.1 contribution.
U-02 may build on the reviewed normalized HTTP/client/shell boundary. Keep
browser accessibility, viewport, interaction and live-origin verification in
U-05; release, container publication, deployment and live-stack behavior remain
separate gates.
