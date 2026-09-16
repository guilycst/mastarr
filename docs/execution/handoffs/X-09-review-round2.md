# X-09 correction round one independent review

Decision: **approved**. The round-one P1 is closed in the reviewed correction.
No P1 or P2 finding remains for the X-09 adapter scope.

## Review identity and scope

- Reviewer: `/root/x05_reviewer`; independent of the product correction.
- Exact product commit: `ff3d7d55490435c9b99d38ad97edd3b2f27f9df5`.
- Exact product tree: `967e136ec53baae52fe9fedeff1c1c269e0d2afe`.
- Exact correction handoff: `c2065ab1ce2cb0cfb6e5dbac61b6768028acf257`.
- Exact review-dispatch checkpoint: `5b6df7d1f344a52ac85f05bb6dad10ca16cf087a`.
- Prior independent receipt: `4f611b5a988c89b73143a05ff792763de091edbb`.
- Reviewed product paths: `internal/adapters/jellyfin/write/`; root ports and
  the public `clients/jellyfin` boundary were inspected for compatibility.

`git diff --exit-code ff3d7d5 5b6df7d -- internal/adapters/jellyfin/write`
passed. The current integrated tree therefore contains the exact product under
review. Product files, generated output, state, and the standalone module were
not edited during review.

## Prior P1 closure

The unconditional capability and mutation path is removed.

- `Config.ExpectedVersion` is now an explicit exact fence. An absent or blank
  fence returns unknown without contacting the native endpoint. Optional
  `ExpectedProduct` adds an exact product fence.
- `Capabilities` performs a fresh `GET /System/Info/Public` and returns library
  support only when the observed version and configured product fence match.
  Missing, sentinel, malformed, unavailable, unauthorized, and mismatched
  evidence cannot produce supported.
- `Refresh` repeats the system-info observation immediately before the
  mutation. It does not reuse a prior capability result. A changed runtime
  between capability inspection and dispatch is rejected with zero POSTs.
- Only an exact fresh match reaches `POST /Library/Refresh`. Item refresh stays
  unsupported and makes no system-info or mutation request.
- Capability evidence no longer contains `refresh_request_accepted`. Supported
  capability evidence records the observed compatibility match and the native
  route. Acceptance evidence appears only after the POST returns an accepted
  native status.
- Refresh acceptance still contains no availability, scan-completion, or path
  mapping claim. Those remain later read observations.

The producer regressions cover missing and `unknown` versions, an unavailable
system endpoint, version mismatch, absent configured fence, exact-match request
ordering, and zero POSTs on every blocked path. Direct inspection confirms the
root adapter consumes only normalized public client observations; generated
Jellyfin DTOs do not cross the module boundary.

## Independent adversarial probes

Reviewer-only tests were added to an exact-checkpoint temporary archive and
were run five times under `-race`.

1. The first system observation matched and made the capability supported. The
   second observation, immediately before refresh, returned a different
   version. The adapter returned `OutcomeUnsupported`; the request log contained
   exactly two GETs and no POST.
2. A matching version with a mismatched product returned unsupported for both
   capability and refresh, with zero POSTs.
3. Malformed system JSON returned unknown capability and `OutcomeUnknown` on
   refresh, with zero POSTs.
4. HTTP 401 on system information returned unknown capability and preserved
   `OutcomeUnauthorized` on refresh, with zero POSTs.
5. Every capability result was checked for absence of the fabricated
   `refresh_request_accepted` token.

Command:

```text
GOWORK=off go test -mod=readonly -race -count=5 -run TestReviewer -v \
  ./internal/adapters/jellyfin/write
```

Result: exit 0 for all repetitions. All servers were synthetic `httptest`
fixtures; no live Jellyfin or media stack was contacted.

## A-33 and A-55 boundaries

- A-55's X-09 contribution is accepted. Capability and refresh use fresh
  runtime evidence, unsupported item scope is no-dispatch, and accepted refresh
  remains distinct from later availability and mapping verification.
- The adapter issues no internal mutation retry. Context cancellation and
  deadlines retain `errors.Is` identity, and typed upstream failures remain
  sanitized. A-33's durable uncertain-result persistence, read-only
  reconciliation, and restart behavior remain the action handler/executor's
  responsibility; this receipt does not claim end-to-end A-33 closure.
- The configured exact fence controls local enablement. Synthetic `10.10.7`
  fixtures do not prove a live deployment or broad Jellyfin release
  compatibility. Release, deployment, native-environment verification, and
  live mutation remain separate gates.

## Independent checks

All module commands used `GOWORK=off`. Offline generation, guardrail, and lint
commands also used `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Focused adapter `go test -mod=readonly -race -count=3 -timeout=180s` and `go vet` | Passed. |
| Reviewer stale-version/product/malformed/unauthorized probes, `-race -count=5` | Passed; every blocked case made zero POSTs. |
| Root `go test -mod=readonly -timeout=180s ./...`, `go vet`, and `go mod verify` | Passed; all modules verified. |
| Full root `go test -mod=readonly -race -count=1 -timeout=300s ./...` | Passed; storage completed in 200.742s. |
| Jellyfin module `go test -mod=readonly -race -count=2 -timeout=180s ./...`, `go vet`, and `go mod verify` | Passed. |
| Jellyfin offline `go generate ./...` and `./check-generation.sh` | Passed; generated output remained clean. |
| Offline `./scripts/check-guardrails.sh --fast` | Passed: generation, staged generation, root and standalone Vacuum, API, architecture, and targeted tests. |
| Offline `./scripts/check-lint.sh` | Passed: zero issues across all nine modules; architecture and standalone isolation passed. |
| `python3 scripts/check_planning.py` | Passed: 53 tasks, 60 acceptance cases, local links resolve. |
| Root and Jellyfin Linux amd64/arm64, `CGO_ENABLED=0 go build -mod=readonly ./...` | Passed for all four builds. |
| Product-scope diff, `git diff --check`, module identity, and no `replace`/`go.work` inspection | Passed; public Jellyfin pseudo-version remains exact. |

The full `--ci` aggregate was not repeated; C-06 owns that matrix. The complete
root race suite and requested bounded gates passed independently.

## Integration recommendation

The coordinator may integrate the correction and this approval receipt, record
the exact product/handoff/review SHAs, and close X-09's adapter review gate.
Preserve the expected-version configuration fence and the separate durable
handler/reconciliation requirement when X-09 is wired into an executable
workflow. This approval does not authorize deployment, live refresh, release,
or any Seerr mutation.
