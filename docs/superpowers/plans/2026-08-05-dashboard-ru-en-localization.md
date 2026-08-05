# Dashboard RU/EN Localization Implementation Plan

Date: 2026-08-05
Design: `docs/superpowers/specs/2026-08-05-dashboard-ru-en-localization-design.md`

## Goal

Implement the approved two-language dashboard with browser-based initial selection, persistent manual override, complete UI coverage, unchanged technical/audit values, accessible locale controls, and Escape-close dialogs.

## Step 1: Add the typed localization core

Files:

- create `control-plane/app/i18n.ts`
- create `control-plane/tests/i18n.test.ts`

Work:

1. Define `Locale`, supported locales, cookie/storage names, and locale guards.
2. Add pure parsing functions for stored values and `Accept-Language`.
3. Define the complete English flat dictionary and require Russian to satisfy its exact key shape.
4. Add named placeholder interpolation.
5. Test Russian browser detection, English fallback, persisted locale validation, key parity, and interpolation.

Verification:

- `cd control-plane && npm test -- --test-name-pattern="locale|translation"`
- `cd control-plane && npm run build`

## Step 2: Resolve locale on the server

Files:

- modify `control-plane/app/page.tsx`
- modify `control-plane/app/layout.tsx`

Work:

1. Read `2papi_locale` from cookies.
2. Fall back to request `Accept-Language`.
3. Pass `initialLocale` to `DashboardClient`.
4. Set `<html lang>` from the same decision.
5. Keep metadata product-oriented and language-neutral where practical.

Verification:

- production build succeeds without hydration or dynamic API errors;
- direct requests with Russian and English `Accept-Language` produce the expected `<html lang>` when no preference cookie exists.

## Step 3: Integrate locale state and persistence

Files:

- modify `control-plane/app/dashboard-client.tsx`

Work:

1. Accept `initialLocale` and create the translator.
2. On mount, honor a valid `localStorage` override and repair the cookie.
3. On manual selection, update React state, local storage, cookie, and `document.documentElement.lang`.
4. Convert navigation labels to translation keys.
5. Add the accessible RU/EN segmented control to the sidebar footer.
6. Format dashboard dates with the selected locale.

Verification:

- switching language updates the UI without a reload;
- reloading preserves the selected locale;
- `<html lang>` follows the active locale.

## Step 4: Translate the complete presentation layer

Files:

- modify `control-plane/app/dashboard-client.tsx`

Work:

1. Replace all hard-coded user-interface strings with dictionary lookups.
2. Translate known display statuses while preserving raw routing/status values where they are technical data.
3. Localize generic client fallback errors and alert chrome while retaining backend error details.
4. Localize all form labels, actions, empty states, dialog labels, accessibility labels, metrics, settings copy, and one-time secret instructions.
5. Preserve provider/account/model values and raw audit event content.

Verification:

- automated dictionary tests remain green;
- targeted source check finds no unintended English presentation strings outside the dictionary.

## Step 5: Add dialog keyboard behavior and responsive styles

Files:

- modify `control-plane/app/dashboard-client.tsx`
- modify `control-plane/app/styles.css`

Work:

1. Add a scoped `keydown` listener to the shared modal.
2. Close only on `Escape` and clean up the listener on unmount.
3. Style the locale segmented control consistently with the dark dashboard.
4. Ensure longer Russian labels preserve the 390px mobile layout and existing internal scroll regions.

Verification:

- Escape closes each modal type;
- other keys do not close dialogs;
- no page-level horizontal overflow in RU or EN.

## Step 6: Run the full validation loop

Automated checks:

1. `docker compose exec control-plane npm test` with PostgreSQL tests and zero skips.
2. Local or containerized `npm run build`.
3. `go test -race ./...`.
4. `go vet ./...`.
5. Compose readiness checks for control plane and gateway.
6. `node test/e2e.mjs` for create, publish, adopt, authenticated request, invalid-key rejection, rollback, and cleanup.

Playwright checks in both locales:

1. desktop 1440x1000 and mobile 390x844;
2. expected localized navigation and page headings;
3. manual toggle and reload persistence;
4. matching `<html lang>`;
5. raw audit action/value remains unchanged between locales;
6. modal closes on Escape;
7. zero console errors and warnings;
8. zero page-level horizontal overflow.

Artifacts:

- save ignored screenshots under `artifacts/` for RU and EN desktop/mobile views.

## Step 7: Commit only after green validation

1. Run `git diff --check`.
2. Confirm no generated build output is staged.
3. Commit the implementation and tests as one verified change.
