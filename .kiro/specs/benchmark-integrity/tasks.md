# Tasks — Benchmark Integrity Suite

Requirements: [requirements.md](requirements.md) · Design: [design.md](design.md)

## Фаза 1 — Аудит текущих заявлений

- [x] **TSK-001**: Зафиксировать расхождения README ↔ `test/results/*.json` ↔ `docs/benchmarks.md` с точными цифрами и первопричиной каждого.
  - Requirement: FR-9
  - Deliverables: раздел аудита в финальном отчёте
  - Acceptance: каждое заявление README сопоставлено с данными, которые его подтверждают или опровергают.

- [x] **TSK-002**: Проверить доступность апстрим-провайдера и задокументировать блокировку живого слоя.
  - Requirement: FR-10, AC-10.1
  - Deliverables: `test/provider_probe.mjs`, `test/results/provider_probe.json`
  - Acceptance: перечислены реально отдаваемые модели; если ни одна не работает — статус BLOCKED с кодами.

## Фаза 2 — Оффлайн-харнесс качества squoze

- [x] **TSK-003**: Собрать корпус кейсов по пяти классам из design.md.
  - Requirement: FR-1, AC-1.2
  - Deliverables: `test/squozebench/corpus.go`
  - Acceptance: ≥ 12 кейсов, включая negative и размерные гейты.

- [x] **TSK-004**: Реализовать метрики: needle-recall, format-safety, idempotency, детерминизм, prefix-stability, latency.
  - Requirement: FR-2, FR-3, FR-4, FR-5
  - Deliverables: `test/squozebench/metrics.go`
  - Acceptance: negative-кейсы дают «не тронуто»; `MaxKept`-кейс даёт recall < 100%.

- [x] **TSK-005**: Раннер: прогон, JSON-отчёт, артефакты до/после.
  - Requirement: FR-1, AC-1.1, AC-1.3
  - Deliverables: `test/squozebench/main.go`, `test/results/squoze_quality_report.json`
  - Acceptance: работает в `golang:1.23` без сети.

- [x] **TSK-006**: Токенный скоринг реальным токенизатором.
  - Requirement: FR-6
  - Deliverables: `test/tokenscore.mjs`, поля `tokens_*` в отчёте
  - Acceptance: экономия в токенах посчитана; для Claude помечена как прокси.

## Фаза 3 — Слой шлюза

- [x] **TSK-007**: Прогнать матрицу режимов против fake-upstream и сверить с `docs/benchmarks.md`.
  - Requirement: FR-7
  - Deliverables: `test/results/gateway_matrix_report.json`
  - Acceptance: расхождения > 20% помечены.

- [x] **TSK-008**: Конформность OpenAI-совместимости.
  - Requirement: FR-8
  - Deliverables: `test/conformance.mjs`, `test/results/conformance_report.json`
  - Acceptance: PASS/FAIL с фактическим наблюдением по каждой проверке.

## Фаза 4 — Живой слой (готов, заблокирован апстримом)

- [x] **TSK-009**: Раннер точности с k≥3 повторов, median/IQR, preflight.
  - Requirement: FR-10
  - Deliverables: `test/accuracy_suite.mjs`
  - Acceptance: отказывается публиковать n=1; при отсутствии модели — BLOCKED.

## Фаза 5 — Правка документации

- [x] **TSK-010**: Переписать бенч-раздел README по измеренным числам; удалить/переразметить бейджи; добавить «что не измерено».
  - Requirement: FR-9, AC-9.1, AC-9.2
  - Deliverables: `README.md`, `docs/benchmarks.md`
  - Acceptance: ни одного заявления без артефакта в `test/results/`.

## Фаза 6 — Ремонт корпуса и воспроизводимость A/B

- [x] **TSK-011**: Снять противоречие ожиданий на `json_api_list_800_rows` и перенести проверку конверта в грейдинг.
  - Requirement: FR-2, FR-3, AC-2.2, AC-3.1
  - Deliverables: `test/squozebench/corpus.go`, `test/squozebench/verify_test.go`
  - Acceptance: кейс объявляет ровно одно ожидание (`Class: structured-data`, `Expect: ExpectEither`), `format-invalid` в `cmp_savings.mjs` пуст, а потеря конверта ловится как `never-elide` — v0.2.0 падает на нём в каждом прогоне.

- [x] **TSK-012**: A/B-харнесс, не мутирующий `go.mod`.
  - Requirement: NFR-3
  - Deliverables: `test/squozebench/repro/{savings_ab.sh,accept.sh,cmp_savings.mjs,README.md}`, `.gitignore`
  - Acceptance: обе стороны собираются через `-modfile=go.local.mod`; прерванный прогон не оставляет `replace` в дереве; последняя строка вывода печатает пин squoze из нетронутого `go.mod` (на момент задачи v0.2.0, с 2026-09-05 v0.3.0).

