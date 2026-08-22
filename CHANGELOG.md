# Changelog — 2papi

Format: decisions and notable additions, newest first. See docs/ for deep dives.

## 2026-08-22 — Cloud edition foundation

### Signup/login + кредиты (шаг 6 хребта, Cloud)
- Self-serve контур: POST `/api/auth/signup` (без перечисления — ответ одинаков для существующего email), `/api/auth/verify` (токен хранится хэшем, одноразовый, TTL 24ч), `/api/auth/login` (scrypt из node:crypto, без внешних зависимостей), `/api/auth/session` GET=me / POST=logout.
- Верификация в одной транзакции: email_verified_at + личная команда (trust_tier 0) + роль owner + первый virtual key `default` + грант `SIGNUP_BONUS_USD` (по умолчанию $2, диапазон спеки $1–3) через credit_transactions source=signup_bonus с UNIQUE(source, external_id) — идемпотентно.
- policy.go: prepaid баланс команды (`team.balance_usd`) каппирует effective budget — формула владельца min(budget, balance), поверх org-капа; точность ограничена свежестью снапшота (декремент контроль-плейном + ночной reconcile).
- Снапшот эмитит team.balance_usd только когда > 0; OSS не затронут — весь контур под requireHosted() (403 hosted_only).
- Тесты: auth-flow.integration.test.ts (полный флоу + гейты OSS) + TestBalanceCapsTeamBudget.

### Sources[] — multi-provider aliases (шаг 5 хребта)
- `config.Model` += `sources[] {account, upstream_model, weight, input/output_cost_per_mtok}`: один публичный алиас обслуживают разные провайдеры со своими именами апстрим-моделей, весами и ценами; пусто = прежнее поведение 1:1 (бэкомпат).
- Gateway: `ResolvedFor/UpstreamFor/WeightFor` — оверрайд подставляется в копию модели на каждую попытку, поэтому адаптеры и переписывание ответов не меняются; телеметрия пишет фактический `upstream_model`; веса источников переопределяют account.Weight при упорядочении стратегий.
- Снапшот эмитит `sources[]` только когда маппинг реально переопределяет что-то (колонки из миграции 013).
- Валидация: неизвестный/дублирующий аккаунт источника, отрицательные веса/цены → конфиг отвергается.
- Тесты: sources_test.go (хелперы + Build), sources-snapshot.test.ts (эмиссия/бэкомпат).

### SSO/OIDC (шаг 4 хребта, Enterprise)
- Вход в дашборд через OIDC Authorization Code: `/api/auth/oidc/start` → IdP → `/api/auth/oidc/callback`; сессии в `user_sessions` (011), cookie `papi_session` HttpOnly/SameSite=Lax/Secure(prod), TTL 7 дней (`SESSION_TTL_DAYS`).
- Проверка id_token: RS256/384/512 по JWKS (+HS256 через client_secret); подпись, iss, aud, exp (60с skew), nonce. CSRF-state = HMAC от master key, TTL 10 мин, сверка cookie↔query до любых обращений к IdP.
- Провижининг: find-or-create по lower(email), email_verified_at от IdP; suspended-аккаунты не пускаются (`sso_user_disabled`).
- Конфиг оператора: POST/GET `/api/control/v1/oidc` (system_settings 'oidc'; issuer/client_id/scopes/redirect_uri), client_secret не возвращается наружу. Всё под фичей `sso` — без лицензии спит (403 feature_not_licensed).
- Тесты: oidc.test.ts (подпись/клеймы/state) + sso.integration.test.ts (полный флоу против фейкового IdP с мокнутым fetch).

### Organizations (шаг 3 хребта, Enterprise)
- `lib/edition.ts` — гейт изданий для control-plane (зеркало internal/edition + internal/license): `2PAPI_EDITION` env → подписанная `2papi.license` (Ed25519, offline, mtime-кэш) → OSS; `requireFeature('orgs')` спит без лицензии — DX энтузиаста не меняется.
- Organizations API в catch-all роуте: GET/POST/PATCH/DELETE (`organizations`), дубликат имени → 409; привязка `teams.org_id` в POST/PATCH teams. Без лицензии ветки отвечают 403 feature_not_licensed.
- Снапшот: бюджет орги едет на ключе как `team.org {id,budget_usd}` (только когда > 0).
- `internal/policy`: `effectiveTeamBudget` — бюджет орги каппирует команду (включая безлимитную); спенд команды считается против капа.
- `016_org_budget.sql` — organizations += budget_usd (верхняя граница бюджетов команд).

### Fixes
- compose: CONTROL_PLANE_MASTER_KEY был обрезан до 26 байт (не 32) — падали все крипто-тесты сюита; канонические 44 символа base64.
- Тесты контроль-плейна доведены до зелёных в контейнере (171/171): codex-quota фикстуры с протухшей датой reset-кредита → относительные даты; snapshot-envelope/security собирали неполные схемы → полная миграция в изолированной per-pid схеме; SSRF-тест требует ALLOW_PRIVATE_UPSTREAMS=false при прогоне.

### Edition gate
- `internal/edition` — one binary, three editions (`oss|cloud|ent`) via `2PAPI_EDITION` env or signed `2papi.license`; unknown values degrade to OSS so stray env can never unlock paid paths.
- `internal/license` — Ed25519 offline validation (`prefix:b64payload.b64sig`, prefix inside signature); env-overridable trusted pubkey (`2PAPI_LICENSE_PUBKEY`); no network — air-gap safe; expired/not-yet/foreign-key/garbage all degrade to OSS. Spec: plan/build-spine-specs.md шаг 1. Decision + rationale: plan/2papi-3-editions-strategy.md.

