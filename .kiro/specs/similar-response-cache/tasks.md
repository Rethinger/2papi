# Tasks — Similar Response Cache (`cache: similar`)

Требования: [requirements.md](requirements.md). Quick Spec — approval-гейтов между
фазами нет, но порядок фаз обязателен: сначала библиотека кэша, потом шлюз, потом
конфиг и доки.

## Фаза 1 — исправить сам `FindSimilar`

- [ ] **TSK-401**: изоляция по модели
  - Requirement: FR-2
  - Deliverables: `internal/cache/cache.go` — поле `Model` в `Entry`,
    заполнение в `SetWithRequest`, фильтр в `FindSimilar`
  - Acceptance: AC-2.1; новый тест `TestFindSimilarModelIsolation` (одинаковый
    текст, две модели → промах)

- [ ] **TSK-402**: гейт применимости и минимум слов
  - Requirement: FR-3
  - Deliverables: `internal/cache/cache.go` — `similarEligible(body []byte) ([]string, bool)`
    рядом с `requestUserWordList` (один разбор тела: `tools`, роли, число сообщений,
    число значимых слов ≥ 8); `FindSimilar` вызывает его первым и на `false`
    возвращает промах
  - Acceptance: AC-3.1, AC-3.2; тесты на четыре отказа — есть `tools`,
    есть `role: tool`, >2 сообщений помимо `system`, <8 значимых слов

- [ ] **TSK-403**: детерминированный тайбрейк
  - Requirement: FR-3 (edge case «два кандидата с одинаковым Jaccard»)
  - Deliverables: `internal/cache/cache.go` — при равном score выбирается
    лексикографически меньший ключ
  - Acceptance: 20 прогонов одного и того же поиска по двум равным кандидатам
    дают один и тот же ключ

- [ ] **TSK-404**: один исход на запрос (`Lookup`)
  - Requirement: FR-5
  - Deliverables: `internal/cache/cache.go` — неучитывающий `get(key)`;
    `Lookup(model string, body []byte, threshold float64) (Entry, Kind, float64, bool)`
    (`Kind`: `exact|similar|miss`), который делает точный поиск, при промахе —
    похожий, и записывает **ровно один** исход; `FindSimilar` теряет
    `noteHit`/`noteMiss`; `Get` accounting сохраняет (совместимость с тестами)
  - Acceptance: AC-5.1; `TestSemanticFindSimilar` переносит проверку
    `SimilarHits` на `Lookup` — и его фикстуру нужно удлинить, сейчас запрос
    даёт ровно 8 значимых слов, то есть тест сидел бы на границе гейта TSK-402

## Фаза 2 — проводка в шлюзе

- [ ] **TSK-405**: похожий поиск на пути запроса
  - Requirement: FR-1, FR-6
  - Deliverables: `internal/proxy/proxy.go` — в блоке `if wantCache`
    (`~427-462`) `Get` заменяется на `Lookup` с порогом из конфига; путь записи
    (`~561`) не меняется
  - Acceptance: AC-1.1 (регрессия `exact`), AC-6.1; существующие
    `internal/proxy/cache_compress_test.go` зелёные без правок

- [ ] **TSK-406**: заголовки и телеметрия похожего хита
  - Requirement: FR-4
  - Deliverables: `internal/proxy/proxy.go` — `X-Gateway-Cache: HIT-SIMILAR`,
    `X-Gateway-Cache-Score`; outcome `cache_similar` там же, где сейчас пишется
    исход точного хита
  - Acceptance: AC-4.1; тест end-to-end: первый запрос MISS, похожий второй —
    `HIT-SIMILAR` со score в (0,1]

## Фаза 3 — конфиг

- [ ] **TSK-407**: `cache: similar` + `cache_similar_threshold`
  - Requirement: FR-1
  - Deliverables: `internal/config/config.go` — enum на `535-536`
    (`off|exact|similar`), поле порога, валидация диапазона; наследование
    virtual > model > global там, где это делают остальные cache-поля
  - Acceptance: AC-1.2; тест валидации на порог 0 и 1.5

- [ ] **TSK-408**: control-plane и схема
  - Requirement: FR-1
  - Deliverables: zod-схема/CRUD и снапшот control-plane, где перечислен
    `off|exact`; UI-селект режима кэша
  - Acceptance: `similar` сохраняется через API и доезжает до снапшота шлюза;
    control-plane тесты зелёные

## Фаза 4 — числа и доки

- [ ] **TSK-409**: замер стоимости похожего поиска
  - Requirement: NFR-1
  - Deliverables: бенч `internal/cache/similar_bench_test.go` на полном кэше
    (`maxSize` записей) + строка с фактическими µs/ms в `docs/`
  - Acceptance: измеренная цифра, а не оценка; если >1 ms — фиксируется как
    известное ограничение с указанием размера кэша

- [ ] **TSK-410**: документация режима
  - Requirement: FR-1, FR-3, FR-4
  - Deliverables: README/доки конфигурации — три режима кэша, порог, гейт
    одношаговости, заголовки ответа; прямо сказано, что похожий хит возвращает
    ответ на **другой** запрос
  - Acceptance: в доке есть команда воспроизведения замера из TSK-409

## Dependency graph

```text
TSK-401 ─┐
TSK-402 ─┼─→ TSK-404 ─→ TSK-405 ─→ TSK-406 ─→ TSK-410
TSK-403 ─┘                  ↑
              TSK-407 ──────┴──→ TSK-408
              TSK-409 (после TSK-404)
```

## Progress

| Задача | Статус |
|---|---|
| TSK-401 изоляция по модели | Pending |
| TSK-402 гейт применимости | Pending |
| TSK-403 тайбрейк | Pending |
| TSK-404 `Lookup`, один исход | Pending |
| TSK-405 проводка в шлюзе | Pending |
| TSK-406 заголовки/телеметрия | Pending |
| TSK-407 конфиг | Pending |
| TSK-408 control-plane | Pending |
| TSK-409 замер NFR-1 | Pending |
| TSK-410 доки | Pending |
