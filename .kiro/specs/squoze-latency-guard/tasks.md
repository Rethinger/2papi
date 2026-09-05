# Tasks — Squoze Work Guard & Envelope Reach

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

Пути `squoze/...` — во втором репозитории (`C:\Users\rethi\Documents\Projects\squoze`),
как в спеках `squoze-v2` и `thinking-budget-and-user-squoze`.

## Фаза 1 — Контракт границ работы в squoze

- [x] **TSK-301**: Ввести `Limits`, `NewEngineWithLimits`, `Engine.Limits()`; поля
  `Skipped`/`SkipReason` в `Result`; кап `MaxBodyBytes` до любого разбора.
  - Requirement: FR-1, AC-1.2, NFR-4
  - Deliverables: `squoze/squoze.go`, `squoze/internal/engine/engine.go`
  - Acceptance: кап возвращает вход побайтно, `BlocksSqueezed=0`, `NewEngine` не менялся.

- [x] **TSK-302**: Реализовать `canDistill` зеркально пассам `distillText`.
  - Requirement: FR-2
  - Deliverables: `squoze/internal/engine/prescan.go`
  - Acceptance: таблица проверок из design.md реализована в том же порядке; функция без состояния.

- [x] **TSK-303**: Включить гейт в `processOpenAIChatFast` и `processAnthropicFast`
  после дедупа, перед `distillText`/`distillUserContent`.
  - Requirement: FR-2, AC-2.3
  - Deliverables: `squoze/internal/engine/engine.go` (**отклонение от плана**: гейт
    стоит в `distillText`, а не в двух сканерах, и **после** обращения к мемо —
    см. design.md, «Отклонения при реализации», О1)
  - Acceptance: дедуп по-прежнему выполняется до гейта; экономия на дублях не изменилась
    (`TestDuplicateReadsKeepSavings`, >=96%).

- [x] **TSK-304**: Тест output-neutrality: весь корпус, гейт против отсутствия гейта, `bytes.Equal`.
  - Requirement: **AC-2.1** (главный критерий), AC-1.1
  - Deliverables: `squoze/internal/engine/prescan_test.go`
  - Acceptance: 0 расхождений; при искусственной поломке `canDistill` тест краснеет.

## Фаза 2 — Досягаемость лифтинга

- [x] **TSK-305**: `findLiftableArray` — поиск массива по структуре, глубина <=2,
  детерминированный тайбрейк; замена цикла по четырём именам.
  - Requirement: FR-3, AC-3.1
  - Deliverables: `squoze/internal/distill/json_tabular.go`
  - Acceptance: обёртки `rows|matches|files|entries|hits|diagnostics|logs|choices|content|list`
    и путь глубины 2 (`result.rows`) лифтятся с той же экономией, что `data`.

- [x] **TSK-306**: `scalarSiblings` переводится с ключа на путь — сиблинги берутся
  из родительского объекта найденного массива.
  - Requirement: FR-3, AC-3.2
  - Deliverables: `squoze/internal/distill/json_tabular.go`
  - Acceptance: конверт раскрывается и для глубины 2; поля конверта не теряются.

- [x] **TSK-307**: Параметризованный тест по именам обёрток + регрессии
  детерминизма и конверта.
  - Requirement: AC-3.1, AC-3.2, AC-3.3, AC-3.4
  - Deliverables: `squoze/internal/distill/json_prescan_test.go` (новый файл вместо
    правки `json_tabular_test.go`: тесты предпрохода и досягаемости лифтинга — одна тема)
  - Acceptance: экономия однородна по именам (`TestWrapperNamesStillLift`); 12 движков
    дают равный выход; порог 35% по-прежнему отклоняет невыгодный лифтинг —
    именно он, а не разрешение пути, краснил `TestDottedKeyPathResolves`
    (ratio 0.660 против бара 0.65), см. О4.

## Фаза 3 — Релиз squoze v0.4.0

- [x] **TSK-308**: Поднять `engine.Version`, дописать CHANGELOG, прогнать
  `go test ./...` и бенчи, поставить тег `v0.4.0`, запушить.
  - Requirement: FR-5, AC-5.1
  - Deliverables: `squoze/internal/engine/engine.go`, `squoze/CHANGELOG.md`, тег
  - Acceptance: тег виден в proxy.golang.org, чистый клон собирается.

## Фаза 4 — Проводка в 2papi

- [x] **TSK-309**: Поднять пин `github.com/Rethinger/squoze` до v0.4.0 без `replace`.
  - Requirement: FR-5
  - Deliverables: `go.mod`, `go.sum`
  - Acceptance: `go build ./...` и `go test ./...` зелёные.

- [x] **TSK-310**: Пул движков по `Limits`, разрешение значения
  (заголовок > virtual > model > global), заголовок `X-Gateway-Squoze-Skip`.
  - Requirement: FR-4, AC-4.1, AC-4.2
  - Deliverables: `internal/proxy/proxy.go`, `internal/config/config.go`
  - Acceptance: при выключенном капе поведение шлюза не изменилось.
  - **отклонение от плана**: заголовок в разрешении `Limits` не участвует и
    движков не создаёт — он может только ужать бонд для одного запроса
    (пул ключуется `Limits`, значит произвольное число из заголовка = вектор
    роста памяти). См. design.md, «Отклонения при реализации», О5.