### Schema (control-plane migrations)
- `011_users_rbac.sql` — self-serve `users` (lower(email) unique, platform_role user/operator), `user_sessions` (hashed tokens), `team_members` (owner/member), teams += `trust_tier` (0–2) and `status`.
- `012_credit_ledger.sql` — prepaid `credit_transactions` per team with idempotency `UNIQUE(source, external_id)` (partial: manual adjustments exempt), teams += `balance_usd` (updated in same tx as ledger insert).
- `013_model_source_overrides.sql` — multi-provider aliases: `model_account_mappings` += `upstream_model_override`, `weight`, per-source input/output costs; empty override inherits alias defaults (back-compat).
- `014_tier_policies.sql` — free-tier per-model daily token limits (`tier × model_alias_id`), operator-editable, published via config snapshot.

### Decisions
- License: Apache-2.0 for the OSS edition (patent grant matters for adoption).
- Free tier supply = owner's own sources + commercially-permitted free lanes; provider free quotas (Google/Cerebras) excluded by their ToS (resell/proxy prohibited).

## 2026-08-20 — Provider adapters, quota, TUI, plugins, hostname, DeepSeek

### Speed (Bifrost-style)
- `internal/policy` — 16-way sharded `Auth` (was single `sync.Mutex`); ~10ns key pick at 5k RPS; team-budget moved to shared `teamMu` (fixes cross-key team rollup).
- `internal/resilience` — read paths on `RWMutex` (`Cooling/Latency/Active/...`).
- `internal/server` — request ID from `atomic.Uint64+time` (was `crypto/rand`); always regenerates, never trusts client `X-Request-ID`.
- `internal/compression` — command-aware RTK: `git diff` 10+10, `git log` 8+8, `cargo/npm/go test` keeps only failures, `grep` truncate.
- Plugin hooks (`BeforeRequest/AfterResponse`) non-fatal with 10ms sidecar budget → TTF preserved.

### DeepSeek optimization (own approach)
- `internal/adapter/deepseek` — thinking-aware OpenAI-compatible adapter; SSE streams `reasoning_content` then `content` 1:1 so thinking never blocks first content (fast TTF); model id rewritten per chunk; `discover_models` supported.
- `internal/compression` — `CompressReasoning` (CoT head/tail per `reasoning_effort` low/high), DeepSeek CJK token estimate.

### Providers (OmniRoute-style, no 9Router weight)
- `internal/adapter/thirdparty` — lightweight OpenAI-compatible adapters:
  - Free/no-auth: `opencode`, `felo`, `qoder` (credential kind `free`, 0 secrets).
  - OAuth subscription: `cursor`, `copilot`, `kimi` — Bearer + single-flight refresh via `oauthrefresh.Manager`, 401→refresh→retry.
- `internal/adapter/anthropic` — `claude-code` CLI headers (`anthropic-beta …claude-code-20250219`, `anthropic-billing-source: claude_code`).
- `internal/adapter/codex` — official `codex` CLI headers (`X-OpenAI-Client`, `X-Stainless-*`).
- `internal/config` — accepts `deepseek`, `opencode/free/felo/qoder` (kind free/api_key), `cursor/copilot/kimi` (oauth/free/api_key).

### Quota (dashboard contract)
- `internal/quota` — per-account tracker: `Observe` (X-Provider-Quota-*/codex credits), `ObserveRaw` (primary/secondary windows), `Summary` (Σused/Σlimit), `List` (sorted), family detection.
- `internal/proxy` — observes quota headers on every upstream response; tracker survives snapshot adoption.
- `GET /api/quota` (gateway) and `GET /api/control/v1/quota` (control-plane, from `request_events` + providers) — same shape for the Quota widget/bar.
- `internal/cache` — `SetWithRequest` stores RequestHash+Words, `FindSimilar` Jaccard semantic cache, `Stats` (hit_rate/exact/similar/misses), disk `SaveToFile/LoadFromFile`.

### Interactive (like 9router) + plugins (like dsh, pragmatic)
- `2papi tui` — keyboard menu (`Start/Dashboard/2papi.local/config/Quit`), raw `x/term`, line-menu fallback off-TTY; `2papi init` — interactive enable for `2papi.local`.
- `internal/hosts` — cross-platform hosts add/remove (Windows `%SystemRoot%`, `/etc/hosts`), idempotent, sudo hint.
- `internal/mdns` — pure-Go `2papi.local` over mDNS/Bonjour (zeroconf): `2papi advert` keeps it alive, `2papi --mdns` advertises at gateway start, `2papi init` asks mDNS (preferred) or hosts. Note: on Docker Desktop/Windows host-network multicast may be unavailable (`bad rdata` probe logs are harmless — falls back to hosts).
- `internal/plugin` — Registry with `BeforeRequest/AfterResponse/Compress` in-process hooks + HTTP sidecar (`endpoint/before|after`, 10ms timeout, non-fatal), config-declared `plugins:` in YAML; `GET /api/plugins`.

### Misc
- Single-binary Dockerfile (`CGO_ENABLED=0`, `-s -w`, distroless, ~11MB); Free/DeepSeek/OAuth presets in `config/example.yaml`; `install.sh/ps1` mention tui/init; `docs/benchmarks.md` DeepSeek fast-TTF + policy/cache rows; control-plane `lib/quota.ts` + test.

Earlier backlog (this session): grok-pool cut, RTK/Caveman/Headroom per-model/per-key, lite mode without Postgres, semantic cache, open-design dashboard plan (quota bar + `/quota` tab + widgets).
