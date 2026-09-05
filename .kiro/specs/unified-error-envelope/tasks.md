# Tasks — Unified Error Envelope

Требования: [requirements.md](requirements.md).

## Фаза 1 — пакет `internal/apierr`

- [ ] **TSK-501**: коды и таблица
  - Requirement: FR-1, FR-2
  - Deliverables: `internal/apierr/apierr.go` — тип `Code`, 11 констант,
    таблица `Code → {HTTPStatus, Type}`, `Format` (`openai|anthropic`)
  - Acceptance: AC-1.1 — тест перебирает все константы и проверяет, что у каждой
    есть статус и тип, и что дублей статуса+типа нет там, где их быть не должно

- [ ] **TSK-502**: `Write` в двух форматах
  - Requirement: FR-2, FR-7
  - Deliverables: `internal/apierr/write.go` — OpenAI-конверт и Anthropic-конверт,
    `request_id`, `Retry-After` для `rate_limited`
  - Acceptance: AC-2.1, AC-2.2; golden-тесты на оба формата

- [ ] **TSK-503**: классификатор
  - Requirement: FR-4, NFR-2
  - Deliverables: `internal/apierr/classify.go` — `Classify(status, body, contentType)`,
    лимит 4 KiB, ветки по `error.type`/`error.code`/подстрокам, дефолты для 4xx/5xx
  - Acceptance: AC-4.1, AC-4.2; табличный тест с телами пяти провайдеров, включая
    не-JSON HTML и китайский текст

- [ ] **TSK-504**: бюджет классификации
  - Requirement: NFR-1
  - Deliverables: `internal/apierr/classify_bench_test.go`
  - Acceptance: измеренная цифра в комментарии к бенчу; >50 µs — переработать,
    а не задокументировать

## Фаза 2 — шлюз: перехват и подмена

- [ ] **TSK-505**: `proxy.Error` через `apierr`
  - Requirement: NFR-3, FR-2
  - Deliverables: `internal/proxy/proxy.go:120-126` — обёртка над `apierr.Write`;
    `code` в теле становится строкой
  - Acceptance: `go build ./...` без правки 82 вызовов; существующие тесты шлюза
    зелёные (ожидания на числовой `code`, если найдутся, обновить в этом же коммите)

- [ ] **TSK-506**: перехват апстримных 4xx
  - Requirement: FR-3
  - Deliverables: `internal/proxy/proxy.go` — ветка `result.Status >= 400` в `try`
    до путей `pipe`/`rewriteResponseModel`; апстримные заголовки тела не пересылаются
  - Acceptance: AC-3.1, AC-3.2; тест на `test/fakeupstream` с 403 и телом на китайском

- [ ] **TSK-507**: канонические коды на внутренних отказах
  - Requirement: FR-1
  - Deliverables: миграция значимых вызовов — `proxy.go:240` (guardrail),
    `485` (rate limit), `502` (нет аккаунта), `699` (invalid json),
    `server.go:236-407` (405/401/400/404/403/503/501)
  - Acceptance: у каждого перенесённого места в теле осмысленный `code`, а не
    `gateway_internal`

- [ ] **TSK-508**: худшая причина при исчерпании попыток
  - Requirement: FR-5
  - Deliverables: `internal/proxy/proxy.go` — накопление причины в цикле,
    замена `Error(w, 502, "all upstream attempts failed")` на `apierr.Write`
  - Acceptance: AC-5.1; тест на два аккаунта с 429

## Фаза 3 — диагностика

- [ ] **TSK-509**: сырое в лог и трейс
  - Requirement: FR-6
  - Deliverables: структурированная строка лога в `try`; в `internal/telemetry/otel.go`
    атрибуты `2papi.error.code`, `2papi.upstream.status`, `2papi.upstream.error` и
    `SetStatus` с кодом
  - Acceptance: AC-6.1, AC-6.2

- [ ] **TSK-510**: причина в попытках
  - Requirement: FR-6
  - Deliverables: `internal/telemetry/telemetry.go` — `ErrorCode` и
    `UpstreamMessage` (<=512 B) в `Attempt`; заполнение в цикле попыток
  - Acceptance: событие телеметрии содержит код на каждой неудачной попытке;
    дашборд-виджет попыток не ломается на новых полях

## Фаза 4 — контракт и проверка

- [ ] **TSK-511**: документация кодов
  - Requirement: FR-1, ограничения
  - Deliverables: раздел в README (или `docs/errors.md`) — таблица код → статус →
    когда бывает → что делать клиенту
  - Acceptance: все 11 кодов описаны; сказано, что набор закрыт

- [ ] **TSK-512**: проверка на живом провайдере
  - Requirement: FR-3, FR-4
  - Deliverables: расширение `test/provider_probe.mjs` (или отдельный
    `test/error_probe.mjs`) — снять реальные ошибки crax (невалидный ключ,
    неизвестная модель, переполненный контекст) и записать в `test/results/`
  - Acceptance: отчёт показывает канонический код на каждую вызванную ошибку;
    ключ передаётся только через env (`PROBE_KEY`/`CRAX_KEY`), в отчёт не попадает

## Dependency graph

```text
TSK-501 ─→ TSK-502 ─┬─→ TSK-505 ─→ TSK-506 ─→ TSK-507 ─→ TSK-508 ─→ TSK-511
TSK-503 ────────────┘        └─→ TSK-509 ─→ TSK-510      └─→ TSK-512
TSK-504 (после TSK-503)
```

## Progress

| Задача | Статус |
|---|---|
| TSK-501 коды и таблица | Pending |
| TSK-502 `Write` в двух форматах | Pending |
| TSK-503 классификатор | Pending |
| TSK-504 бюджет классификации | Pending |
| TSK-505 `proxy.Error` через `apierr` | Pending |
| TSK-506 перехват апстримных 4xx | Pending |
| TSK-507 коды на внутренних отказах | Pending |
| TSK-508 худшая причина | Pending |
| TSK-509 сырое в лог и трейс | Pending |
| TSK-510 причина в попытках | Pending |
| TSK-511 документация кодов | Pending |
| TSK-512 проверка на живом провайдере | Pending |
