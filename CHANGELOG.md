# Changelog — 2papi

Format: decisions and notable additions, newest first. See docs/ for deep dives.

## 2026-08-24 — Optimization mode presets (виток 3/9: умный авто)

### Авто-эвристики (`auto.go`)
- **RTK auto решает ПО БЛОКУ** (`AutoRTKParamsForBlock`): <4k символов → light/пропуск, 4–32k → standard, >32k → aggressive. Размер блока стабилен между ходами → решение стабильно → промпт-кэш провайдера не страдает (deep-gap id=78).
- **Headroom auto** (`AutoHeadroomProfile`): оценка <50% резерва → noop вовсе (долгие кэш-дружелюбные эпохи), ≤90% → conservative, выше → aggressive.
- **Caveman auto**: агенты (tools/tool_calls/tool_result в теле) → full-директива, обычный чат → lite.
- Идемпотентность RTK зафиксирована тестом: второй прогон по уже сжатому телу = no-op (маркер элизии → skip).
- E2E-режимы в proxy: `X-Gateway-Compress: auto` / `X-Gateway-Caveman: auto` / headroom_profile=auto резолвятся перед выполнением и эхоют КОНКРЕТНЫЙ режим.

## 2026-08-24 — Optimization mode presets (витки 1-2/9)

### Proxy wiring + эхо-заголовки режимов
- Три блока оптимизаций в `proxy.go` переведены на `Decide*` (каскад + режим); `auto` пока маппится в легаси-дефолт (per-request эвристики — виток 3).
- Эхо при применении: `X-Gateway-RTK-Mode`, `X-Gateway-Caveman-Mode`, `X-Gateway-Headroom-Profile` — видно, какой режим реально сработал.
- Fast-path RTK теперь по `params.MinBytes` режима (aggressive 1024Б, light 8192Б).
- Старые `Should*` остались как тонкие обёртки над Decide* (легаси-API без дрейфа семантики).
- E2E: aggressive сжимает 2KB tool_result и эхоит режим; lite-директива уходит апстриму; заголовок `light` не трогает малое тело, `false` выключает. Дерево 0 FAIL.

### Go-ядро: режимы для RTK / Caveman / Headroom
- `config.Optimization`: `rtk_mode` (light|standard|aggressive|auto), `caveman_mode` (lite|full|auto), `headroom_profile` (conservative|balanced|aggressive|auto); пусто = прежнее поведение. Валидация в `Build()` для global/model/vk — опечатка падает на старте.
- Каскад как у булов: header > vk > model > global; заголовки принимают имена режимов (`X-Gateway-Compress: aggressive`) вместе с true/false, мусор игнорируется.
- Пресеты RTK: light 8192Б/40+40 без командных пресетов; standard как было; aggressive 1024Б/8+8.
- Headroom-профили: conservative keep=16/150k, aggressive keep=4/80k; явные reserve/keep сильнее профиля.
- Caveman lite: короткая директива с полным набором safety-карвов (security/irreversible/multi-step).
- **Идемпотентность RTK**: блок с маркером элизии повторно не сжимается никогда (глубже — защита промпт-кэша провайдера, research id=78).
- Тесты: каскад приоритетов, пресеты, профили+оверрайды, safety-клаузы lite, идемпотентность (второй прогон = no-op), валидация мусорных режимов. Дерево: 0 FAIL.

## 2026-08-23 — Payment loop + E1 showcase

### Гейтинг закрыт: audit_export + ipacl (Ent)
- `audit-export` (NDJSON) прикрыт флагом `audit_export` — раньше отдавался всем hosted-операторам, что нарушало собственный гейтинг.
- **ipacl** — новая Ent-фича: allowlist IPv4 CIDR на control API. Хранение в `system_settings` ('ipacl'), настройка `POST /api/control/v1/ipacl` (`requireFeature('ipacl')`), enforcement `assertIpacl()` на входе GET/POST/PATCH/DELETE.
- Семантика: проверяется только X-Forwarded-For (трафик через прокси); прямые/локальные запросы без XFF не блокируются — защита от self-lockout; OSS не исполняет ACL даже при наличии списка.
- Коды: `ip_not_allowed` 403, `invalid_cidrs` 400 (каталог дополнен).
- Тесты: юнит CIDR-матчинга (диапазоны, host-bits, /0, IPv6 literal) + интеграция гейтинга. Сюит 206 → **212**.

