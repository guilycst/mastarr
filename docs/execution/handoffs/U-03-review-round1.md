# U-03 independent review round one

## Decision

`changes_requested`.

Three P1 and three P2 findings remain. Production workflow normalization can
attach a foreign action run to the requested step, omits the referenced action
plan information that the page claims to show, and duplicates unresolved
effects. Review detail omits decision-critical exact scope. The HTTP decoders
accept duplicate JSON keys, and both draft forms reject note lengths that their
HTML advertises as valid.

## Review identity and scope

- Independent reviewer: `/root/x05_reviewer`; reviewer did not author U-03.
- Product commit:
  `ac732775c0877932687148ddb1c77596c80d9be5`.
- Product tree: `738e6ecce689967059be28b3e6a3f89b1a8b1f42`.
- Product base: `b5472182821951ee9e0a4649f397192d7ab6a107`.
- Product base tree: `11115c24ddaab8d324201f0b026ea1ab5be449e3`.
- Handoff commit:
  `a7536bad70b157d2db3fec6aed5b42a6ba135d50`.
- Handoff tree: `5020f243694ef3e67cc7e22338c6474718b28982`.
- Clean review checkpoint:
  `31d6fe147a0915d8eaaaefdec4c7ddbd71143fe8`.
- Checkpoint tree: `e704fa21ac77a8f2b7872e62efbd148139638deb`.
- Handoff SHA-256:
  `f796c791e4380476a2aeb57814266a8f85b0f91dd5b43ad6256667719f466144`.
- Product diff SHA-256 for `ui/internal/review/` and
  `ui/internal/workflows/`:
  `aef7ab1a0965e47bee32d70a0cadf8cb9bdb5656257a1fe08371c01ec9d7ab9a`.

Git ancestry binds product to handoff and handoff to checkpoint. Product adds
only the eight recorded files under the two owned UI packages. Review used
synthetic in-memory readers and local `httptest` servers. Temporary reviewer
probe files were removed before this receipt.

## Findings

### R1 — P1: a foreign action run is accepted as evidence for the requested workflow step

`ui/internal/workflows/http.go:297-314` requests the step's linked action-run
ID, but `getActionRun` at lines 318-330 returns any decoded record without
checking its ID. `mergeActionRun` at lines 333-357 then overwrites the expected
`ActionRunID` with the response ID and never checks `PlanId`, `WorkflowRunId`,
or `StepId` against the workflow and step being normalized.

Independent synthetic reproduction served workflow `...0010` with step
`copy`, plan `...0012`, and linked run `...0011`. The requested
`/api/v1/action-runs/...0011` response instead returned run `...0021`, plan
`...0022`, and `stepId: "foreign"`. `GetWorkflow` returned success and exposed
run `...0021` under the requested step. This permits effects and state from a
different action, plan, or step to appear as authoritative evidence for the
selected workflow, violating A-35, A-37, and A-49.

Required correction: bind every joined response to the requested run, plan,
workflow, and step before merging. Missing identity that is required for a
workflow-linked run must remain incomplete, not silently accepted. Add separate
regressions for foreign and absent run/plan/workflow/step identity, with zero
foreign evidence rendered.

### R2 — P1: production workflow pages cannot show referenced plan kind, binding, or impacts

`ui/internal/workflows/http.go:283-315` copies the workflow envelope and fetches
only linked action runs. It never reads each `ActionPlanId`. Consequently the
production HTTP path never populates `Step.ActionKind`, `Step.Impacts`, or
`Step.Destructive`; it also never populates the plan revision/digest/source/
configuration/mapping/manifest fields rendered at
`ui/internal/workflows/model.go:522-549`. The positive rendering test bypasses
this gap by injecting those fields directly through `fakeReader`.

Real normalized workflow pages therefore cannot distinguish registration from
import by action kind, cannot show exact client/torrent/destructive impacts,
and render claimed immutable binding fields as `unknown`. A composed workflow
also needs binding per referenced plan; one set of workflow-level plan fields
cannot represent multiple independent plans. This misses the central U-03
standalone/composed and exact-binding scope and its contributions to A-12,
A-15, A-20, and A-37.