- [x] **TSK-013**: Пересобрать сохранённые отчёты текущими скриптами и согласовать снимок.
  - Requirement: NFR-3, FR-1
  - Deliverables: `test/results/squoze_ab/` (3 прогона на сторону + канонические `base`/`head`), `test/results/squoze_quality_report.json`
  - Acceptance: канонические файлы побайтово равны `base.1.json` / `head.1.json`; ни один сохранённый отчёт не описывает корпус, которого больше нет.

- [x] **TSK-014**: Закрыть три пина контрактов гейтом и вынести их в отдельное задание CI.
  - Requirement: NFR-3, FR-1
  - Deliverables: `test/squozebench/verify_test.go`, `.github/workflows/ci.yml`, `docs/benchmark-audit.md`, `test/squozebench/repro/README.md`, `README.md`, `CHANGELOG.md`
  - Acceptance: `go test -race ./...` зелёный на пине `squoze v0.2.0` (три `SKIP` вместо трёх `FAIL`), с `SQUOZE_CONTRACT_PINS=1` все три по-прежнему падают, задание `squoze-contract-pins` помечено `continue-on-error` и не входит в `needs` релиза.

- [x] **TSK-015**: Поднять пин squoze до v0.3.0, снять гейт и каскадом обновить документацию.
  - Requirement: NFR-3, FR-1
  - Deliverables: `go.mod`, `go.sum`, `test/squozebench/verify_test.go`, `test/squozebench/classify_test.go`, `.github/workflows/ci.yml`, `test/results/squoze_quality_report.json`, `README.md`, `CHANGELOG.md`, `docs/benchmark-audit.md`, `test/results/README.md`, `test/squozebench/repro/README.md`, `test/squozebench/repro/savings_ab.sh`, `test/squozebench/repro/accept.sh`, `.kiro/specs/benchmark-integrity/design.md`
  - Acceptance: `go.mod` пинит `github.com/Rethinger/squoze v0.3.0`; `go vet ./...` и `go test -race ./... -count=1` зелёные без переменных окружения, без `SKIP` в `test/squozebench`; `contractPin` и задание CI `squoze-contract-pins` удалены; свежий `go run ./test/squozebench` даёт 14 pass / 0 fail / 1 known-limit вердикт-в-вердикт равно `squoze_ab/head.1.json` и лежит в репозитории с `squoze_version: 0.3.0`; ни один документ больше не утверждает, что тега выше v0.2.0 нет; зеркало скорера в `classify_test.go` отражает v0.3.0 (добавлен line-anchored `crashHits`, не отражённые диагностические строки объявлены как нижняя граница) и исключает само себя из выборки, потому что держит маркеры литералами; §3 аудита и таблица §7 пересчитаны по свежему прогону: 0 из 97 файлов элидировано, 0 из 65 тест-файлов выше порога, пункты 1–5 закрыты в v0.3.0, 6–7 открыты, 8 частично.

## Фаза 7 — Живая точность и честное сравнение (P2/P3)

- [x] **TSK-016**: Preflight на действующем провайдере (`https://gpt.crax.lol`) и выбор моделей, которые реально отвечают.
  - Requirement: FR-11, AC-11.3, AC-11.4, NFR-2
  - Deliverables: `test/results/provider_probe.crax.json`, правки `test/provider_probe.mjs`
  - Acceptance: выполнено — список отвечающих моделей **пуст**, и отчёт говорит почему: из 19 id в `/v1/models` ни одна из 15 текстовых моделей не ответила (9×502, 6×429), через несколько минут — 403 `site_locked`. Ключ только из env, в отчёте его нет.
  - **отклонение от плана** (путь артефакта): отчёт пишется в `provider_probe.crax.json`, а не в
    `provider_probe.json`. Прогон второго провайдера в тот же файл удалил бы единственное
    свидетельство о первом, а на оба ссылается `test/results/README.md`. Имя выбирается через
    `PROBE_OUT`, каталог моделей — через `PROBE_MODELS`, чтобы один раннер обслуживал разных
    провайдеров без форка логики пробы.
  - **дополнительно найдено**: сама проба считала `ok: true` любой ответ с разбираемым JSON без поля
    `error` — включая 502 с пустым телом. Исправлено: теперь требуется 2xx **и** непустое `content`.

