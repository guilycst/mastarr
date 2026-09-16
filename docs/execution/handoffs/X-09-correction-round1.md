# X-09 correction round one: version-bound Jellyfin refresh

## Assignment

- Task ID and title: X-09 correction round one, bind Jellyfin refresh to observed runtime compatibility.
- Owner and independent reviewer: `/root/x05_implementer`; `/root/x05_reviewer`.
- Correction dispatch integration: `2535a1200392337eae1735acd200cd4ff47b9d6d`; dispatch state record `1ee282c`.
- Review receipt addressed: `4f611b5a988c89b73143a05ff792763de091edbb`.
- Product base: `1ee282ce48f4c53ca5158b26d21f266884be24e3`.
- Branch/worktree: shared `main` checkout; coordinator owns `docs/execution/state.json`.
- Owned paths: `internal/adapters/jellyfin/write/` and this handoff only.
- Required acceptance: A-33 and A-55, with refresh acceptance kept separate from later availability.

## Correction

The prior adapter claimed `jellyfin.refresh.library` supported for every client and
could send `POST /Library/Refresh` without observing the connected Jellyfin
runtime. The correction removes the fictive static compatibility version and adds
an explicit runtime fence:

- `Config.ExpectedVersion` is required for the library mutation gate.
  `Config.ExpectedProduct`, when supplied, is an additional exact product fence.
  There is no default release claim.
- `Capabilities` performs a read-only `GET /System/Info/Public` through the
  standalone Jellyfin client. It returns library `supported` only when the
  observed version matches the configured expected version and any configured
  product matches exactly. It returns `unknown` for absent/unknown/unavailable
  runtime evidence and `unsupported` for an observed mismatch.
- `Refresh` repeats the fresh version observation immediately before any native
  mutation. Missing configuration, missing or unknown version, unavailable
  version evidence, and version/product mismatch return a sanitized failure with
  zero `POST /Library/Refresh` calls.
- A matching observation permits the existing native library refresh request and
  preserves the result as request acceptance only. Neither capabilities nor the
  refresh result claims scan completion or later Jellyfin availability.
- Item refresh remains explicitly unsupported and performs no native request.
- Capability evidence no longer contains `refresh_request_accepted` for a
  capability read that did not perform a refresh. Matching capability evidence
  identifies the observed system-info route and runtime match; blocked evidence
  identifies the compatibility gate.
- The adapter continues to use only normalized public standalone-client values,
  preserves context cancellation/deadline identity, and keeps upstream endpoint,
  token, body and transport details out of root errors.

## Changed paths and exact product commit

- `internal/adapters/jellyfin/write/client.go`
- `internal/adapters/jellyfin/write/client_test.go`
- Product commit: `ff3d7d55490435c9b99d38ad97edd3b2f27f9df5`

The standalone Jellyfin client already exposes the required sanitized
`GetSystemInfo` version observation. A broad live release compatibility claim is
still intentionally absent: an operator must provide the expected runtime fence,
and the adapter stays blocked when it is omitted. The matching test is a
synthetic `httptest` observation of Jellyfin `10.10.7`; it is not evidence about a
live deployment.

## Regression evidence

`client_test.go` uses only synthetic HTTP fixtures. The fixture records every
request and serves `/System/Info/Public` independently from `/Library/Refresh`.

- `TestRefreshVersionGateBlocksUnverifiedRuntime` covers missing `Version`, the
  native `Version: "unknown"` sentinel, an unavailable system-info response, a
  mismatched version, and an omitted configured fence. Every blocked refresh
  performs zero native POST requests; the tests also assert the capability state
  (`unknown` or `unsupported`) and expected read-only observation count.
- `TestMatchingRuntimeObservationEnablesOnlyLibraryRefresh` proves that an exact
  observed version yields a supported library capability, keeps capability
  evidence free of request acceptance, and orders the fresh system-info read
  before the library POST.
- `TestLibraryRefreshMapsAcceptanceAndSeparatesAvailability` was updated to
  prove the matching preflight and preserve acceptance/availability separation.
- Existing item-scope, input-validation, status-normalization, sanitation,
  cancellation and deadline tests remain active. The success/error fixtures now
  provide the explicit matching runtime fence required to reach the native POST.

## Verification

All checks ran from product commit `ff3d7d55490435c9b99d38ad97edd3b2f27f9df5`.
Offline commands used `GOWORK=off GOPROXY=off GOSUMDB=off` where shown; no live
service or private media data was used.

| Check | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly -race -count=3 -timeout=180s ./internal/adapters/jellyfin/write` | PASS / exit 0 |
| `GOWORK=off go vet -mod=readonly ./internal/adapters/jellyfin/write` | PASS / exit 0 |
| `GOWORK=off go test -mod=readonly -race -count=2 -timeout=180s ./...` in root | PASS / exit 0 |
| `GOWORK=off go vet -mod=readonly ./...` in root | PASS / exit 0 |
| `GOWORK=off go mod verify` in root | PASS: all modules verified |
| `GOWORK=off go test -mod=readonly -race -count=2 -timeout=180s ./...` in `clients/jellyfin` | PASS / exit 0 |
| `GOWORK=off go vet -mod=readonly ./...` in `clients/jellyfin` | PASS / exit 0 |
| `GOWORK=off go mod verify` in `clients/jellyfin` | PASS: all modules verified |
| `GOWORK=off GOPROXY=off GOSUMDB=off go generate ./... && ./check-generation.sh` in `clients/jellyfin` | PASS: Jellyfin generation checks passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --fast` | PASS: generation, staged generation, Vacuum/API, architecture and targeted guardrails |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-lint.sh --architecture-only` | PASS: root and standalone import boundaries |
| `python3 scripts/check_planning.py` | PASS: 53 tasks, 60 acceptance cases; local links resolve |
| Root `CGO_ENABLED=0 GOWORK=off GOOS=linux GOARCH=amd64/arm64 go build -mod=readonly ./...` | PASS for amd64 and arm64 |
| `clients/jellyfin` `CGO_ENABLED=0 GOWORK=off GOOS=linux GOARCH=amd64/arm64 go build -mod=readonly ./...` | PASS for amd64 and arm64 |
| Product pre-commit hook | PASS: generation, staged generation, all standalone Vacuum contracts, architecture, targeted tests and fast guardrails |

## Review and integration

- Review source and finding: the prior review required a version-bound native
  observation before capability enablement or mutation; it also required zero-POST
  missing, unknown and mismatched regressions.
- Product correction commit: `ff3d7d55490435c9b99d38ad97edd3b2f27f9df5`.
- Independent correction review: pending `/root/x05_reviewer`.
- Coordinator integration and state record: pending; the coordinator owns state.
- No live mutation, release, deployment or authentication change was performed.

## Resume checkpoint

- Product and tests are complete at `ff3d7d55490435c9b99d38ad97edd3b2f27f9df5`.
- The handoff is the next separate commit.
- The adapter has no implementation blocker. Runtime enablement remains
  explicitly configuration-bound: without `ExpectedVersion`, it returns unknown
  and dispatches no POST; no synthetic fixture is treated as live release proof.
- Next safe action: coordinator records the product and handoff SHAs in
  `docs/execution/state.json` and dispatches the independent correction review.