Required correction: load and identity-bind each referenced plan through the
generated HTTP client, retain its immutable binding and exact impact information
on the corresponding step, and render it there. Add production `HTTPReader`
fixtures for standalone and registration-then-import workflows rather than
proving these fields only with a hand-built normalized model.

### R3 — P1: unresolved action effects are duplicated and contradictory evidence can inflate effect count

`ui/internal/workflows/http.go:341-353` preserves `UnresolvedEffects` and also
appends every unresolved ID to `Step.Effects` for unknown/needs-review runs.
`ui/internal/workflows/model.go:593-600` then renders `Step.Effects` and
`UnresolvedEffects` separately. Every ordinary unresolved ID is shown twice;
an ID present in both API `effects` and `unresolvedEffects` is shown three times.
Validation at `model.go:387-421` checks only aggregate slice length. It does not
reject empty, duplicate, overlapping, or count-contradictory evidence and does
not compare `AggregateEffectCount` or `UnresolvedCount` with step evidence.

Independent synthetic reproduction supplied one step with `file:1` in both
effect sets. Handler returned HTTP 200 and rendered `file:1` three times. This
can overstate actual effect count after cancellation or a lost response, in
direct conflict with A-35, A-37, and A-48.

Required correction: retain one exact entry per effect identity with one honest
state, reject or explicitly reconcile contradictory/duplicate sets, and verify
workflow aggregate counts when present. Add real HTTP-reader-to-handler
regressions for applied, unresolved, overlapping, duplicate, empty, partial,
cancelled, and already-satisfied evidence.

### R4 — P1: review detail omits exact approval scope and decision-impact evidence

Conversion retains authority-bearing values, including client item IDs, stopped
client IDs, trash retention/identity, subtitle hearing-impaired state,
capabilities, and estimated bytes (`ui/internal/review/http.go:295-329` and
lines 333-435). Rendering at `ui/internal/review/model.go:588-699` does not show
those values. In particular:

- `client.stop` and `client.remove` do not show affected torrent/client IDs;
- `fs.trash` does not show stopped-client prerequisites or retention;
- import rows omit hearing-impaired/SDH state;
- hardlink/copy review does not show `EstimatedBytes` despite promising a new
  plan with a space estimate;
- plan capabilities are not displayed.

Independent handler reproduction supplied exact client item
`torrent-exact-1` plus an English SDH subtitle. Response was HTTP 200 and
contained neither client ID nor any SDH/hearing-impaired field. This prevents
the review surface from proving exact selected scope and impact under A-13,
A-15, A-20, and A-49.

Required correction: render all action-kind-specific authority fields with
explicit unknown handling and tests for every supported action kind. Keep exact
client records, unselected-payload impact, retention, irreversible scope,
subtitle flags, capabilities, and storage estimate visible before a draft can
say “Approve exact plan.”

### R5 — P2: duplicate JSON object keys are accepted by both HTTP readers

Both `decodeStrict` implementations
(`ui/internal/review/http.go:233-247` and
`ui/internal/workflows/http.go:233-247`) use `encoding/json` with
`DisallowUnknownFields`, which does not reject duplicate object keys.
Independent reproduction returned a workflow with both `"name":"first"` and
`"name":"forged"`; `GetWorkflow` succeeded and retained `forged`. The same
decoder shape protects plans and action runs, including identity, digest,
discriminator, state, and effect fields.

Required correction: reject duplicate keys at every nesting depth before typed
decode. Add plan, workflow, action-run, nested action-union, and nested manifest
fixtures. Unknown fields, trailing data, invalid UTF-8, and duplicate keys must
all remain protocol errors.

### R6 — P2: both forms advertise 4,096-byte notes but reject them after 512 bytes

Both parsers apply `MaxValueLength` (512) to every query value before field-
specific handling (`ui/internal/review/model.go:384-400` and
`ui/internal/workflows/model.go:340-356`). Both render reason textareas with
`maxlength="4096"` (`review/model.go:740-741` and
`workflows/model.go:657`). An independently submitted 513-byte workflow reason
received HTTP 400 rather than preserving the accepted draft. Review uses the
same code pattern.