- [~] **TSK-017**: Прогон набора точности с k≥3 и снятие статуса BLOCKED. — **AC-11.2 сделан, AC-11.1 блокирован провайдером**
  - Requirement: FR-11, AC-11.1, AC-11.2
  - Deliverables: `test/results/accuracy_report.json`, `test/accuracy_gates_selftest.mjs`, строки в `test/results/README.md`
  - Acceptance: три гейта теперь судят, а не печатают — `evaluateGates()` даёт `pass`/`fail`/`null` по каждому и ставит `status` ∈ `PASS`/`FAIL`/`PARTIAL`/`INCONCLUSIVE` вместо безусловного `COMPLETE`; 11 проверок самотеста зелёные. Сам прогон на живом провайдере не состоялся.
  - **чем заблокировано**: оба провайдера на руках лежат — gorouter отдаёт 403 с 2026-09-04, crax прошёл
    502 → 429 → 403 `site_locked` за 2026-09-05. Шлюз был поднят с парными алиасами (`crax-squoze` /
    `crax-nosquoze` — один upstream, разница только в squoze), preflight упёрся в 403, и отчёт машинно
    записал `status: BLOCKED` с текстом провайдера в `blocked_reason` — как и требует AC-11.4.
    Замена оффлайн-доказательством не делается: `squoze_quality_report.json` мерит другое
    (сохранность иголок в тексте), а не ответ модели.

- [ ] **TSK-018**: Перемер накладных расходов шлюза после v0.4.0 и обновление BASELINE матрицы.
  - Requirement: FR-7, FR-12, AC-12.2
  - Deliverables: `test/results/gateway_matrix_report.json`, `test/matrix_compare.mjs` (BASELINE), `docs/benchmarks.md`
  - Acceptance: BASELINE соответствует пину squoze на момент прогона (значение 438.56 снято на v0.2.0 и устарело на два релиза); у каждой цифры указаны размер payload, режим и команда перезапуска.

- [ ] **TSK-019**: Таблица сравнения: свои замеры против заявлений вендоров.
  - Requirement: FR-12, AC-12.1, AC-12.3
  - Deliverables: `docs/benchmarks.md`, бенч-раздел `README.md`
  - Acceptance: у каждой строки помечено «измерено» или «заявление вендора» с условиями; строки-цели без замера удалены; заявление bifrost (11 µs t3.xlarge / 59 µs t3.medium при 5 000 RPS) приведено как их цифра при их условиях, а сторонний бенчмарк, отдающий 403, помечен непроверяемым.

## Dependency graph

```mermaid
graph LR
    T1[TSK-001] --> T10[TSK-010]
    T2[TSK-002] --> T9[TSK-009]
    T3[TSK-003] --> T4[TSK-004] --> T5[TSK-005] --> T6[TSK-006] --> T10
    T7[TSK-007] --> T10
    T8[TSK-008] --> T10
    T3 --> T11[TSK-011] --> T13[TSK-013] --> T10
    T5 --> T12[TSK-012] --> T13
    T11 --> T14[TSK-014] --> T15[TSK-015]
    T2 --> T16[TSK-016] --> T17[TSK-017] --> T19[TSK-019]
    T15 --> T18[TSK-018] --> T19
```

## Progress

| Task | Статус | Артефакт |
|---|---|---|
| TSK-001 | Complete | `docs/benchmark-audit.md` |
| TSK-002 | Complete | `test/results/provider_probe.json` — все модели BLOCKED |
| TSK-003 | Complete | `test/squozebench/corpus.go` — 15 кейсов, 7 классов |
| TSK-004 | Complete | `test/squozebench/metrics.go` |
| TSK-005 | Complete | `test/results/squoze_quality_report.json` |
| TSK-006 | Complete | `test/tokenscore.mjs`, поля `tokens_*` |
| TSK-007 | Complete | `test/results/gateway_matrix_report.json` |
| TSK-008 | Complete | `test/results/conformance_report.json` — 9 pass / 0 fail / 3 inconclusive |
| TSK-009 | Complete | `test/accuracy_suite.mjs` — статус BLOCKED (апстрим) |
| TSK-010 | Complete | `README.md`, `docs/benchmarks.md` |
| TSK-011 | Complete | `corpus.go` + `verify_test.go` — конверт как needles, `format-invalid: none` |
| TSK-012 | Complete | `test/squozebench/repro/` — `-modfile=go.local.mod`, `go.mod` не мутируется |
| TSK-013 | Complete | `test/results/squoze_ab/` — 3+3 прогона, канонические пары сверены `cmp` |
| TSK-014 | Complete | `verify_test.go` гейт `SQUOZE_CONTRACT_PINS`, задание CI `squoze-contract-pins` |
| TSK-015 | Complete | `go.mod` → `squoze v0.3.0`, гейт и задание CI удалены, 14 pass / 0 fail без переменных |
| TSK-016 | Complete | preflight crax → `test/results/provider_probe.crax.json`: 19 id в каталоге, 0 ответивших (502/429, затем 403 `site_locked`); исправлен `ok`-критерий пробы |
| TSK-017 | Blocked (AC-11.2 Complete) | гейты судят через `evaluateGates()`, 11/11 самотестов; BLOCKED не снят — оба провайдера недоступны |
| TSK-018 | Pending | `gateway_matrix_report.json` + BASELINE после v0.4.0 |
| TSK-019 | Pending | таблица «измерено / заявление вендора» |

