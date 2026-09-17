# U-01 correction round two handoff

## Identity

- Task: U-01 correction round two.
- Owner: `/root/x05_implementer`.
- Independent reviewer: `/root/x05_reviewer`.
- Dispatch/current clean base: `ebcc5d933ba3a58b76c645ca6b8b52d34aed429f`.
- Coordinator recorded base: `e116df6a55bd1921be52dc134a0d5ae4dc53d34a`.
- Environment dependency support: `b74cde7e0a38635023a02e905db71ae963a5c0bc`.
- Review source: `docs/execution/handoffs/U-01-review-round1.md`.
- Product commit: `65a1c0e66764308ff95c2a70de10ace15712b695`.
- Handoff commit: this file, committed separately after the product.
- Owned product paths: `ui/internal/client/`, `ui/internal/shell/`,
  `ui/internal/config/`, `ui/cmd/`, and `ui/internal/assets/`.

## Corrections

- R1: `NewHandler` applies the GET/HEAD policy before dispatching every asset
  prefix, including `/preview.svg`, `/assets/`, and
  `/consoleshell/assets/`. Other methods return 405 with `Allow: GET, HEAD`.
  Synthetic tests cover all three prefixes.
- R2: readiness handling explicitly accepts only `StateReady` and
  `StateDegraded`. Empty, unknown, and future states with nil errors remain
  unavailable and return 503; a direct unknown-state regression covers this
  fail-closed behavior.
- R3: UI startup parsing uses the typed `env/v11` `ParseAsWithOptions` path
  with the existing `Environment` tags and `:8081` default. Parser errors are
  sanitized to variable names. API URL and public origin require absolute
  HTTP(S), reject credentials, query, fragment, and invalid ports; public
  origin has no path and permits local HTTP. HTTPS metadata continues through
  Goshtoso, while local HTTP uses a small escaped metadata fallback compatible
  with the shell. Configuration is read once at startup.
- R4: `ui/cmd/mastarr` logs the sanitized variable-specific startup reason and
  fixed restart guidance without logging configured values. A regression checks
  the diagnostic for variable-name and secret/URL absence.

No product changes were made to API/schema/generated bindings, root modules,
clients, scripts, state, or live services. The UI remains HTTP-only and imports
no root internals, database, upstream, or media packages; it has no mutation
or credential capability.

## Verification

All commands below were run from the exact product tree with the stated
environment where shown.

| Check | Result |
| --- | --- |
| `gofmt -w ui/cmd/mastarr/*.go ui/internal/config/*.go ui/internal/shell/*.go` | PASS |
| `GOWORK=off go test ./... -count=1 -timeout=120s` in `ui/` | PASS |
| `GOWORK=off go test -race ./... -count=3 -timeout=180s` in `ui/` | PASS |
| `GOWORK=off go vet ./...` in `ui/` | PASS |
| `GOWORK=off go mod verify` in `ui/` | PASS; all modules verified |
| `MASTARR_GENERATION_SNAPSHOT=1 GOWORK=off GOPROXY=off GOSUMDB=off ./scripts/generate.sh --check` | PASS |
| `./scripts/check-lint.sh --architecture-only` | PASS |
| `python3 scripts/check_planning.py` | PASS; 53 tasks and 60 acceptance cases |
| `./scripts/check-guardrails.sh --fast` | PASS; generation/API/Vacuum, architecture, root fast tests and UI checks |
| `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOWORK=off go test -c -o /tmp/mastarr-u01-r2-ui-amd64.test ./cmd/mastarr` | PASS; static ELF |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 GOWORK=off go test -c -o /tmp/mastarr-u01-r2-ui-arm64.test ./cmd/mastarr` | PASS; static ELF |
| `git diff --check` | PASS |

`GOWORK=off go mod tidy -diff` in `ui/` remains non-clean and is the only
recorded blocker. The coordinator support commit added `env/v11` as indirect,
while the authored config now imports it directly; tidy requests promoting it
to a direct requirement, removing stale indirect `oapi-codegen/nullable` and
`testify` requirements, and normalizing additional `go.sum` entries. The
manifest files are outside this task's ownership and were left unchanged.

The full CI aggregate was not rerun for this correction; browser behavior and
the UI manifest tidy remain follow-up gates. No unsafe success or live-service
behavior is claimed.