### /status — публичный статус-эндпоинт
- `GET /status` на гейтвее: version (ldflags), uptime_seconds, счётчики accounts total/enabled/cooling, models, mcp_servers, config_version. Без секретов; только GET.
- Основа для внешнего status page (бэклог Cloud Фазы 2). Тест: публичность/отсутствие секретов/405 на POST.

### JWT-auth для API (Ent)
- `POST /api/auth/token`: операторская сессия → короткоживущий HS256 JWT (1ч, `API_TOKEN_TTL_SECONDS`) для программного доступа к `/api/control/v1/*`.
- `requireOperator` принимает Bearer JWT наравне с cookie; проверка подписи/aud/exp (30s skew), только HS256 (alg-swap отвергается).
- Тесты: юнит (roundtrip/tamper/expiry/alg-confusion) + интеграция (токен → мутация 200, мусор → 401, тенант → 403).

### Rate limiting auth-эндпоинтов
- `lib/rate-limit.ts` — фиксированное окно per key (ip+маршрут), ленивая чистка, лимит 0 = эндпоинт закрыт.
- signup 5/час/IP · login 10/час/IP · verify 20/час/IP (`SIGNUP_RATE_LIMIT`, `LOGIN_RATE_LIMIT`, `VERIFY_RATE_LIMIT`); 429 rate_limited при исчерпании.
- Тесты: юнит (окно/изоляция/сброс/ноль) + интеграция 429 на третьем signup и логине с одного IP; другой IP не затронут.

### Fix: hosted control mutations require operator session
- Адверсариальный аудит нашёл дыру: в cloud/ent издании мутации `/api/control/v1/*` (включая money-path `billing/adjust`) принимались без аутентификации.
- `requireOperator(req, db)`: hosted → сессия с platform_role='operator' обязательна на все методы catch-all; OSS не тронут (локальный тул остаётся открытым, DX сохранён).
- Тесты переведены на операторскую сессию (organizations/sso/auth-flow).

### Tenant billing (страница + API тенанта)
- `GET /api/auth/billing` — по сессии: баланс/леджер/ключи ТОЛЬКО своей команды (+ checkout_url из env); hosted-only, 401 без сессии.
- Страница `/tenant-billing`: баланс (красный при нуле), кнопка «Add credits» из PADDLE_CHECKOUT_URL, список ключей с копированием префикса, история кредитов.
- Тест: чужие команды и транзакции не утекают; аноним → 401.

### Fix: X-Gateway-Overhead-MS считал весь апстрим
- Заголовок показывал полное время запроса (включая ожидание провайдера), а не добавленную гейтвеем задержку — на живом Fireworks overhead=upstream≈1368ms при wall ~1.4s.
- Семантика Ferro: overhead = elapsed − upstream_ms (floor 0). Живо проверено: overhead 0–1ms, upstream 1368ms. Усилен тест latency headers (sleep 30ms у апстрима + assert overhead<upstream).

### Benchmark (Ferro-методология, воспроизводимый)
- `docker compose --profile bench up --build bench-runner` — фиксированный локальный fake-upstream (без сети провайдера), тиры конкурентности, отчёт RPS + TTFB p50/p95/p99 + self-reported overhead (`X-Gateway-Overhead-MS`).
- Замер на Windows/Docker Desktop: 408/1797/2419 RPS @10/50/100 conc, TTFB p50 3–20ms, overhead гейтвея avg 0.02–0.31ms, 37k+ запросов без ошибок. Скрипт `test/bench.mjs`, конфиг `config/bench.yaml`, профиль `bench` в compose.

