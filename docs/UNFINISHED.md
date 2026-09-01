# 2papi — недоделанное / что дальше

Дата: 2026-08-28. Этот файл — честная карта незавершённого. План-документ с
ресёрчем гэпов: `C:\Users\Xiaomi\.opencode\plan\2papi-gap-research-2026-08-28.md`.

## В работе (optimization-modes, витки 1-8 закрыты к 2026-09-02)

| Виток | Статус | Что осталось |
|---|---|---|
| 1-3 (Go-ядро: типы, пресеты, auto-эвристики) | ✅ сделано | — |
| 4 (control-plane RoutingSchema + snapshots) | ✅ сделано, commit `c3579ed` | — |
| 5 (дашборд: селекты режимов + i18n) | ✅ сделано, commit `475a654` | селекты rtk_mode/caveman_mode/headroom_profile/squoze (Auto первой, disabled без тумблера), mutateOptimization, ключи en/ru |
| 6 (доки + полный QA) | ✅ сделано | README секция Optimization; CHANGELOG; go test ./... + npm test + tsc + next build зелёные |
| 7 (single-pass пайплайн) | ✅ сделано, commit `023efcd` | `compression.OptimizeRequest` — headroom+RTK+caveman одним parse/marshal; squoze остался эксклюзивным |
| 8 (бенчмарки) | ⏳ **не сделано** | замеры overhead по режимам (было → стало) на test/bench.mjs |
| 9 (cache-economics governor B3 + gateway_retrieve LRU) | ⏳ **не сделано** | объединить с G4 (exact response cache) — per-model `cache: off\|exact` + TTL + LRU-эвикция |

## Гэпы из gap-ресёрча (порядок внедрения)

- **G1** virtual keys: `budget_duration` (месячное окно сброса), — per-key
  RPM/TPM enforcement уже есть в policy.go (G1 закрыт частично, остался
  только budget_duration) — блокирует Cloud self-serve
- **G2** MCP tool pinning (rug-pull detection: хэш tools/list при регистрации, audit + опциональный block) — Ent-дифференциатор
- **G3** OTel GenAI emission (минимальный набор атрибутов, OTLP exporter по env)
- **G4** exact-match response cache (per-model `cache: off|exact`, TTL); semantic cache — только Cloud/Ent
- **G5** guardrails реализовать (сейчас только флаг в лицензии): regex-PII + injection-эвристики, режим block|redact|log, обязателен eval на false positives
- **G6** adaptive routing — ✅ ЗАКРЫТ: стратегии cheapest/fastest/adaptive уже на месте (router.go)
- **G7** `/v1/embeddings` pass-through + метеринг — ✅ ЗАКРЫТ (server.go:158 + тест)
- **G8** squoze question-aware elision — отдельный ресёрч-виток (не копировать LLMLingua — ломает zero-dep)

## Известные долги/баги

- **18 фейлов control-plane интеграционных тестов** (auth-flow/SSO/orgs/billing):
  «Cannot read properties of undefined (reading 'id')» — воспроизводится на
  чистом `eec6cf6`, pre-existing, не связаны с optimization-modes. Копать:
  `tests/auth-flow.integration.test.ts` — провижининг team/key после signup.
  ⚠️ На Windows-машине без Docker не воспроизводятся (контроль-плейн npm test:
  135 pass / 80 skip без TEST_DATABASE_URL).
- **compose.yaml postgres порт** — ✅ ЗАКРЫТ: добавлен `127.0.0.1:5432:5432`.
- **`.enola/`** — ✅ отсутствует.
- **`cmd/gateway` не компилировался** (pre-existing): RunTUI/RunInit/
  AdvertMDNS/defaultHostname вызваны, но не определены; `.gitignore`-паттерн
  `gateway` игнорировал всю директорию cmd/gateway. ✅ ПОЧИНЕНО: interactive.go
  + root-only-паттерны в .gitignore (44e16c9, 447c8e5).
- **squoze root-обёртка не публична**: go.mod `replace => ../squoze`, но Git-
  репозиторий v0.1.1 не содержит корневого пакета `github.com/Rethinger/squoze`
  (только internal/). CI (`go test -race ./...`) собирает ./... — падает без
  локального `../squoze` с root-файлом. Нужно: добавить root-обёртку в
  squoze-репо и тег/коммит, либо заменить импорт на внутренний пакет.

## Идеи на следующий раунд gap-ресёрча

Multi-region/durability, unit-economics PLG (pricing), протокол A2A, SOC2-путь для Ent.