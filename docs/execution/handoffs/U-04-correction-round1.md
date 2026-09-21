# U-04 correction round one handoff

## Identity and scope

- Task: U-04 correction round one, trash and settings review findings.
- Owner: `/root/c01_implementer`.
- Independent reviewer: `/root/c01_reviewer`.
- Dispatch/base checkpoint: `eefef5f1b71f0bee9d587887d99023889e751787`.
- Review receipt: `26c1355f59c08cdf78c1eb0c426602b230cd8552`.
- Prior product: `13ec09f9f54523788aa4ddee1efa9e7bd9165704` plus coverage follow-up `f63cedf1dc93c6def4fb851342a3da1c19fbbd9b`.
- Prior handoff: `b99fb7552be89994e870fa50b6352b2a58e9b533`.
- Branch/worktree: shared `main`; coordinator owns `docs/execution/state.json`.
- Owned product paths: `ui/internal/trash/` and `ui/internal/settings/`.
- Handoff path: `docs/execution/handoffs/U-04-correction-round1.md`.
- Product commit: `067ebe5fbef03ae915bf31d22234d637a9147cb2`.

## Findings closed

### P1-1 — exact trash action binding

Trash drafts now accept only `restore` and `purge`. Confirmation is accepted
only when it repeats the selected action; missing, duplicate, unknown and
mismatched action/confirmation values fail before the reader is called. The
visible selector marks the selected action, and the second form carries the
same action in its hidden field and confirmation value. Production
HTTPReader-to-handler fixtures cover default restore, restore, purge, missing
action, duplicate action, unknown action, mismatched confirmation and the
unbound `confirm=yes` case.

### P1-2 — endpoint draft credential boundary

Settings query parsing rejects endpoint drafts containing userinfo, query,
fragment or credential-like material before values reach normalized state or
HTML rendering. API response endpoint redaction remains in place. Production
handler fixtures cover synthetic userinfo, token query and fragment attempts;
all return `400` without echoing the supplied secret markers.

### P1-3 — exact trash manifest evidence

Trash normalization and handler validation reject duplicate original targets,
duplicate selected root-relative files and repeated nonempty manifest
identities. Distinct video and subtitle files remain accepted. Detail rendering
now retains identity, type, role, size, digest, observed timestamp and exact
root-relative target, showing `unknown` only for absent optional evidence.
Production list/detail reader and handler fixtures cover distinct multi-file
entries plus duplicate file, duplicate target and repeated-identity responses;
invalid responses fail closed with no partial page.

### P2-1 — source ownership binding

Source metadata now enforces `source=yaml` with `editable=false`; contradictory
API evidence returns a sanitized protocol error before any handler rendering.
Production fixtures cover configuration, connection, storage-root and
path-mapping contradictions, while active API and YAML records plus retired
connection and storage-root observations remain accepted and preserved.

### P2-2 — restore policy visibility

Trash detail now states that associated clients stay stopped and restore has no
automatic re-add or automatic resume. Missing stop, remove and association
observations remain explicitly unknown. A production handler fixture with no
client association verifies policy text and unknown presentation together.

## Verification

Commands ran against product commit `067ebe5fbef03ae915bf31d22234d637a9147cb2`.
All fixtures use synthetic `httptest` services; no live service, credential,
database, filesystem or media operation was used.

| Check | Result |
| --- | --- |
| `gofmt -w ui/internal/trash/model.go ui/internal/trash/http_test.go ui/internal/settings/model.go ui/internal/settings/http_test.go` | PASS |
| `git diff --check` | PASS |
| `GOWORK=off go test ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=1` from `ui/` | PASS |
| `GOWORK=off go vet ./...` from `ui/` | PASS |
| `GOWORK=off go mod verify` from `ui/` | PASS; all modules verified |
| `GOWORK=off go mod tidy -diff` from `ui/` | PASS; no manifest diff |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` from `ui/` | PASS |
| `GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...` from `ui/` | PASS |
| `GOWORK=off ./scripts/generate.sh --check` | PASS; generation and staged generation checks passed |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-api.sh` | PASS; API/Vacuum score 100/100 |
| `python3 scripts/check-architecture.py` | PASS |
| `GOWORK=off ./scripts/check-lint.sh` | PASS; lint, architecture and standalone client boundaries passed |
| `python3 scripts/check_planning.py` | PASS; 53 tasks and 60 acceptance cases |
| `GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/check-guardrails.sh --ci` | PASS; exit 0 and `guardrail checks passed (ci)` |
| Product commit hook | PASS; staged generation/API/Vacuum, architecture and fast guardrail checks |

## Boundaries and follow-up

Correction changes stay within the assigned trash/settings packages and this
handoff. API/OpenAPI/generated files, router composition, root packages, state
files and other UI lanes were not edited. UI remains HTTP-only and read-only;
forms preserve GET draft context and never dispatch restore, purge, hard delete,
credential replacement or configuration writes.

Frozen `TrashEntry` still does not expose retention-policy details, per-effect
stop/remove evidence, restore-conflict records or per-item outcomes. Those
fields remain unknown rather than inferred. Browser integration, production
router composition and independent correction review remain separate gates.

## Review and integration

- Product commit: `067ebe5fbef03ae915bf31d22234d637a9147cb2`.
- Handoff commit: this file, committed separately after product.
- Independent review: pending `/root/c01_reviewer`.
- Coordinator integration/state update: pending; this lane did not edit
  `docs/execution/state.json`.
- Blocker: none for owned readers, handlers or synthetic verification.
