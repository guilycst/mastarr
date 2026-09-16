# X-08 retained descriptor service, correction round three

## Assignment and scope

- Task: X-08 correction round three, durable capture publication recovery.
- Owner: `/root/x05_implementer`; independent reviewer: `/root/x05_reviewer`.
- Dispatch checkpoint: `5d0c349660efa06210ae56fceac41edde6bee7b8`.
- Source product under correction: `b8f648f055d922699061264877e9acb81d3ad075`.
- Prior independent review: `0ca424094dbce6700d5823ecbf6bb6e409a4178a`.
- Integrated review/state checkpoint supplied by coordinator: `69b4241367bd29ab17d1422e4e8a64e0994a15a3`.
- Product commit: `b4724473923d2b7059eca519252d119bff9a850e`.
- Owned paths: `internal/descriptors/` and this handoff only. `docs/execution/state.json` remains coordinator-owned.
- Acceptance scope: A-06 and A-42, with the round-three R4 capture publication correction.

## Correction

Capture now records an append-only, redacted `descriptor.capture.intent` before
publishing a private object. The intent binds the descriptor request, generated
resource ID, digest, byte count, source label, root-relative final object path,
root-relative staging basename and the operation-created Unix device/inode
identity. No descriptor bytes, source pathname or absolute storage path enters
the journal. Publication requires this durable identity and uses the existing
exclusive stage, no-replace link and final byte read-back checks. Platforms
without the reviewed object identity primitive fail closed before publication.

After the filesystem publication, metadata insertion or update is followed by a
terminal redacted `descriptor.capture` event linked to the intent event. A
committed or aborted terminal event closes only its exact intent; active-intent
queries exclude those closed rows while retaining the append-only audit history.
Metadata or cleanup failures are no longer discarded. If an operation-owned
final object or stage remains, the service returns `ErrCaptureUncertain` and
leaves the exact intent available for recovery. If both are absent, it records
an aborted terminal event and returns the original metadata failure.

Fresh capture calls reconcile an active intent before attempting another write.
They verify the bound object identity, size and digest, adopt a verified final
object into an unavailable row or create the missing row, and close the intent
only after metadata is durable. A stage left before final publication can be
completed with the same identity checks, no-replace publication and final
read-back; a failed stage cleanup remains uncertain. Changed or foreign
objects never become accepted metadata.

`Delete` and `RecordUnavailable` inspect active capture intents first. An
acknowledged delete of an unavailable row with its bound final object or stage
returns `ErrCaptureUncertain` and cannot terminalize the row. After recovery,
normal exact-scope deletion removes the verified object and preserves the
existing durable deletion audit behavior. Unavailable-row updates also carry a
database fence against an active capture intent.

## Regression evidence

`TestCaptureIntentReconcilesMetadataAndCleanupFailureAfterRestart` exercises an
existing unavailable descriptor with synthetic metadata and cleanup failures.
Capture publishes only after its intent is durable, returns
`ErrCaptureUncertain`, leaves the exact operation object and redacted pending
intent, and prevents acknowledged deletion from claiming success. A fresh
service adopts the object, records the committed capture terminal event, and
then deletes the exact object successfully.

`TestCaptureIntentRecoversNewDescriptorAfterMetadataFailure` covers a failed
metadata insert with no descriptor row. The pending intent contains neither
source bytes nor the fixture's absolute root; a fresh service adopts the bound
object under the original generated ID.

`TestCaptureIntentRecoversStageBeforeFinalPublication` seeds a verified private
stage and durable intent, then exercises fresh-service stage publication and
cleanup. The resulting record is available, the final bytes match the intent,
and the stage pathname is absent.

The existing descriptor tests continue to cover exact export evidence, mounted
pathname replacement, unavailable state, idempotent capture, no-replace
staging, deletion acknowledgement, object identity checks, post-unlink restart
reconciliation, cross-service capture/delete fencing and redacted audit
metadata. No live service, credential, private media, tracker data or user path
was used.

## Verification

| Command or scenario | Result |
| --- | --- |
| `GOWORK=off go test -mod=readonly ./internal/descriptors -count=1` | Exit 0; full descriptor package passed. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -count=1` | Exit 0; full descriptor race suite passed. |
| `GOWORK=off go test -race -mod=readonly ./internal/descriptors -run '^TestCaptureIntent' -count=10` | Exit 0; metadata/cleanup, new-row and staged-publication recovery regressions passed ten repetitions. |
| `GOWORK=off go vet -mod=readonly ./internal/descriptors` | Exit 0. |
| `python3 scripts/check_planning.py` | Exit 0; planning valid, 53 tasks and 60 acceptance cases. |
| Versioned pre-commit hook during product commit | Exit 0; generation, staged contract/Vacuum, architecture, formatting and fast root/UI checks passed. |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | Exit 0; generation, all standalone Vacuum contracts, lint/architecture, root/UI/tools/client tests and race, vet, module verification and 18 Linux cross-builds passed; final marker `guardrail checks passed (ci)`. |
| `git diff --check` and `gofmt -l internal/descriptors/descriptors.go internal/descriptors/descriptors_test.go` | Exit 0; no whitespace or formatting findings. |

No check was skipped or blocked. The full aggregate was run from the product
commit with offline module settings and completed successfully.

## Review and resume

- Product is ready for independent review at `b4724473923d2b7059eca519252d119bff9a850e`.
- Round-three R4 is addressed with a pre-publication durable intent, exact
  object identity, explicit uncertain cleanup, Delete/RecordUnavailable fences,
  and fresh-service adoption/stage recovery.
- No upstream, credential, migration, API or runtime capability was broadened.
- Unsupported platforms without a reviewed serializable object identity retain
  fail-closed capture publication and deletion behavior.
- Coordinator must record the product and this handoff commit in
  `docs/execution/state.json`; review remains a separate gate.
- Active process or agent ownership, sanitized: no active descriptor process;
  shared `main` may advance under coordinator ownership.
- Next safe action: independent review of the exact product tree.
- Blocker and exact input/evidence needed: none for this correction; the
  coordinator must record the handoff SHA after its commit.
- No conflicting writes or unknown files were removed; only the owned
  descriptor product and handoff paths were changed.
