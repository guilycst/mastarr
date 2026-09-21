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
| A-26 | `GET /trash/123e4567-e89b-12d3-a456-426614174000?action=restore&confirm=restore` and the matching `purge` request returned `200`; action and confirmation remained bound in separate GET forms. Invalid `action=delete`, mismatched confirmation, and duplicate action returned `400`. Forged `POST` and `DELETE` returned `405 Allow: GET, HEAD`. | Synthetic API saw exactly four allowed `GET` reads (restore, purge, repeated purge twice) and zero mutation requests. Invalid drafts reached zero reader calls. Test also asserts stopped/no automatic re-add policy and no POST/DELETE form. | Production handler pass; browser route pending until router composition. |
| A-42 | `GET /settings` returned `200` with YAML provenance, `editable=false`, API-owned draft guidance, private/no-store headers, and redacted connection data. Five literal, encoded, and nested-encoded credential-like endpoint drafts returned `400`; safe endpoint draft returned `200`. | Unsafe endpoint drafts caused zero `GetConnection` calls and did not echo markers or values. Response contains no plaintext credential, write-only, or browser-storage secret. | Production handler pass; browser URL/storage inspection pending. |
| A-47 | Shell render for `/media/opaque-record?root=/private/media` produced title, allowlisted canonical `/media`, OG/X metadata, generic `/preview.svg`, private robots/referrer metadata, and escaped title. Preview `GET /preview.svg?...` returned `200` with generic 1200x630 SVG; `POST`/`PUT` returned `405`. Unknown route produced structural `Page not found` alert. | Opaque IDs, query strings, private paths, and request-controlled preview data were absent from rendered output. | Metadata/asset structural pass; final router route-status check pending. |
| A-48 | Shell structural capture contains one semantic `h1`, labelled main section, skip link, focusable main target, labelled menu, navigation `aria-current`, status/alert roles, and configuration landmark. Outage capture keeps a safe Retry link and states no successful state is inferred. | Settings/trash drafts preserve GET-only operator/ETag context; repeated trash drafts perform reads only. | Structural pass; real tab order, focus restoration, repeated-click browser behavior, and back/forward remain pending. |
| A-49 | `shell.NewConfig()` exposes `goshtoso` default theme, system color scheme, persisted preference policy, drawer navigation, and labelled navigation items. | No live viewport or browser theme surface was available. | Configuration evidence pass; 390px/1440px light/dark browser captures pending. |
| A-50 | Synthetic readiness API returned `503` with a private body; normalized client error omitted body/host details. Blocking API honored a 10ms deadline and caller cancellation. Unavailable shell escaped request-controlled route data and rendered sanitized Retry guidance. | `errors.Is` preserved `context.DeadlineExceeded` and `context.Canceled`; no synthetic secret appeared in normalized errors or HTML. | Transport and structural rendering pass; browser visual failure capture pending. |
| A-51 | HTTPS shell capture includes initial title/description/canonical/OG/X metadata and generic safe preview. Asset boundary is GET/HEAD-only with immutable cache and `nosniff`; mutation methods are rejected. | Asset body never reflects request query data. | Initial/asset structural pass; composed route status, screenshot, and browser-origin evidence pending. |

## Reproduction

From `ui/`:

```sh
GOWORK=off go test ./tests/browser -count=1 -timeout=120s
```

The test names map directly to acceptance groups:

- `TestA26TrashActionsStayReadOnlyAndBindDraftChoices`
- `TestA42SettingsRedactsCredentialsAndRejectsEncodedEndpointDrafts`
- `TestA47A48A49A51ShellMetadataAccessibilityAndAssetBoundary`
- `TestA50TransportFailuresTimeoutCancellationAndEscapedErrors`

The absence of CUA/browser and the uncomposed router are review risks, not
claims of browser-level completion. U-05 must be revisited after route
composition to collect screenshots, keyboard traversal, viewport/theme,
browser-storage, and actual status/effect traces.
