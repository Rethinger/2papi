# Dashboard RU/EN Localization Design

Date: 2026-08-05
Status: Approved
Related design: `docs/superpowers/specs/2026-08-04-dashboard-control-plane-design.md`

## 1. Goal

Add a complete Russian and English interface to the local 2papi control-plane dashboard without introducing locale-specific routes or a heavyweight internationalization dependency.

The first locale follows the browser language. A manual RU/EN choice persists across reloads. User-facing interface text is translated, while technical identifiers, model/provider values, and audit event data remain unchanged.

## 2. Confirmed product choices

- Supported locales: Russian (`ru`) and English (`en`).
- First visit: use the browser language, with Russian selected only for a Russian browser preference and English as the fallback.
- Manual override: persist the choice and reuse it on future visits.
- Scope: navigation, headings, descriptions, controls, forms, statuses, validation chrome, empty states, dialogs, and accessibility labels.
- Preserve original values: provider names, model aliases, IDs, endpoints, routing strategy values, configuration payloads, and audit records.
- Add keyboard dismissal with `Escape` to all dialogs while touching the shared modal behavior.
- No locale in the URL. The dashboard remains a single local route.

## 3. Chosen architecture

### 3.1 Typed local dictionary

Use a small application-owned i18n module rather than `next-intl`.

The English dictionary defines the complete key set. The Russian dictionary must satisfy the same TypeScript shape. Missing and extra keys therefore fail type checking. Translation lookup supports named placeholders for values such as counts and versions.

The module owns:

- `Locale = 'ru' | 'en'`;
- locale validation and browser-language parsing;
- English and Russian message dictionaries;
- interpolation of named values;
- human-readable labels for known UI statuses and actions.

Raw technical values are never passed through a general translator. Only known presentation labels use translation keys.

### 3.2 Initial locale and persistence

The server chooses an initial locale in this order:

1. a valid `2papi_locale` cookie from a previous manual selection;
2. the request `Accept-Language` header;
3. English fallback.

The client receives that locale as a prop, preventing a hydration mismatch and avoiding a visible language swap on first paint. Changing the toggle updates:

- React locale state;
- `localStorage` key `2papi.locale`;
- cookie `2papi_locale` for future server renders;
- `document.documentElement.lang`.

On hydration, a valid local-storage value may repair an absent or stale cookie. The explicit client preference wins over browser detection.

### 3.3 Dashboard integration

`DashboardClient` owns the active locale and derives a `t()` translator. Shared presentational helpers receive translated labels or the translator explicitly. Navigation entries store translation keys rather than English labels.

The language switch is a compact two-option segmented control in the sidebar footer. It remains reachable in the mobile horizontal navigation layout and exposes an accessible localized label.

`<html lang>` is set from the same server-side locale decision and updated immediately after a manual switch.

## 4. Translation boundary

### 4.1 Translate

- sidebar navigation and system state;
- top-bar actions and breadcrumbs;
- page headings and explanatory copy;
- metric labels and UI-generated details;
- buttons, field labels, placeholders, hints, and form actions;
- enabled/disabled/ready/fallback presentation labels;
- empty states and one-time secret instructions;
- modal titles, modal eyebrow, close labels, and generic client errors;
- settings descriptions and snapshot action labels;
- accessibility labels and screen-reader-only text.

### 4.2 Keep unchanged

- account, provider, and model names entered by the user;
- slugs, IDs, aliases, URLs, ports, model names, and API key prefixes;
- stored routing strategy and resilience values shown as technical configuration;
- audit action names and audit payload data;
- backend-supplied technical error details when no stable error code exists;
- product name `2papi` and technical brand label `CONTROL PLANE`.

Localized context surrounds unchanged technical content so the user can still understand what it represents.

## 5. Dialog behavior

The shared modal component installs an `Escape` key listener only while mounted. Escape invokes the same close callback as the close button and backdrop click. Form submission and pending mutations retain their current behavior.

The close button and dialog label are localized. The implementation must not close a dialog in response to unrelated keys.

## 6. Error handling

Client-generated fallback errors use translation keys. Backend error text is preserved because the current API exposes messages rather than stable localization codes. The surrounding alert title, dismiss label, and recovery controls are localized.

Unknown or invalid persisted locales are ignored safely and replaced by browser detection or English fallback.

## 7. Accessibility and responsive behavior

- The RU/EN control uses real buttons with pressed/current state.
- The group has a localized accessible name.
- `<html lang>` always matches the selected locale.
- Dialog Escape behavior is keyboard accessible.
- Longer Russian labels must not create page-level horizontal overflow at 390px.
- Existing internal scrolling for mobile navigation and wide account tables is preserved.

## 8. Validation

Automated validation must include:

- TypeScript compilation and production Next.js build;
- dictionary key parity and locale parsing tests;
- existing control-plane unit and PostgreSQL integration tests with zero skips;
- Go race tests and `go vet` to detect cross-stack regressions;
- Compose health and the scripted create/publish/adopt/request/rollback E2E flow;
- Playwright checks in both RU and EN for locale persistence, `<html lang>`, dialog Escape behavior, console errors, and page-level overflow;
- desktop and mobile screenshots for both locales.

## 9. Non-goals

- locale-prefixed routes;
- more than two languages;
- translation of user-generated or provider-generated content;
- translation of raw audit events or configuration JSON;
- server-side localization of API response messages;
- introducing a third-party translation management service.