Required correction: enforce 4,096 bytes for `reason` and the tighter limit for
other scalar fields, then test 512, 513, 4,096, and 4,097-byte boundaries in
both handlers. This is required for A-48 draft preservation.

## Additional observations

- Registration and exact import phase wording is separate and explicit.
- Registration monitoring normalization preserves nil/false/true. Monitoring
  and transfer-fallback draft controls are nevertheless rendered for every
  action kind, including unrelated client, delete, and refresh plans. Correction
  should scope those controls to the action kind to avoid contradictory drafts.
- GET/HEAD method gates, detail identity checks at the handler boundary,
  redirect refusal, context timeout/cancellation classification, sanitized
  display errors, HTML escaping, private response headers, generated DTO
  containment, and root-module import boundaries passed review.
- No native mutation route, root/database import, live service, or private data
  use was found in the reviewed packages.

## Independent verification

| Command or scenario | Result |
| --- | --- |
| Exact commits, trees, ancestry, owned diff, SHA-256s, clean product tree, and `git diff --check` | PASS. |
| Temporary focused reviewer probes `TestReviewerProbeForeignRunAndPlanAreAccepted`, `TestReviewerProbeDuplicateJSONAccepted`, `TestReviewerProbeContradictoryEffectsInflatePage`, `TestReviewerProbeAdvertisedReasonLengthRejected`, and `TestReviewerProbeExactClientScopeAndSDHOmitted` | Reproduced R1 and R3-R6; all probes demonstrated the unsafe/current behavior and were removed. |
| `GOWORK=off go test ./internal/review ./internal/workflows -count=1 -timeout=120s` | PASS. |
| `GOWORK=off go test -race ./internal/review ./internal/workflows -count=1 -timeout=180s` | PASS. |
| `GOWORK=off go vet ./internal/review ./internal/workflows` | PASS. |
| `GOWORK=off go test ./... -count=1 -timeout=240s` from `ui/` | PASS. |
| `GOWORK=off go test -race ./... -count=1 -timeout=300s` from `ui/` | PASS. |
| `GOWORK=off go vet ./...`, `GOWORK=off go mod verify`, and offline `go mod tidy -diff` from `ui/` | PASS; all modules verified and no manifest diff. |
| CGO-free `GOOS=linux go build ./...` from `ui/` for amd64 and arm64 | PASS. |
| Offline `./scripts/generate.sh --check` | PASS; generation and staged generation checks passed. |
| Offline `./scripts/check-api.sh` | PASS; Vacuum score 100/100 and zero warning/error exit. |
| `GOWORK=off ./scripts/check-lint.sh` and `python3 scripts/check-architecture.py` | PASS; zero lint issues and all import boundaries passed. |
| `python3 scripts/check_planning.py` | FAIL before receipt: `U-03: invalid status`; coordinator state uses `awaiting_review`, while validator accepts `in_review`. This is a coordinator-ledger issue, not a product-path finding. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 with final `guardrail checks passed (ci)`, including aggregate race/module/cross-build checks. |

## Acceptance disposition

- A-12 and A-13: phase wording and tri-state monitoring normalization exist,
  but production workflow action identity and action-kind presentation remain
  blocked by R1/R2; exact action-specific review fields remain blocked by R4.
- A-15 and A-20: immutable plan digest/manifest basics render on review pages,
  but per-step workflow bindings and copy storage/capability evidence remain
  blocked by R2/R4.
- A-35 and A-37: cancellation wording is sound, but foreign joined action-run
  evidence and duplicated unresolved effects remain blocked by R1/R3.
- A-48: idempotency values round-trip at ordinary lengths, but actual effect
  count and the advertised reason boundary remain blocked by R3/R6.
- A-49: top-level detail ID checks pass, but linked action-run identity and
  exact client/subtitle scope remain blocked by R1/R4.

## Integration recommendation

Do not integrate U-03 as complete. Correct R1-R6, add production HTTP fixtures
that drive the rendered pages rather than relying on enriched fake readers, and
repeat independent review. Coordinator should also change U-03 state from
`awaiting_review` to the validator-supported `in_review` before claiming the
planning gate.