### Стриминг: публичный алиас в чанках
- `protocol.NewSSEModelRewriteReader` — построчная перезапись upstream model id → публичный алиас в OpenAI-SSE стриме; построчный буфер безопасен при разрезании JSON по сетевым чанкам, остальное — дословно.
- Включён для OpenAI-формата когда alias ≠ upstream (non-streaming уже нормализовался через pipe-path). Проверено живьём на Fireworks deepseek-v4-flash: чанки несут `model: <алиас>`.
- Тесты: компактная/пробельная форма, разрезанная строка, bypass при совпадении имён.

### MCP servers CRUD (контроль-плейн)
- Миграция 018 `mcp_servers` (name unique, url, enabled, headers_secret_id → secret_records).
- CRUD в catch-all: GET (без значений заголовков, только флаг headers_set), POST/PATCH (headers шифруются envelope'ом через insertSecret, ротация = новая запись), DELETE; дубликат имени → 409.
- Снапшот: декларативная часть несёт только name/url/enabled (credential-free инвариант сохранён — проверено тестом); runtime-материализация инжектит расшифрованные заголовки. Поле пропадает, когда серверов нет.
- Тест mcp-crud.integration.test.ts: CRUD + ротация + отсутствие утечек в GET и stored snapshot.

### Paddle webhook
- `POST /api/webhooks/paddle`: HMAC-SHA256 подпись (`ts=…;h1=…`, окно 5 мин) → transaction.completed → идемпотентный топап: `credit_transactions` UNIQUE(source, external_id) + `balance_usd += delta` в одной транзакции + audit_event.
- Реплеи безопасны (ack без двойного зачисления), forged/stale подписи → 401; не-USD/не-paid/без team_id → 422; без `PADDLE_WEBHOOK_SECRET` → 503; hosted-only.
- Тесты: зачисление+реплей+второй платёж+аудит; подделки; валюты/статусы; гейты.

### Платёжный контур (шаг 6, «Платежи»)
- Декремент `teams.balance_usd` в той же транзакции ingest'а request_events (только команды с балансом > 0; неуспешные запросы бесплатны).
- Ночной reconcile (`lib/balance.ts`, `BALANCE_RECONCILE_INTERVAL_MS`, 0 = выкл): баланс пересчитывается из леджера − успешный спенд; расхождение с живым значением — алерт в лог. Планировщик подключён через instrumentation.
- Тест: спенд 0.5 → баланс 1.5; порча → reconcile восстанавливает; failed-запросы не списывают.

### E1-витрина
- CI: контроль-плейн теперь гоняет ПОЛНЫЙ сюит (190) с Postgres-сервисом и ALLOW_PRIVATE_UPSTREAMS=false — раньше только юнит-подмножество без БД.
- README: позиционирование против LiteLLM (<5ms vs ~8ms p95), три издания одной таблицей, семантический кэш/MCP/sources[]/SSO в фичах.
- SECURITY.md (репортинг, scope, hardening-чеклист) + CONTRIBUTING.md (правила: шаг=коммит, гейты, миграции append-only).

## 2026-08-22 — Cloud edition foundation

### MCP gateway (хребет, после шага 6)
- `POST /v1/mcp/<server>` — streamable-HTTP JSON-RPC passthrough к настроенным upstream MCP-серверам; за виртуальными ключами: бюджеты/RPM/конкарренси действуют на tool-calls как на модельный трафик.
- Конфиг из файла (`mcp_servers: [{name,url,enabled?,headers}]`) — ноль обязательного контроль-плейна, DX энтузиаста; заголовки несут креды апстрима (тот же уровень доверия, что api_key аккаунтов).
- Ответ апстрима идёт клиенту дословно (SSE-стримы проходят); tool-call пишется в request_events (endpoint `/v1/mcp/<server>`, без токенов).
- Валидация: уникальные имена, http(s) URL; неизвестный/выключенный сервер → 404. Тесты: forwards/headers/телеметрия, unknown/disabled/auth, бюджет 429.

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
