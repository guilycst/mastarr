# X-09 independent review, round one

Decision: **changes_requested**. One P1 capability/write-enablement finding
remains. The adapter otherwise preserves the standalone client boundary,
validation, error sanitation, unsupported item scope, and the distinction
between refresh acceptance and later availability.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`; independent of product authorship.
- Exact product commit: `38f4999910b01097b55ee1f09dd463bc9add981b`.
- Exact product tree: `d6027e32a1af551949cd62c2ccfb3e682363b5bf`.
- Exact handoff commit: `4ca6fbed07cc1766cc620f180e9a58ad80927de5`.
- Coordinator integration reviewed: `f2bf30f97068f98606251b668528afd18e479e3f`.
- Reviewed paths: `internal/adapters/jellyfin/write/`, the frozen root ports,
  `clients/jellyfin` public contract, root `go.mod`/`go.sum`, A-33/A-55, and the
  compatibility evidence that controls runtime enablement.
- Product paths, execution state, generated output, and the standalone module
  were not edited. All mutation fixtures were synthetic.

`git diff --exit-code 38f4999 f2bf30f -- internal/adapters/jellyfin/write`
passed, so the integrated tree contains the exact product adapter. The root
manifest resolves the public module
`github.com/guilycst/mastarr/clients/jellyfin` at
`v0.0.0-20260915195508-4eee8e84eff5`, with no local `replace` or `go.work`.

## Finding

### R1 - P1: an unversioned, unobserved Jellyfin runtime is declared supported and receives a write

`Client.Capabilities` returns `jellyfin.refresh.library` as `supported` for
every constructed client without reading the configured server's product or
version. It labels the static adapter contract
`jellyfin-refresh-compat-0.0.1` as the capability version and includes
`refresh_request_accepted` even though the capability call intentionally makes
no native request. `Client.Refresh` then sends `POST /Library/Refresh` without
requiring any independently established runtime compatibility evidence.
A non-nil zero-value client also reaches the same supported capability result
without an upstream client, because this path checks only `client == nil`.

This contradicts the controlling evidence:

- `docs/research/compatibility-matrix.md` records `CAP-JELLYFIN-REFRESH` as
  `UNKNOWN` and says X-09 is blocked until an endpoint/version test exists.
- The independently approved X-17 receipt leaves native release pins and
  refresh capability fixtures open as separate runtime gates.
- `clients/jellyfin/README.md` says no specific Jellyfin release is pinned and
  runtime capability enablement remains an integration gate.
- `domain.Capability` is intentionally three-valued so unverified support is
  not presented as available. A-55 requires unsupported refresh scope to stay
  blocked; a synthetic HTTP status shape does not establish deployment/runtime
  support.

Independent exact-tree reproduction:

1. Construct the adapter against an `httptest` server that exposes only
   `POST /Library/Refresh`; any system/version read is counted separately.
2. Call `Capabilities` for that connection. It returns library refresh as
   `supported` while the version-read count remains zero.
3. Call library `Refresh`. It returns accepted and the fixture records one POST,
   still with zero version reads.
4. Run the probe three times under `-race`; all repetitions reproduce the
   unsupported enablement.

Command:

```text
GOWORK=off go test -mod=readonly -race -count=3 \
  -run TestReviewerUnknownRuntimeIsClaimedSupportedAndMutated -v \
  ./internal/adapters/jellyfin/write
```

Result: exit 0; each repetition observed `supported`, zero runtime reads, and
one native POST. The probe lived only in an exact-product temporary archive.

Required correction: fail closed while the native gate is unknown. Report the
library refresh capability as `unknown` and reject refresh before dispatch, or
land independently reviewable versioned native evidence and bind enablement to
the configured server's observed compatible product/version. Add negative
regressions for missing, unknown, and mismatched runtime evidence with zero
POSTs. Any accepted 200/202/204 set must be justified by that same pinned native
fixture rather than only by the Mastarr-owned OpenAPI shape. Do not label a
preflight capability observation as an accepted refresh request.

## Passing boundaries

- Library acceptance results contain only acceptance evidence and never claim
  item visibility, scan completion, or mapping correctness. Later Jellyfin
  reads remain separate.
- Valid and malformed item refresh requests remain unsupported/invalid before
  native dispatch. Wrong connection, unknown scope, library item IDs, nil
  context, and already-cancelled context are rejected before a write.
- The adapter performs no automatic retry. The durable executor remains
  responsible for treating a handler error after dispatch as uncertain and
  reconciling before any retry; no end-to-end A-33 closure is claimed here.
- Context cancellation/deadline identity is preserved. Typed HTTP failures map
  to the root sanitized vocabulary without endpoint, token, response body, or
  transport detail.
- Only normalized public `clients/jellyfin` types cross the root adapter
  boundary. No generated DTO, root type, or other client enters the standalone
  module. No Seerr write surface or live-service fixture exists in this change.
- Root module integration uses the exact public Jellyfin pseudo-version and its
  sums. No local replacement or workspace dependency is present.

These passing properties do not make the current unconditional library write
safe while the explicit runtime capability gate remains unknown.

## Independent checks

All commands below ran from the exact integrated checkout with `GOWORK=off`.
Offline generation/guardrail commands also set `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Focused adapter `go test -mod=readonly -race -count=3 -timeout=180s` and `go vet` | Passed; does not cover R1. |
| Independent unknown-runtime write probe, `-race -count=3` | Passed as a reproduction: support claimed with zero version reads and one POST each time. |
| Root `go test -mod=readonly -timeout=180s ./...` and `go vet -mod=readonly ./...` | Passed; storage completed in 11.931s. |
| Root `go mod verify` | Passed: all modules verified. |
| Jellyfin module `go test -mod=readonly -race -count=2 -timeout=180s ./...`, `go vet`, `go mod verify` | Passed. |
| Jellyfin offline `go generate ./...` and `./check-generation.sh` | Passed; committed generated output remained clean. |
| `./scripts/check-guardrails.sh --fast` offline | Passed: generation, staged generation, root and standalone Vacuum, API, architecture, and targeted tests. |
| `./scripts/check-lint.sh` offline | Passed: zero issues across all nine modules; architecture and standalone isolation passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolve. |
| Root and Jellyfin Linux `amd64`/`arm64`, `CGO_ENABLED=0 go build -mod=readonly ./...` | Passed for all four builds. |
| `git diff --check`, generated-output diff, product-scope diff | Passed; review checkout was clean before this receipt. |

The full `--ci` aggregate was not repeated; C-06 owns that evidence. Passing
mechanical checks do not exercise the runtime capability contradiction.

## Acceptance and integration recommendation

- A-55: **not accepted for X-09**. Acceptance remains separate from later
  availability, but the library mutation is enabled while its native runtime
  capability is explicitly unknown.
- A-33: the adapter makes one call and preserves errors, but full lost-response
  recovery belongs to the durable handler/executor integration and is not
  closed by this product.

Do not integrate X-09 as an enabled write adapter. Correct R1, retain item scope
as no-dispatch, and request another independent review at exact product and
handoff SHAs. This receipt does not approve release, deployment, live Jellyfin
mutation, or any Seerr write.
