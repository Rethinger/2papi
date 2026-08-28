# 2papi — недоделанное / что дальше

Дата: 2026-08-28. Этот файл — честная карта незавершённого. План-документ с
ресёрчем гэпов: `C:\Users\Xiaomi\.opencode\plan\2papi-gap-research-2026-08-28.md`.

## В работе (optimization-modes, 9 витков — сделаны 1-4)

| Виток | Статус | Что осталось |
|---|---|---|
| 1-3 (Go-ядро: типы, пресеты, auto-эвристики) | ✅ сделано | — |
| 4 (control-plane RoutingSchema + snapshots) | ✅ сделано, commit `c3579ed` | — |
| 5 (дашборд: селекты режимов + i18n) | ⏳ **не сделано** | `dashboard-client.tsx`: селекты rtk_mode/caveman_mode/headroom_profile/squoze, disabled при выключенном тумблере, «Auto» первой опцией; `i18n.ts` ключи en/ru |
| 6 (доки + полный QA) | ⏳ **не сделано** | README секция Optimization (таблица режимов, авто, заголовки/yaml); CHANGELOG; полный прогон go test + npm test + сборка |
| 7 (single-pass gjson/sjson пайплайн) | ⏳ **не сделано** | слияние RTK/caveman/headroom/squoze в один проход по телу |
| 8 (бенчмарки) | ⏳ **не сделано** | замеры overhead по режимам (было → стало) |
| 9 (cache-economics governor B3 + gateway_retrieve LRU) | ⏳ **не сделано** | объединить с G4 (exact response cache) |

## Гэпы из gap-ресёрча (порядок внедрения)

- **G1** virtual keys: `budget_duration` (месячное окно сброса), per-key RPM/TPM enforcement в policy.go — блокирует Cloud self-serve
- **G2** MCP tool pinning (rug-pull detection: хэш tools/list при регистрации, audit + опциональный block) — Ent-дифференциатор
- **G3** OTel GenAI emission (минимальный набор атрибутов, OTLP exporter по env)
- **G4** exact-match response cache (per-model `cache: off|exact`, TTL); semantic cache — только Cloud/Ent
- **G5** guardrails реализовать (сейчас только флаг в лицензии): regex-PII + injection-эвристики, режим block|redact|log, обязателен eval на false positives
- **G6** adaptive routing: `strategy: cheapest|least_latency` на модели
- **G7** `/v1/embeddings` pass-through + метеринг
- **G8** squoze question-aware elision — отдельный ресёрч-виток (не копировать LLMLingua — ломает zero-dep)

## Известные долги/баги

- **18 фейлов control-plane интеграционных тестов** (auth-flow/SSO/orgs/billing):
  «Cannot read properties of undefined (reading 'id')» — воспроизводится на
  чистом `eec6cf6`, pre-existing, не связаны с optimization-modes. Копать:
  `tests/auth-flow.integration.test.ts` — провижининг team/key после signup.
- **compose.yaml**: postgres не публикует порт 5432 на хост → интеграционные
  тесты на хосте требуют временного контейнера (`docker compose run -p 15432:5432`).
  Добавить `ports: ["127.0.0.1:5432:5432"]` под dev-профиль.
- **`.enola/`** — untracked мусор в корне (не пушить).
- **Нет ветки main на origin** — активная ветка `autoresearch/session-20260821`.

## Идеи на следующий раунд gap-ресёрча

Multi-region/durability, unit-economics PLG (pricing), протокол A2A, SOC2-путь для Ent.