## Результаты прогона

Корпус один и тот же — 15 кейсов, 7 классов (`should-squeeze`, `size-gate`,
`algorithm-limit`, `must-not-touch`, `structured-data`, `format-contract`,
`user-path`). Разница только в версии squoze, поэтому цифры двух сторон нельзя
смешивать: A/B прогоняется `test/squozebench/repro/savings_ab.sh`, 3 повтора на
сторону, отчёты в `test/results/squoze_ab/`.

### Сторона base — `squoze v0.2.0`, то, что `go.mod` пинил до 2026-09-05 (снято 2026-09-04)

11 pass · 3 fail · 1 known-limit · сжатие сработало на 11/15 · медиана
96.14% · p95 max 5.89 мс · `prefix_broken_models: [claude-opus-4-5, gpt-5]`.

Нарушения контрактов, подтверждённые отдельными тестами в `test/squozebench/verify_test.go`:

1. **`touched-protected-content`** — исходный код, плотный по маркерам `FAILED`/`PASSED`/`assert `, элидируется как машинный вывод (85–92% срезано, Go становится синтаксически битым). Калибровка: на реальных файлах репозитория срабатывает 1 из 93 (и это собственный `corpus.go`), на реальных `_test.go` — 0 из 64 (score 1 против порога 3). Опасность латентная, инцидентность близка к нулю.
2. **`never-elide`** — при подъёме списка в Markdown-таблицу теряются все три поля конверта: `has_more`, `next_cursor`, `total_count` (recall 0/3). Читатель распакованной таблицы делает вывод, что список кончился, и перестаёт листать.
3. **Недетерминизм** — `tryTabularLifting` строит порядок колонок обходом Go-map: 3 разных порядка на 12 свежих движках. Прямое нарушение cache-safe контракта.
4. **`PREFIX-BROKEN`** — кросс-turn дедуп переотправляет ранний ход в ПОЛНОМ виде (4 245 → 29 159 байт) из-за самопротиворечивого guard в `stream_scanner.go`.

### Сторона head — рабочее дерево squoze, вышло релизом v0.3.0 (2026-09-05)

14 pass · 0 fail · 1 known-limit · сжатие сработало на 9/15 · медиана
97.02% · p95 max 5.62 мс · `contract_violations: {}` · `prefix_broken_models: []`.

Все четыре нарушения закрыты. Два кейса из знаменателя ушли намеренно
(`go_source_dense_in_test_words`, `realistic_test_helper_source` больше не
сжимаются — это выполненный must-not-touch), поэтому медиана по всем 15 кейсам
падает 87.44% → 76.20%, а сравнимая медиана по кейсам, которые сжимают обе
стороны, держится: 97.06% → 97.02%, −0.03 pp.

Known-limit один и тот же на обеих сторонах: `go_test_200_fails_vs_maxkept50`
при `MaxKept=50` оставляет 17 из 200 строк `FAIL`, потеря раскрыта в маркере.

**Поставлено (2026-09-05).** Сторона head вышла релизом `squoze v0.3.0`,
`go.mod` шлюза пинит его, и головные цифры теперь воспроизводятся простым
`go run ./test/squozebench` без `-modfile`: свежий прогон на релизе даёт те же
вердикты и те же проценты, что сторона head снимка (p95 max 5.4 мс против
5.62 — шум хоста). `-modfile=go.local.mod` остаётся механизмом для следующего
раунда правок squoze, а не условием действительности цифр.

### Слой шлюза

Матрица режимов: squoze на 633 КБ измерен 74.7 / 146.6 мс против
задокументированных 438.56 — цифра в `docs/benchmarks.md` устарела.

Байты против токенов: расхождение ≤ 0.6 pp на элидированном машинном выводе, но
76.2% байт против 63.7% токенов на подъёме в таблицу (−12.5 pp) — Markdown-пайпы
токенизируются хуже заменяемого JSON. `test/tokenscore.mjs` помечает любой кейс с
расхождением ≥ 5 pp.
