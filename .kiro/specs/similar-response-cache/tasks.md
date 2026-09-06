# Tasks — Similar Response Cache (`cache: similar`)

Требования: [requirements.md](requirements.md). Quick Spec — approval-гейтов между
фазами нет, но порядок фаз обязателен: сначала библиотека кэша, потом шлюз, потом
конфиг и доки.

## Фаза 1 — исправить сам `FindSimilar`

- [x] **TSK-401**: изоляция по модели
  - Requirement: FR-2
  - Deliverables: `internal/cache/cache.go` — поле `Model` в `Entry`,
    заполнение в `SetWithRequest`, фильтр в `FindSimilar`
  - Acceptance: AC-2.1; новый тест `TestFindSimilarModelIsolation` (одинаковый
    текст, две модели → промах)

- [x] **TSK-402**: гейт применимости и минимум слов
  - Requirement: FR-3
  - Deliverables: `internal/cache/cache.go` — `similarEligible(body []byte) ([]string, bool)`
    рядом с `requestUserWordList` (один разбор тела: `tools`, роли, число сообщений,
    число значимых слов ≥ 8); `FindSimilar` вызывает его первым и на `false`
    возвращает промах
  - Acceptance: AC-3.1, AC-3.2; тесты на четыре отказа — есть `tools`,
    есть `role: tool`, >2 сообщений помимо `system`, <8 значимых слов

- [x] **TSK-403**: детерминированный тайбрейк
  - Requirement: FR-3 (edge case «два кандидата с одинаковым Jaccard»)
  - Deliverables: `internal/cache/cache.go` — при равном score выбирается
    лексикографически меньший ключ
  - Acceptance: 20 прогонов одного и того же поиска по двум равным кандидатам
    дают один и тот же ключ

- [x] **TSK-404**: один исход на запрос (`Lookup`)
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

- [x] **TSK-405**: похожий поиск на пути запроса
  - Requirement: FR-1, FR-6
  - Deliverables: `internal/proxy/proxy.go` — в блоке `if wantCache`
    (`~427-462`) `Get` заменяется на `Lookup` с порогом из конфига; путь записи
    (`~561`) не меняется
  - Acceptance: AC-1.1 (регрессия `exact`), AC-6.1; существующие
    `internal/proxy/cache_compress_test.go` зелёные без правок

- [x] **TSK-406**: заголовки и телеметрия похожего хита
  - Requirement: FR-4
  - Deliverables: `internal/proxy/proxy.go` — `X-Gateway-Cache: HIT-SIMILAR`,
    `X-Gateway-Cache-Score`; outcome `cache_similar` там же, где сейчас пишется
    исход точного хита
  - Acceptance: AC-4.1; тест end-to-end: первый запрос MISS, похожий второй —
    `HIT-SIMILAR` со score в (0,1]

## Фаза 3 — конфиг

- [x] **TSK-407**: `cache: similar` + `cache_similar_threshold`
  - Requirement: FR-1
  - Deliverables: `internal/config/config.go` — enum на `535-536`
    (`off|exact|similar`), поле порога, валидация диапазона; наследование
    virtual > model > global там, где это делают остальные cache-поля
  - Acceptance: AC-1.2; тест валидации на порог 0 и 1.5

- [x] **TSK-408**: control-plane и схема
  - Requirement: FR-1
  - Deliverables: zod-схема/CRUD и снапшот control-plane, где перечислен
    `off|exact`; UI-селект режима кэша
  - Acceptance: `similar` сохраняется через API и доезжает до снапшота шлюза;
    control-plane тесты зелёные

## Фаза 4 — числа и доки

- [x] **TSK-409**: замер стоимости похожего поиска
  - Requirement: NFR-1
  - Deliverables: бенч `internal/cache/similar_bench_test.go` на полном кэше
    (`maxSize` записей) + строка с фактическими µs/ms в `docs/`
  - Acceptance: измеренная цифра, а не оценка; если >1 ms — фиксируется как
    известное ограничение с указанием размера кэша

- [x] **TSK-410**: документация режима
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
| TSK-401 изоляция по модели | Complete |
| TSK-402 гейт применимости | Complete |
| TSK-403 тайбрейк | Complete |
| TSK-404 `Lookup`, один исход | Complete |
| TSK-405 проводка в шлюзе | Complete |
| TSK-406 заголовки/телеметрия | Complete |
| TSK-407 конфиг | Complete |
| TSK-408 control-plane | Complete |
| TSK-409 замер NFR-1 | Complete |
| TSK-410 доки | Complete |

## Заметки реализации (что решено по ходу)

