# X-08 retained descriptor service, correction round six

## Assignment

- Task: X-08 correction round six, owner-fenced filesystem transitions and release metadata.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `fac20adb43e326b8691ca6c0b5f323d4f7add12c`.
- Source product under correction: `a6cb5f06fc3248704ff84270776cc7a203966305`.
- Prior independent review: `e15275d5741e66a05102540e18cb503b429734fb`.
- Integrated dispatch checkpoint: `85a1f751545e768f41e62a7330b9506b63e690ac`.
- Product commit: `8c770f779204a00c2d8cc6312a2c0fc04262ea35`.
- Owned paths: `internal/descriptors/` and this handoff only. `docs/execution/state.json` remains coordinator-owned.
- Acceptance scope: A-06 and A-42, with review findings R4c and R4d.

## Correction

Capture recovery now revalidates the exact durable fence owner, generation and
lease immediately before each recovery filesystem transition. Stage-to-final
publication checks the fence after the publication seam and immediately before
the no-replace link. Stage cleanup checks it immediately before the identity
guarded remove. A fenced final-object cleanup checks it immediately before the
descriptor-relative unlink as well as at the enclosing cleanup boundary. Any
expired, released, replaced or opposing owner returns `ErrCaptureUncertain`
without performing the transition. Same-operation owners are also rejected at
the transition boundary, so a second live recovery executor cannot mutate the
first executor's stage.

The Unix descriptor-relative removal primitive accepts a guard callback after
reopening and identity-checking the target and invokes it immediately before
`unlinkat`. Platforms without the reviewed primitive retain the existing
fail-closed removal behavior. Existing no-replace publication, digest/size and
object-identity read-back, and cancellation/uncertainty behavior remain in
place.

Fence release metadata now persists the exact `LeaseUntil` value from the
pending fence. Release validates that value with the event, operation, owner
and generation. Retrying an exact-owner release recognizes the already
persisted release metadata and returns success without appending a duplicate
release event. A changed or incomplete release identity remains uncertain.

## Regression evidence

`TestExpiredCaptureOwnerCannotPublishAfterOpposingDeleteFence` creates a
pending capture intent and stage, advances a deterministic clock past the
capture owner's lease at the publication boundary, and starts acknowledged
Delete. The Delete side signals after acquiring its opposing
`delete-reconcile` fence and remains held while the stale capture owner
revalidates. The stale owner returns `ErrCaptureUncertain` before linking the
stage, Delete returns uncertainty after observing the still-bound stage, the
final object remains absent, the stage remains present, and no delete terminal
event is recorded.

`TestCaptureFenceReleasePersistsLeaseAndIsIdempotent` releases one synthetic
fence, decodes the persisted release event and checks its exact owner,
generation, event and RFC3339Nano lease. It then retries the same release and
proves that the release-event count remains one.

The existing owner/generation, lease-expiry takeover and publication/Delete
fence tests continue to pass. Fixtures use only synthetic descriptor bytes,
temporary SQLite state and disposable roots; no credentials, live services,
private coordinates or real media data are used.

## Verification

| Command or scenario | Commit / fixture version | Result / exit status | Evidence path |
| --- | --- | --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -run 'TestCaptureFenceRelease|TestExpiredCaptureOwner' -count=1 -timeout=90s` | `8c770f7` / synthetic temp roots | Exit 0 | `internal/descriptors/descriptors_test.go` |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -run 'TestCaptureFence|TestExpiredCaptureOwner' -count=5 -timeout=180s` | `8c770f7` / synthetic temp roots | Exit 0 | `internal/descriptors/descriptors_test.go` |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -count=1 -timeout=120s` | `8c770f7` | Exit 0 | descriptor package tests |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -count=1 -timeout=180s` | `8c770f7` | Exit 0; 33.280s | descriptor package race suite |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | `8c770f7` | Exit 0 | descriptor package vet |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | `8c770f7` | Exit 0 | Linux amd64 compile |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | `8c770f7` | Exit 0 | Linux arm64 compile |
| `GOWORK=off GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | `8c770f7` | Exit 0 | Darwin amd64 compile |
| `GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | `8c770f7` | Exit 0 | Darwin arm64 compile |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | final product tree at `8c770f7` | Exit 0; final marker `guardrail checks passed (ci)` | generation, Vacuum, architecture/lint, tests/race/vet, module verification and cross-build output |
| Versioned pre-commit hook during product commit | `8c770f7` | Exit 0; fast generation, staged contract/Vacuum, architecture and guardrails passed | `.githooks/pre-commit` |
| `git diff --check`; `gofmt -l internal/descriptors/*.go` | `8c770f7` | Exit 0; no whitespace or formatting findings | working tree checks |

The aggregate gate was run with `GOWORK=off`, `GOPROXY=off` and `GOSUMDB=off`.
It covered the root, UI, tools and all standalone client modules, including
their race/vet/module-verification and Linux cross-build lanes. No checks were
skipped for this correction.

## Review and resume

- Product is ready for independent review at `8c770f779204a00c2d8cc6312a2c0fc04262ea35`.
- R4c is addressed by exact fence revalidation at publication, stage removal
  and fenced final cleanup, with a deterministic expired-owner/opposing-Delete
  regression.
- R4d is addressed by persisted lease metadata and exact-owner idempotent
  release with a duplicate-event regression.
- No runtime capability, schema, upstream, credential or public API was added.
- Coordinator must record the product and handoff SHAs in
  `docs/execution/state.json`; review remains a separate gate.
- No implementation blocker remains. The independent reviewer should verify
  the exact lease checks at each filesystem transition and the persisted
  release metadata in the product tree.

## Resume checkpoint

- Current state: product committed at `8c770f779204a00c2d8cc6312a2c0fc04262ea35`; handoff commit follows separately.
- Active process or agent ownership: no active X-08 process; shared `main` remains coordinator-managed.
- Next safe action: independent review of the exact product tree, then coordinator state recording/integration.
- Blocker: none for this correction; unresolved integration/release gates remain coordinator-owned.
- No conflicting writes or unknown files were removed.
