# U-01 independent review round one

## Decision

`changes_requested`.

The HTTP-only module boundary, generated-client containment, sanitized API
errors, private response headers, configured-origin metadata, safe preview,
ordinary outage handling and Linux builds pass. Five P2 findings remain in the
reviewed product: static asset routes bypass the method gate, an unknown
normalized readiness state becomes a false HTTP 200 success, the UI startup
environment implementation bypasses and already differs from the frozen
bootstrap contract, the executable discards its actionable startup error, and
the UI module is not tidy even though that gate was explicitly left to U-01.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; the reviewer did not author the
  product.
- Product: `2f86d298d890e466334ecaa6d0bd92dc4d50f64f`.
- Product parent/base: `73748f1c08f016ad4ecfdbc352462fb22d261e0f`.
- Product tree: `344f369797ba0b50e768bc9a99905390337169bf`.
- Handoff commit: `0ddb7fc75b00ec296879efca8f2b46720a81b56b`.
- Coordinator checkpoint: `41636b4851a8d08efccc84ebb5935512f07e4c44`.
- Product diff SHA-256: `6c55e9af8a922c7803cbcf8eee583391ad7f9702b16d2f7e85bed0c70c498625`.
- Handoff file SHA-256: `a21d50ed42205945d7064521e8639424c7f84c6cb54e86a714e4e5026a45837c`.
- Git proves the product is an ancestor of the checkpoint and the owned paths
  are byte-for-byte unchanged between product and checkpoint.
- Reviewed paths: `ui/internal/client/`, `ui/internal/shell/`,
  `ui/internal/config/`, `ui/cmd/`, and `ui/internal/assets/`.

The review used synthetic `example.test` endpoints, in-memory HTTP requests,
and temporary reviewer tests removed before this receipt. It did not use live
services, credentials, private coordinates, media data, a browser, or U-05
evidence. This receipt is the only persistent review change.

## Findings

### P2 R1: static assets bypass the GET/HEAD method gate

`ui/cmd/mastarr/main.go:68-78` dispatches `/assets/` and
`/consoleshell/assets/` before the method check at lines 80-84. Both upstream
asset handlers serve their content for `POST`, contradicting the U-01 handoff's
claim that the BFF rejects mutation methods and leaving method behavior
dependent on mounted handlers.

An independent temporary test called `NewHandler` with two synthetic requests:

- `POST /assets/styles.css` returned `200`, `text/css`, and the stylesheet.
- `POST /consoleshell/assets/shell.css` returned `200`, `text/css`, and the
  stylesheet.

The test required `405` and failed for both paths. Move the method gate before
all route dispatch, or mount the assets behind an explicit GET/HEAD wrapper,
and add regressions for each mounted asset prefix plus `/preview.svg`.

### P2 R2: unknown readiness evidence is reported as ready

`ui/cmd/mastarr/main.go:107-115` treats every successful `Reader.Ready` result
other than `StateDegraded` as available. A synthetic `Reader` returning
`ReadinessState("future")` and no error produced HTTP `200` and rendered “The
API is ready.” The independent probe required unavailable/503 and failed.

The `Reader` interface is the normalized trust boundary. Switch explicitly on
`StateReady` and `StateDegraded`; empty, unknown, future, or otherwise invalid
states must remain unavailable. Add direct handler regressions for each invalid
state so later reader implementations cannot create false-success UI.

### P2 R3: the UI environment parser is a second, drifting bootstrap contract

The frozen configuration specification requires parsing once with
`caarlos0/env/v11` and generating the reference from the typed definition with
`g4s8/envdoc`. `ui/internal/config/config.go:46-63` instead declares an unused
tagged `Environment`, manually calls `os.LookupEnv`, and manually maintains a
second validation implementation. The UI module has neither environment
dependency. The handoff explicitly defers the prescribed parser/generator as a
coordinator follow-up, so the claimed contract is incomplete.

There is already observable drift. An independent root bootstrap probe accepted
`MASTARR_UI_PUBLIC_ORIGIN=http://localhost:8081` and passed `ValidateUI`, which
matches the specification's local-development allowance. The UI parser's
existing `origin http` case rejects the same value. Both tests pass separately,
proving the generated root contract and actual BFF startup do not agree.

Use one typed UI environment definition parsed with `env/v11`, wire it into the
pinned envdoc workflow owned by `tools`, and make the independently versioned
UI module's runtime validation agree with the generated reference. A frozen
module manifest is not a reason to defer a required U-01 dependency change;
coordinate that owned-file change before integration.

### P2 R4: startup failure removes the actionable validation evidence

`config.Parse` returns sanitized errors containing the failing variable, but
`ui/cmd/mastarr/main.go:24-29` discards the error and logs only `mastarr UI
configuration is invalid`. Building and running the exact product with an
empty synthetic environment exited `1` and emitted only:

```text
mastarr UI configuration is invalid
```

It did not identify `MASTARR_UI_API_URL` or provide the fixed restart guidance
claimed by the handoff. The shell's restart note cannot help because the server
never starts. Preserve the sanitized variable-specific reason and fixed
restart instruction while continuing to omit configured values.

### P2 R5: U-01 leaves the UI module manifest untidy

`GOWORK=off GOPROXY=off GOSUMDB=off go mod tidy -diff` in `ui/` exits `1`.
The diff changes `ui/go.mod` grouping and removes unused indirect requirements
for `github.com/oapi-codegen/nullable` and `github.com/stretchr/testify`; it
also normalizes committed sums to the pinned toolchain's tidy result. U-00's
approved round-two receipt explicitly left “clean module tidy” to U-01 after
authored/generated packages consumed the pins. U-01 did not perform that gate
and its handoff treats the manifests as frozen instead.

Run tidy with the pinned toolchain, review the dependency delta, commit the
result through the coordinator-owned manifest path, and require a subsequent
`go mod tidy -diff` exit `0`. `go mod verify` and the aggregate guardrail pass do
not establish this condition because neither gate checks tidy reproducibility.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact ancestry, tree, owned-path drift, product diff and handoff hashes | PASS; identities above. |
| Product `git diff --check`; review worktree cleanliness before receipt | PASS. |
| Temporary `POST` asset-prefix method probe | FAIL as described in R1; both mounted stylesheets returned 200. |
| Temporary unknown-readiness handler probe | FAIL as described in R2; future state returned 200 and ready text. |
| Root bootstrap versus UI local-HTTP-origin probe | Contract drift reproduced as described in R3; temporary source removed. |
| Built executable with empty synthetic environment | Expected startup failure exit 1; only generic error emitted, reproducing R4. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test ./... -count=5 -timeout=180s` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go test -race ./... -count=3 -timeout=300s` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go vet ./...` in `ui/` | PASS. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod verify` in `ui/` | PASS; `all modules verified`. |
| `GOWORK=off GOPROXY=off GOSUMDB=off go mod tidy -diff` in `ui/` | FAIL/non-clean as described in R5. |
| Independent all-route initial metadata probe, five repetitions | PASS; every allowlisted route used configured-origin canonical/OG/X metadata and did not reflect Host, forwarded host, query, or opaque child identity. |
| Independent header matrix for shell, 404, preview and both asset prefixes | PASS; noindex, nosniff and no-referrer were present. Shell/404 were no-store; public-safe assets used their explicit public cache policy. |
| Generated DTO boundary and dependency/import scan | PASS; authored UI packages expose normalized UI types only and import no root internal, database, upstream client, or media package. No `go.work` or local `replace`. |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS; generation checks passed. |
| `./scripts/check-lint.sh --architecture-only` | PASS; root/UI and standalone-client boundaries passed. |
| `python3 scripts/check_planning.py` | PASS; 53 tasks, 60 acceptance cases, local links resolve. |
| Linux amd64 and arm64, CGO-free, `-mod=readonly` build of `ui/cmd/mastarr` | PASS; ELF x86-64 and aarch64 binaries. |
| Full offline `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 and final `guardrail checks passed (ci)`. Generation, staged generation, OpenAPI/Vacuum, lint, architecture, all module ordinary/race/vet/verify checks and 18 Linux cross-builds passed. Root storage race completed in `198.337s`. |

## Acceptance disposition

- A-45: not accepted for U-01. Generation, module isolation, verification,
  architecture, lint and cross-builds pass, but the explicitly deferred UI
  tidy gate is non-clean and the typed environment contract is not the one
  actually parsed by the BFF.
- A-46: accepted for the U-01 contribution. The shell requires no application
  credentials, does not invent an actor, and does not trust Host or forwarded
  headers for metadata.
- A-47: not accepted for U-01 while mounted asset routes bypass the BFF method
  policy and unknown readiness evidence becomes successful UI state. The
  configured-origin and private header checks otherwise pass. Browser mutation
  and CSRF behavior remains U-05 scope and is not claimed here.
- A-51: accepted for the server-side U-01 contribution. All allowlisted initial
  routes, private-safe canonical/OG/X metadata, useful 404, and the generic
  1200x630 SVG preview passed synthetic checks. Browser loading remains U-05.

## Required correction and next review

Correct R1-R5 atomically enough to keep each checkpoint buildable. Add
regressions for asset method handling, invalid readiness states, executable
startup diagnostics, and one authoritative env contract. Tidy and verify the
UI module, regenerate/check env documentation, rerun the focused ordinary/race
matrix and full offline guardrail, then request a second independent review.
Do not unblock U-02 from this receipt.
