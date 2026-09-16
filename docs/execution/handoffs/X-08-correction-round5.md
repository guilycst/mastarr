# X-08 retained descriptor service, correction round five

## Assignment and scope

- Task: X-08 correction round five, owner-fenced capture recovery leases.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `c0c03bb20ad063321214bb72da4011be3d0cd18d`.
- Source product under correction: `cc7c530e4114112cceeaba28000ab59d893efc59`.
- Prior independent review: `0bd2bb566fc4feb44141ef28a93ab2880c93bf13`.
- Integrated review/state checkpoint: `7e9bb099040adcbc04b459fcfd59968d65918983`.
- Product commit: `a6cb5f06fc3248704ff84270776cc7a203966305`.
- Owned paths: `internal/descriptors/` and this handoff only. `docs/execution/state.json` remains coordinator-owned.
- Acceptance scope: A-06 and A-42, with review finding R4b.

## Correction

Each acquisition of the existing append-only `descriptor.capture.fence` now
creates a distinct owner-fenced event. The fence metadata contains the exact
capture intent event, descriptor resource, operation, generated owner ID,
per-service generation and a 30-second lease. Same-operation recovery no
longer returns or shares another executor's event. An active opposing
operation remains blocked, while multiple same-operation owners retain their
own independent transitions.

Fence release validates the exact event, owner ID, generation, operation,
intent and lease metadata before appending its redacted `released` event. A
second executor therefore cannot release the first executor's live fence.
Terminal capture transitions use the owner-specific fence and require that it
is still pending and within its lease. They reject any other active owner, so
one executor cannot terminalize another executor's protected transition.

The acquisition compare-and-swap treats an expired lease as recoverable by a
fresh executor. The fresh executor gets a new owner and generation rather than
taking or mutating the old event. The old event remains independently
releasable by its original owner, but cannot block recovery after its lease
expires or perform an expired terminal transition. Unexpired fences continue
to protect the filesystem observation/publication boundary introduced in
round four.

The prior cross-service publication/Delete fence, exact terminal outcome
matching, no-replace stage publication, object identity/read-back checks,
metadata/content split, exact deletion and fresh recovery behavior remain in
place. No schema migration, upstream capability, credential, live service,
media path or public API was added.

## Regression evidence

`TestCaptureFenceOwnerAndGenerationPreventLiveReuseRelease` creates an
unavailable descriptor with a verified private stage and capture intent, then
uses separate `Service` values over the same SQLite database. The first and
second same-operation acquisitions receive distinct fence events and owner
IDs/generations. The second cannot terminalize while the first remains active;
releasing the second does not let `delete-reconcile` acquire an opposing fence.
Only after the first owner releases can the opposing fence be acquired, and it
is released by its own exact owner.

`TestCaptureFenceExpiredLeaseAllowsFreshOwner` advances a deterministic test
clock beyond the first fence's lease and verifies that a fresh service obtains
a new owner-fenced event. The previous event is not reused. The existing
`TestCaptureRecoveryFencePreventsDeleteAbortDuringPublication` continues to
pause stage publication with one fence held and proves that a separate
acknowledged Delete returns `ErrCaptureUncertain` without aborting capture.

The test suite uses synthetic bytes, disposable SQLite state and temporary
roots only. No descriptor bytes or private paths enter audit metadata or
handoff output.

## Verification

| Command or scenario | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -run 'TestCaptureFence' -count=1 -timeout=60s` | Exit 0. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -run 'TestCaptureFence' -count=5 -timeout=180s` | Exit 0; owner/release, publication/delete and expired-lease regressions passed five repetitions. |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -count=1 -timeout=90s` | Exit 0; full package passed. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -count=1 -timeout=180s` | Exit 0; full descriptor race suite passed in 30.443s. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| `GOWORK=off GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/descriptors` | Exit 0. |
| Versioned pre-commit hook during product commit | Exit 0; generation, staged OpenAPI/Vacuum, architecture and fast guardrails passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0 in 104.78s; final marker `guardrail checks passed (ci)`. Generation, all six standalone Vacuum contracts, lint/architecture, root/UI/tools/client tests and race, vet, module verification and 18 Linux cross-builds passed. Aggregate log SHA-256: `2463b50dca12f6a9d0ec1b487c6c7f98ac144c7da26fda046104589034225843`. |
| `git diff --check`; `gofmt -l internal/descriptors/descriptors.go internal/descriptors/descriptors_test.go` | Exit 0; no whitespace or formatting findings. |

## Review and resume

- Product is ready for independent review at `a6cb5f06fc3248704ff84270776cc7a203966305`.
- R4b is addressed with distinct owner/generation fence events, exact-owner
  release/terminal checks and a bounded lease for fresh-executor recovery.
- No runtime capability, schema, upstream, credential or API surface was
  broadened. Unsupported platforms retain existing fail-closed behavior.
- Coordinator must record the product and this handoff commit in
  `docs/execution/state.json`; review remains a separate gate.
- No implementation blocker remains in this correction. The independent
  reviewer should verify the live same-operation owner sequence, lease-expiry
  takeover and opposing Delete fence in the exact product tree.