- [x] **TSK-311**: Тесты шлюза на заголовки и наследование конфига.
  - Requirement: AC-4.1, AC-4.2
  - Deliverables: `internal/proxy/squoze_limits_test.go`
  - Acceptance: заголовок появляется только при пропуске и несёт причину.

## Фаза 5 — Числа и документация

- [x] **TSK-312**: Промоушен разового пробника в постоянный бенчмарк форм.
  - Requirement: AC-2.2, NFR-3
  - Deliverables: `squoze/internal/engine/shape_bench_test.go`
  - Acceptance: генераторы JSON-конверта, прозы, кода, логов на 60 и 300 турнов;
    цифры воспроизводимы одной командой.

- [ ] **TSK-313**: Пересъём и публикация чисел squoze.
  - Requirement: FR-6, AC-6.1, AC-6.3
  - Deliverables: `docs/benchmarks.md`, `test/results/README.md`
  - Acceptance: каждая цифра снабжена командой перезапуска; версия названа.

- [ ] **TSK-314**: Обновить `BASELINE` дрейфа (устарел на два релиза).
  - Requirement: AC-6.2
  - Deliverables: `test/matrix_compare.mjs`
  - Acceptance: корректный прогон squoze больше не помечается дрейфом.

## Dependency graph

```
TSK-301 ──> TSK-302 ──> TSK-303 ──> TSK-304 ─┐
TSK-305 ──> TSK-306 ──> TSK-307 ─────────────┤
                                             ├──> TSK-308 ──> TSK-309 ──> TSK-310 ──> TSK-311
TSK-312 ─────────────────────────────────────┘                                  └──> TSK-313 ──> TSK-314
```

## Progress

| Task ID | Описание | Статус | Evidence |
|---|---|---|---|
| **TSK-301** | `Limits` + `Result.Skipped` + кап | Complete | `internal/engine/limits.go` + 6 правок `engine.go`; `TestZeroLimitsMatchDefaultEngine`, `TestBodyCapSkipsWithoutLooking`, `TestBlockCapLeavesBigBlockAlone`, `TestMinBlockRaisesFloor` |
| **TSK-302** | `canDistill` | Complete | `internal/engine/prescan.go` (`canDistill`) + `internal/distill/json_prescan.go` (`CanDistillJSON`); `TestPrescanRejectsDeadWork` |
| **TSK-303** | Гейт в сканерах | Complete | гейт в `distillText` после мемо (отклонение О1); `TestDuplicateReadsKeepSavings` |
| **TSK-304** | Output-neutrality по корпусу | Complete | `TestPrescanIsOutputNeutral` (11 форм x 2 арма, `bytes.Equal` + равные `BlocksSqueezed`/`SavedBytes`) и `TestCorpusStillProduces` против вакуумного прохода |
| **TSK-305** | `findLiftableArray` | Complete | `FindLiftableArray` (структурный поиск, глубина <=2); `TestStructuralSearchFindsUnlistedKeys` |
| **TSK-306** | `scalarSiblings` по пути | Complete | сиблинги берутся из родителя по пути (`json_tabular.go:170`); `TestDottedKeyPathResolves`, `TestLiftDeclinedWhenSiblingWouldVanish` |
| **TSK-307** | Тесты обёрток и регрессии | Complete | `json_prescan_test.go`: 6 тестов, включая параметризацию имён обёрток |
| **TSK-308** | Релиз v0.4.0 | Complete | тег `v0.4.0` + `10cd1f4` в origin/main; `go test ./...` 12 пакетов rc=0 |
| **TSK-309** | Пин v0.4.0 в 2papi | Complete | `go.mod:6` → `squoze v0.4.0` без `replace`; `go build ./...` + `go vet` зелёные |
| **TSK-310** | Пул движков, заголовок пропуска | Complete | `squozeEngines sync.Map` + `squozeEngineFor`/`squozeLimitsFor`/`squozeHeaderCap`; `SquozeMaxBodyBytes` в `config.Optimization` с валидацией `< 0` |
| **TSK-311** | Тесты шлюза | Complete | `internal/proxy/squoze_limits_test.go`: 4 теста / 3 подтеста, все PASS (`go test ./internal/proxy/ ./internal/config/` rc=0) |
| **TSK-312** | Бенчмарк форм | Complete | `internal/engine/shape_bench_test.go` (`BenchmarkShapes`, `BenchmarkTurns`) + `internal/distill/json_prescan_bench_test.go` (`BenchmarkDistillJSONGate`) — числа в design.md |
| **TSK-313** | Пересъём чисел | Pending | — |
| **TSK-314** | BASELINE дрейфа | Pending | — |

## Измерения, на которых стоит спека (v0.3.0, 2026-09-05)

Экономия и время `Apply` по формам, и контрольная таблица имён обёрток — в
[requirements.md](requirements.md), раздел «Контекст / проблема». Обе таблицы
сняты в `golang:1.22` в Docker на пине, который `go.mod` держит сейчас; после
TSK-312 они пересчитываются постоянным бенчмарком, а не разовым пробником.
