# OpenAI Codex provider operations

The dashboard is local-only at `127.0.0.1:13000`. Browser OAuth returns to `127.0.0.1:1455`; do not expose either port on a non-loopback interface without adding authentication, CSRF protection, and TLS. If the callback cannot open, use Device Code or import the official Codex CLI `auth.json` (normally `~/.codex/auth.json`). The importer accepts the official token/account shape and never echoes token contents.

After authentication, fetch the Codex catalog, select a provider model, assign an exact public alias, and import it into the draft. The route automatically includes every enabled account from that provider where discovery currently reports the model as available; adding another matching account does not require editing the route. Identical upstream model names from different providers remain separate sources. Aliases must be unique. Traffic changes only after **Publish changes**.

Provider model cards expose only metadata returned by discovery: provider, context window, API/tool/function/reasoning support, owner/tier, and active account count. Missing fields are shown as unknown rather than inferred from a model name. **Alternate accounts** rotates healthy accounts per model and ignores sticky affinity. **Switch after quota exhaustion** keeps the provider account order and moves away from an account only after an actual upstream `429`; cached quota percentages never trigger routing changes. `Retry-After`, then Codex reset headers, then the configured cooldown determine how long that account is excluded.

Deleting a public model is permanent for the alias and removes it from virtual-key allowlists, while retained discovery data allows a later re-import. The mutation still creates a draft and requires publishing before the gateway traffic configuration changes.

Quota data comes from an upstream ChatGPT endpoint that may change. If it is unavailable, verify usage on the linked Codex Usage page. Reset credits are non-refundable. A reset is written as pending before dispatch; never retry an `unknown` operation. Reconcile it first, then verify Codex Usage and record an audited manual resolution only when evidence is conclusive.

OAuth credentials are encrypted separately and excluded from snapshots and logs. After migrating an older installation, rotate legacy API keys because database WAL and backups may retain historic plaintext. Rotate the control-plane encryption key only through the documented credential CAS workflow.

`CODEX_TEST_MODE=true` permits overriding OAuth, JWKS, model, quota, and reset origins for the deterministic fake upstream. Production must leave test mode disabled; origin override variables are ignored otherwise.

Validation against the built Compose stack:

```bash
docker compose -f compose.yaml -f compose.codex-test.yaml up -d --build
cd control-plane
npx playwright install chromium
npx playwright test tests/codex-dashboard.spec.ts
cd ..
node test/e2e-codex.mjs
docker compose up -d --force-recreate control-plane gateway
```
