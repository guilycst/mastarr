# X-21 independent review, round one

Decision: **approved** for the scoped Jellyfin adapter migration. No P1 or P2
finding remains. The read adapter uses the published standalone client, and the
previously approved write adapter retains its fail-closed compatibility fence.
This approval does not claim deployment, live Jellyfin compatibility, refresh
completion, or later media availability.

## Exact identity and scope

- Reviewer: `/root/x05_reviewer`; independent of product authorship.
- Exact product commit: `d54f9b5937f5dc42829b3f56ee1e392a61bccf8a`.
- Exact product tree: `fc1230897395e9e0f707c554abf466f938552c75`.
- Exact product handoff: `c418d57db3ae0ca04e3f96e8d0228909db10402a`.
- Exact handoff tree: `911d1a5f39a69f495f6eef34b203d838a68b9291`.
- Exact review-dispatch checkpoint: `b2f2c5f77bb50b21d41a879a334555299a38f4f1`.
- Dispatch tree: `0320556c11b7aa2a791e048e171ac013387b953f`.
- Product base: `4260f23f355a13d217c1cb1ecd7d6c8b8f5f763c`.
- Reviewed paths: `internal/adapters/jellyfin/read/`,
  `internal/adapters/jellyfin/write/`, `tests/fixtures/jellyfin/`, root
  `go.mod`/`go.sum`, and the public `clients/jellyfin` boundary.
- Contract: X-21 contributions to A-08, A-09, A-33, and A-55.

The review ran in a clean detached worktree at the exact dispatch checkpoint.
`git diff --exit-code d54f9b5 b2f2c5f -- internal/adapters/jellyfin/read
internal/adapters/jellyfin/write tests/fixtures/jellyfin go.mod go.sum` passed,
so the reviewed scoped product did not drift after its product commit. Product,
generated, state, and unrelated files were not edited. Reviewer probes were
removed before this receipt was written.

## Boundary and translation review

The root read and write adapters import only the normalized public package
`github.com/guilycst/mastarr/clients/jellyfin`. No adapter imports the client's
internal generated package, exposes a generated DTO, builds a direct Jellyfin
HTTP request, or owns token injection. The root module pins the public client at
`v0.0.0-20260915195508-4eee8e84eff5`; its checksum is present, and there is no
local `replace` or `go.work` dependency.

The read adapter preserves instance scope in its stable identities. Identical
native item IDs observed through two configured Jellyfin connections produce
different scoped identities. Provider namespaces and values remain separate,
and provider relationships retain their source instance, kind, native item ID,
provider namespace, and provider value. Missing or contradictory identity,
pagination, or media-source evidence returns unknown/partial evidence rather
than a complete absence or tracked/available claim.

Playable evidence remains explicit:

- A valid native `File`/`FileSystem` video source can be playable under the
  established X-04 contract. When mappings are configured, the source must map
  to exactly one target; ambiguous or nonmatching mappings remain unplayable.
- Path-only item evidence is always unverified and unplayable, including when
  its path maps to a configured target. The mapping is retained only as
  correlation evidence.
- Missing sources, unsupported source type/protocol/location, invalid paths,
  and unavailable exact items do not become playable. Exact item absence is
  kept distinct from collection-level unsupported or incomplete evidence.

Read refresh remains explicitly unsupported and dispatches no mutation. The
write adapter supports only library refresh. It performs a fresh public system
observation before capability reporting and another immediately before the
POST, requiring the configured exact version/product fence. Missing, unknown,
unavailable, malformed, changed, or mismatched compatibility evidence remains
unknown/unsupported and makes zero POSTs. Item refresh remains unsupported.

A successful native refresh response produces request-acceptance evidence only.
It does not fabricate scan completion, mapped-file presence, playability, or
later Jellyfin availability. The adapter performs no internal mutation retry;
typed context errors retain `errors.Is` identity and upstream failures remain
sanitized. Durable uncertain-result persistence and read-only reconciliation
remain the executor/workflow boundary required by A-33.

## Independent adversarial probes

Reviewer-only synthetic `httptest` probes ran ten times under the race detector.
They verified that the same native item through two client instances preserves
provider IDs and both provider relationships while producing distinct scoped
identities and independently mapped targets. A mapped path-only source stayed
unplayable and retained explicit path-only evidence. Read-side refresh returned
unsupported, emitted no extra request, and every read request remained GET-only.

A separate deadline probe verified that transport cancellation preserved
`errors.Is(err, context.DeadlineExceeded)` while the returned error exposed no
endpoint path or token. No live Jellyfin instance, credential, private
coordinate, inventory, or media was used.

## Independent checks

All commands below passed with exit status zero. Go commands used `GOWORK=off`;
offline generation and guardrails also used `GOPROXY=off GOSUMDB=off`.

| Check | Result |
| --- | --- |
| Focused read/write adapter `go test -mod=readonly -count=3` | Passed. |
| Focused read/write adapter `go test -mod=readonly -race -count=3` | Passed with no race. |
| Reviewer instance/provider/path-only/GET-only/deadline probes, `-race -count=10` | Passed; probes removed afterward. |
| Root focused `go vet`, root `go mod verify`, Jellyfin `go vet`, and Jellyfin `go mod verify` | Passed; both modules verified. |
| Jellyfin `go test -mod=readonly -count=3 ./...` and `-race -count=3 ./...` | Passed. |
| Jellyfin offline `go generate ./...`, `./check-generation.sh`, and clean generated diff | Passed; committed generated output reproduced. |
| Root `./scripts/check-api.sh` | Passed; deterministic generation and Vacuum quality 100/100. |
| Root `./scripts/check-lint.sh`, architecture check, and planning check | Passed; zero lint issues, 53 tasks and 60 acceptance cases. |
| Offline `./scripts/check-guardrails.sh --fast` | Passed. |
| Root and Jellyfin Linux amd64/arm64 CGO-free builds | Passed for all four builds. |
| Offline `./scripts/check-guardrails.sh --ci` | Passed in 284.83s; generation, all OpenAPI Vacuum checks, architecture/lint, root and all nested module tests/race/vet/mod verification, and 18 cross-build entries completed. Root storage race completed in 198.407s. |
| Scope-drift check, `git diff --check`, public module identity, and no `replace`/`go.work` inspection | Passed. |

## Acceptance and integration recommendation

X-21's local contributions to A-08 and A-09 are accepted: incomplete evidence
cannot claim availability, and observations remain connection-scoped. Its A-55
contribution is accepted: mapped/native/path-only/unavailable states remain
distinct, refresh acceptance is separate from later availability, and unsupported
scope makes no mutation. The adapter-side A-33 contribution is accepted: no
blind internal retry or fabricated completion was found. End-to-end durable
uncertainty handling remains outside this adapter review.

The coordinator may integrate the product, handoff, and this approval receipt
and record their exact SHAs. Catalog read capabilities intentionally remain
unknown until a native compatibility matrix is pinned; this is fail-closed and
does not block the migration. Runtime configuration, deployment, release, live
compatibility, live refresh, and subsequent library visibility remain separate
gates. No live operation is authorized by this receipt.
