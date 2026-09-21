# U-05 UI verification ledger

Date: 2026-09-21

Task: U-05 browser/accessibility verification

Dispatch base: `1cc1c9465bc14cad860082bdc07ca8e4c3a704bb`

Verification artifact: [`ui/tests/browser/browser_test.go`](../../ui/tests/browser/browser_test.go)

## Evidence boundary

The U-05 checkout does not expose a browser surface to CUA (`browsers: []`), so
no live browser screenshot, keyboard trace, viewport capture, or browser
storage inspection was available. The application router is also not composed:
`ui/cmd/mastarr/main.go` currently serves the shell, readiness state, and static
assets, while the review, workflow, trash, and settings handlers remain
separate HTTP-only packages. This ledger therefore separates reproducible
production handler evidence from browser/router evidence that remains pending.

All runnable evidence uses synthetic `httptest` APIs. No credentials,
deployment coordinates, media paths, or live services are used.

## Acceptance evidence

| Case | Exact exercised request and result | Effect/read evidence | Status |
| --- | --- | --- | --- |
| A-26 | `GET /trash/123e4567-e89b-12d3-a456-426614174000?action=restore&confirm=restore` and matching `purge` returned `200`; action and confirmation stayed bound in separate GET forms. Invalid `action=delete`, mismatched confirmation, and duplicate action returned `400`. Forged `POST` and `DELETE` returned `405 Allow: GET, HEAD`. | Synthetic API saw exactly four allowed `GET` reads (restore, purge, repeated purge twice) and zero mutation requests. Invalid drafts reached zero reader calls. Test asserts stopped/no automatic re-add policy and no POST/DELETE form. | Partial production-handler evidence. Browser consequential-action flow is **BLOCKED** by absent browser and uncomposed router. |
| A-42 | `GET /settings` returned `200` with YAML provenance, `editable=false`, API-owned draft guidance, private/no-store headers, and redacted connection data. Five literal, encoded, and nested-encoded credential-like endpoint drafts returned `400`; safe endpoint draft returned `200`. | Unsafe endpoint drafts caused zero `GetConnection` calls and did not echo markers or values. Production HTML contains no plaintext credential, write-only, or browser-storage marker. | Partial production-handler evidence. Browser URL/storage inspection is **NOT RUN** because CUA has no browser. |
| A-47 | No cross-origin form/JSON mutation, forged `Host` or forwarded origin, redirect, CSRF/CORS policy, or unauthenticated direct-client-origin scenario was run by this batch. | No effect or origin count exists for these unavailable scenarios. | **NOT RUN / BLOCKED** by absent browser and uncomposed router. |
| A-48 | Synthetic readiness API returned `503` with a private body; normalized client error omitted body/host details. Blocking API honored a 10ms deadline and caller cancellation. Unavailable shell escaped request-controlled route data and rendered sanitized Retry guidance. | `errors.Is` preserved `context.DeadlineExceeded` and `context.Canceled`. No stale approval, same-key idempotency retry, reload/back-forward, or actual consequential-action effect scenario was run. | Partial transport evidence. Remaining browser/action checks are **NOT RUN / BLOCKED**. |
| A-49 | No exact media/episode/subtitle identity, selection, executed-payload identity, or deep-link switching scenario was run. | No identity/effect evidence exists for this case. | **NOT RUN / BLOCKED** by absent media route composition and browser. |
| A-50 | Structural shell capture asserts one semantic `h1`, labelled main section, skip link, focusable main target, labelled menu/navigation, `aria-current`, status/alert roles, configuration landmark, and shell system-theme configuration. | No 390px/1440px viewport, light/dark visual, keyboard/dialog escape, contrast/zoom, focus restoration, or announced-error browser run was available. | Partial structural evidence only. Browser checks are **NOT RUN / BLOCKED**. |
| A-51 | HTTPS shell capture includes initial title/description/canonical/OG/X metadata, generic preview URL, and escaped route data. Unknown route produces structural `Page not found` alert. Preview `GET` and `HEAD` return `200` with generic 1200x630 SVG, immutable cache, and `nosniff`; `POST`/`PUT` return `405`. | Preview body never reflects request query data. Composed initial HTTP status, unknown-ID route, configured-origin preview loading, and browser screenshot were not run. | Partial shell/asset structural evidence. Composed-route/browser checks are **NOT RUN / BLOCKED**. |

## Reproduction

From `ui/`:

```sh
GOWORK=off go test ./tests/browser -count=1 -timeout=120s
```

The tests map only to acceptance portions runnable without a browser or
composed router:

- `TestA26TrashActionsStayReadOnlyAndBindDraftChoices`
- `TestA42SettingsRedactsCredentialsAndRejectsEncodedEndpointDrafts`
- `TestA48TransportFailureAndSanitizedRecovery`
- `TestA50AccessibilityStructureOnly`
- `TestA51InitialMetadataUnknownRouteAndAssetStructure`

The absence of CUA/browser and the uncomposed router make A-47, A-49 and the
browser portions of A-26, A-42, A-48, A-50 and A-51 **NOT RUN / BLOCKED**. U-05
must be revisited after route composition to collect screenshots, keyboard
traversal, viewport/theme, browser-storage, origin-policy, identity/deep-link,
approval/idempotency, and actual effect traces.
