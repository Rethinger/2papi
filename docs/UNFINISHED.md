# 2papi — недоделанное / что дальше

Дата: 2026-09-02 (аудит + разблокировка Docker). Этот файл — честная карта
незавершённого. План-документ с ресёрчем гэпов:
`C:\Users\Xiaomi\.opencode\plan\2papi-gap-research-2026-08-28.md`.

## В работе (optimization-modes, витки 1-7 закрыты к 2026-09-02)

| Виток | Статус | Что осталось |
|---|---|---|
| 1-3 (Go-ядро: типы, пресеты, auto-эвристики) | ✅ сделано | — |
| 4 (control-plane RoutingSchema + snapshots) | ✅ сделано, commit `c3579ed` | — |
| 5 (дашборд: селекты режимов + i18n) | ✅ сделано, commit `475a654` | — |
| 6 (доки + полный QA) | ✅ сделано, commit `2d36bbd` | — |
| 7 (single-pass пайплайн) | ✅ сделано, commit `023efcd` | — |
| 8 (бенчмарки) | ✅ **сделано 2026-09-02** | матрица 14 режимов × 3 профиля payload на Docker-стенде; числа и выводы в `docs/benchmarks.md`. Побочно найден и починен fast-path баг headroom (см. ниже) |
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

- **18 фейлов control-plane интеграционных тестов** — ✅ **ЗАКРЫТ 2026-09-02**.
  Это был не продуктовый баг, а порядок инициализации: `lib/db.ts` создавал
  `new Pool()` на импорте модуля, а ESM поднимает `import` выше любых
  присваиваний — тесты не успевали подменить `DATABASE_URL`, запросы уходили
  в немигрированную базу. Настоящая ошибка (`relation "users" does not exist`)
  была видна только под `CODEX_TEST_MODE=true`, потому что `api.ts:problem()`
  прячет не-ApiError в generic 500. Починено ленивым пулом через `Proxy`
  (сохраняет 21 существующий импорт и duck-typed `'connect' in db`).
  Итог: **215/215 pass, 0 fail, 0 skip** — нужен только `TEST_DATABASE_URL`.
- **CI никогда не запускался** — ✅ ЗАКРЫТ 2026-09-02. `.github/workflows/ci.yml`
  триггерился на `branches: [main]`, а в репозитории только `master`. Именно
  поэтому долги выше жили незамеченными. Исправлено на `master`.
- **Фейковые транзакции** — ✅ ЗАКРЫТ 2026-09-02 (найдено статически, отдельно
  от тестов). `cloud-auth.ts:101` и `webhooks.ts:80` делали `BEGIN`/`COMMIT`/
  `ROLLBACK` через `pool.query`, а Pool на каждый вызов берёт произвольное
  соединение и возвращает его в пул: следующие запросы уходят на *другие*
  соединения и коммитятся сами по себе, а соединение с открытой транзакцией
  висит без дела. Атомарности не было ни у провижининга team/key после signup,
  ни у начисления баланса Paddle. Добавлен `txOn()` (`lib/db.ts`), который
  сначала берёт выделенное соединение.
- **Тесты требовали внешний DNS** — ✅ ЗАКРЫТ 2026-09-02.
  `TestDialSOCKS5NoAuth`/`TestDialSOCKS5Auth` дозванивались до `example.com`
  (схема `socks5://` резолвит цель локально — это корректно), из-за чего падали
  в песочнице: `lookup example.com: i/o timeout`. Хуже, проверка «неверный
  пароль отвергнут» проходила бы и от DNS-таймаута, то есть по неверной
  причине. Цели переведены на `localhost`. Вся Go-сюита теперь зелёная
  с `--network none`.
- **headroom fast-path** — ✅ ЗАКРЫТ 2026-09-02 (найден виток-8 бенчмарком).
  Явные профили headroom на 97 KiB тратили ~8.8-9.2ms и **ничего не обрезали**,
  тогда как `headroom auto` приходил к тому же no-op за 0.02ms: fast-path в
  `OptimizeRequest` требовал `o.RTK` и отсутствия headroom, поэтому любой явный
  профиль проваливался в полный `json.Unmarshal`. Стало ~0.08ms и +23% rps,
  обрезка на `huge` не изменилась. Заодно закрыт пробел покрытия: у single-pass
  пайплайна (виток 7) не было ни одного прямого теста — добавлен
  `internal/compression/optimize_test.go`.
- **compose.yaml postgres порт** — ✅ ЗАКРЫТ (`127.0.0.1:5432:5432`).
- **`.enola/`** — ✅ отсутствует. **ветка main** — ✅ origin/master есть.
- **`cmd/gateway` не компилировался** (pre-existing) — ✅ ПОЧИНЕНО
  (interactive.go, .gitignore root-only-паттерны, CRLF-устойчивый checksum;
  commits 44e16c9, 447c8e5).
- **squoze root-обёртка не публична** — ✅ ЗАКРЫТ 2026-09-02. Оказалось, что
  корневой `squoze.go` уже лежал в `origin/main` репозитория squoze, но не был
  затегирован. Проставлен тег **v0.1.2**, из `go.mod` убран
  `replace => ../squoze`, зависимость тянется из проксей с честными
  контрольными суммами. Разблокировало три пути, которые до этого падали:
  `docker build`, `go build` на чистом клоне и CI-джобу `go test -race`.
- **otlp-зависимости**: go.mod вырос (otel sdk v1.31 + grpc/protobuf
  транзитивно) — бинарь тяжелее; если критично, заменить на самописный
  OTLP-HTTP клиент.
- **Отсутствие релизов и бинарников для пользователей** — ✅ **ЗАКРЫТ 2026-09-02**.
  (Спека `.kiro/specs/release-automation`, коммиты `7f6df06`, `a95fc6d`, `e12cd1e`).
  Опубликован первый официальный релиз **`v0.3.0`** с prebuilt архивами для
  linux/darwin/windows, ldflags-версионированием и чистой установкой через
  `install.sh` / `install.ps1` без необходимости ставить Go. Документация в
  README/RELEASE синхронизирована с `@latest`.

## Открытые вопросы после аудита 2026-09-02

- **squoze экономически не подтверждён**: на 633 KiB это самый дорогой режим
  (438ms, 31 rps против 97 rps baseline) и при этом вернул `squoze=false` —
  полный разбор без выгоды. Синтетическая нагрузка для него нерепрезентативна;
  нужен замер на реальных payload'ах, прежде чем рекомендовать. Пока —
  экспериментальный и только через конфиг.
- **Заявленный p95 <5ms верен только для мелких тел.** Стоимость оптимизаторов
  растёт с размером body (RTK ~12ms на 97 KiB, ~110ms на 633 KiB). Это
  осознанный обмен CPU на токены, но в README цифру стоит квалифицировать —
  агентный цикл с большими tool_result попадает не в тот профиль.

## Идеи на следующий раунд gap-ресёрча

Multi-region/durability, unit-economics PLG (pricing), протокол A2A, SOC2-путь для Ent.