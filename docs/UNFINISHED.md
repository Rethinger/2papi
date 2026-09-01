# 2papi — недоделанное / что дальше

Дата: 2026-08-28. Этот файл — честная карта незавершённого. План-документ с
ресёрчем гэпов: `C:\Users\Xiaomi\.opencode\plan\2papi-gap-research-2026-08-28.md`.

## В работе (optimization-modes, витки 1-7 закрыты к 2026-09-02)

| Виток | Статус | Что осталось |
|---|---|---|
| 1-3 (Go-ядро: типы, пресеты, auto-эвристики) | ✅ сделано | — |
| 4 (control-plane RoutingSchema + snapshots) | ✅ сделано, commit `c3579ed` | — |
| 5 (дашборд: селекты режимов + i18n) | ✅ сделано, commit `475a654` | — |
| 6 (доки + полный QA) | ✅ сделано, commit `2d36bbd` | — |
| 7 (single-pass пайплайн) | ✅ сделано, commit `023efcd` | — |
| 8 (бенчмарки) | ⏳ **не сделано** | замеры overhead по режимам (было → стало) на test/bench.mjs — нужен Docker-стенд (локально недоступен) |
| 9 (cache-economics governor + LRU) | ✅ сделано частично (commit `b08a7cc`): LRU-эвикция + per-model exact cache (G4) | governor B3 (экономика кэша по стоимости) не реализован — отдельный дизайн-виток |

## Гэпы из gap-ресёрча (статус на 2026-09-02)

- **G1** budget_duration — ✅ (commit `144b2f6` + миграция 019): месячное
  окно сброса в policy, zod/CRUD/снапшот/UI-селект; RPM/TPM были уже.
- **G2** MCP tool pinning — ✅ (commit `92d21c0`): sha256+имена tools/list,
  audit (registered/changed/blocked), опциональный block 409 при
  `pin_tools: true`.
- **G3** OTel GenAI — ✅ (commit `01956ac`): otlptracehttp exporter по env,
  gen_ai.* атрибуты, OTEL_SDK_DISABLED, во всех режимах cmd/gateway.
- **G4** exact-match response cache — ✅ (commit `540e174` + миграция 020):
  per-model `cache: off|exact` + `cache_ttl`, LRU-эвикция. Semantic cache —
  только Cloud/Ent (не менялось).
- **G5** guardrails — ✅ (commit `dc5dac4`): regex-PII (email/phone/card/key)
  + injection-эвристики, режимы log|redact|block, eval на false positives.
  Ent-гейтинг премиум-пресетов — follow-up.
- **G6** adaptive routing — ✅ закрыт (cheapest/fastest/adaptive на месте).
- **G7** /v1/embeddings — ✅ закрыт (server.go:158 + тест).
- **G8** squoze question-aware elision — отдельный ресёрч-виток (не копировать
  LLMLingua — ломает zero-dep).

## Известные долги/баги

- **18 фейлов control-plane интеграционных тестов** (auth-flow/SSO/orgs/billing):
  «Cannot read properties of undefined (reading 'id')» — pre-existing,
  воспроизводится на чистом `eec6cf6`. ⚠️ Требует Docker/Postgres
  (на Windows-машине без Docker: npm test 135 pass / 80 skip). Копать:
  `tests/auth-flow.integration.test.ts` — провижининг team/key после signup.
- **compose.yaml postgres порт** — ✅ ЗАКРЫТ (`127.0.0.1:5432:5432`).
- **`.enola/`** — ✅ отсутствует. **ветка main** — ✅ origin/master есть.
- **`cmd/gateway` не компилировался** (pre-existing) — ✅ ПОЧИНЕНО
  (interactive.go, .gitignore root-only-паттерны, CRLF-устойчивый checksum;
  commits 44e16c9, 447c8e5).
- **squoze root-обёртка не публична**: go.mod `replace => ../squoze`, а Git-
  репозиторий v0.1.1 не содержит корневого пакета (только internal/).
  CI (`go test -race ./...`) падает без локального `../squoze` с root-файлом.
  Нужно: root-обёртку в squoze-репозиторий + тег, либо импорт внутреннего
  пакета (сейчас под go1.22 internal-импорт невозможен извне модуля).
- **otlp-зависимости**: go.mod вырос (otel sdk v1.31 + grpc/protobuf
  транзитивно) — бинарь тяжелее; если критично, заменить на самописный
  OTLP-HTTP клиент.

## Идеи на следующий раунд gap-ресёрча

Multi-region/durability, unit-economics PLG (pricing), протокол A2A, SOC2-путь для Ent.