Решения, которых в спеке не было, но которые пришлось принять — здесь, чтобы
следующий читатель не выводил их из диффа:

1. **`SetWithRequest` получил явный параметр `model`** — `Entry.Model` заполняется
   на записи, а не выводится из формата ключа (TSK-401). Ключ остаётся деталью
   `KeyFor`.
2. **Порог живёт на модели.** Полей кэша у `VirtualKey` и в глобальном
   `optimization` нет вообще, поэтому «virtual > model > global» из дизайна — это
   пункт документации (сказано, что наследования нет), а не код.
3. **`cache_similar_threshold` без `cache: similar` — жёсткая ошибка конфига**, а
   не молча проигнорированное поле: иначе включённый порог создаёт ложное
   ощущение включённого режима.
4. **Порог — `*float64`.** Явный `0` отвергается как ошибка вместо того, чтобы
   означать «не задано»; `nil` — и только он — выбирает
   `cache.DefaultSimilarThreshold` (0.95).
5. **`''` добавлен в zod-энумы control-plane** — первый пункт селекта («наследовать»)
   должен круго-трипиться через API, а компилятор снапшота пустое значение опускает.
6. **Валидация режима есть и на публикации снапшота** (`validateCacheModes` в
   `control-plane/lib/snapshots.ts`): PATCH видит только присланные поля, а
   несовместимость «порог без similar» проявляется на составленном состоянии.
   Иначе control-plane выглядел бы здоровым, а шлюз сидел бы на старом конфиге.
7. **Пакетная модалка моделей осталась без полей кэша** — сознательно: `similar`
   не должен включаться пачкой по десяти алиасам одним кликом.
8. **`wordList` теперь дедуплицирует**, поэтому `MinSimilarWords` (8) считает
   **различные** значимые слова. Это смена семантики гейта, допустимая только
   потому, что режим ещё не выпущен: «слово повторено пять раз» больше не
   открывает похожий поиск.
9. **`Entry.WordBits uint64` персистится** (подпись слов, по биту на слово), и
   `LoadFromFile` нормализует кэши, записанные старым билдом: дедуп
   `RequestWords` + пересчёт подписи. Инвариант поддерживается в двух точках
   записи, а не защищается на каждом чтении — на этом и держится NFR-1.
   Пин: `TestLoadFromFileNormalizesLegacyWords` падает, если убрать любую из
   двух половин восстановления.
10. **Оба прунинга в `FindSimilar` — точные верхние оценки Jaccard**
    (`overlapReaches` на размерах множеств, затем счёт совпавших бит подписи),
    поэтому отброшенный кандидат не мог бы победить, а коллизии бит только
    ослабляют оценку — ложный минус невозможен. Эмпирическая проверка — два
    приёмочных теста `internal/proxy/cache_similar_test.go`, зелёные без правок
    после обоих раундов оптимизации.

## Доказательства (evidence)

- **Go**: `go vet ./...` — OK; `go test -count=1 ./...` — OK, включая
  `internal/cache` (12 тестов, из них `TestFindSimilarModelIsolation`,
  `TestSimilarEligibleGate`, `TestFindSimilarDeterministicTiebreak`,
  `TestLoadFromFileNormalizesLegacyWords`) и `internal/proxy`
  (`TestSimilarCacheServesNearDuplicate`,
  `TestSimilarCacheRefusesAgenticAndHeaderOnly`).
- **Control-plane**: `npm test` — 217 тестов, 137 pass, **0 fail**, 80 skip (все
  skip — `TEST_DATABASE_URL is not set`), включая
  `snapshot carries the similar cache mode and refuses a threshold the gateway would reject`
  и `similar cache mode round-trips and its threshold keeps absent apart from zero`.
- **NFR-1 (TSK-409)**: похожий поиск на полном кэше 4096 записей — промах
  0.385/0.423/0.433 ms, хит 0.619/0.572/0.517 ms, 23 alloc/op (было 2.38-2.53 /
  4.56-4.62 ms и 12 310 alloc). Бюджет 1 ms выполнен с запасом ~2x. Команда
  воспроизведения и хост — `docs/benchmarks.md`,
  раздел «Similar-response cache lookup — measured 2026-09-06»:
  `go test -run '^$' -bench 'FindSimilar|LookupExact' -benchtime 2s -count=3 ./internal/cache/`
- **TSK-410**: раздел `## Response cache (off / exact / similar)` в `README.md`
  (три режима, порог, гейт одношаговости, заголовки, прямое предупреждение про
  ответ на другой запрос, команда перезамера), закомментированный пример в
  `config/example.yaml`, подсказка `form.cacheHint` в дашборде (en/ru).
