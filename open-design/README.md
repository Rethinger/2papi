# open-design — бриф для дизайнера (лендинг + консоль)

Эта папка — единственный источник для фронта. Бэк (gateway+panel API) делается параллельно и не блокирует дизайн.

## Как работать
1. **Лендинг:** открой `landing/brief.md` + `tokens.json` → сгенерируй лендинг в Figma / OpenDesign / любом генераторе. Промпт уже внутри `brief.md` — копируй целиком.
2. **Консоль:** открой `console/widgets-spec.md` + `console/pencil-style.md` + `console/dashboard-plan.md` + `tokens.json` → сделай 3 виджета (Latency / Saved Tokens / Health) + грид как в iOS + Quota bar на главной.
3. **Интеграция:** когда дизайн готов — `console/api-contract.md` описывает как виджеты дергают бэк (`/api/widgets`), `console/dashboard-plan.md` — квота (`/api/control/v1/quota`). Бэк реализует контракт позже, дизайн не ждёт.

## Структура
```
open-design/
├── README.md
├── tokens.json          # цвета, радиусы, анимация карандаша
├── landing/
│   ├── brief.md         # промпт "сделай лендинг как я его вижу"
│   ├── sections.md      # секции hero/features/widgets/pricing/footer
│   └── references/      # ссылки на референсы карандаш-стиля
└── console/
    ├── widgets-spec.md  # iOS/Android грид, drag, resize, custom
    ├── dashboard-plan.md# главная + Quota bar + вкладка /quota + навигация
    ├── pencil-style.md  # карандаш: обводка, покачивание, живость
    └── api-contract.md  # контракт бэка для виджетов
```

## Что уже готово к интеграции
- Gateway отдаёт `X-Gateway-Route / Attempts / Saved-Bytes / Cache` — виджеты их показывают.
- Тогглы `RTK / Caveman / Headroom` — будут `per-model/per-key` (бэк готовит).
- Квота: `GET /api/control/v1/quota` — сводный % + per-provider (см. `dashboard-plan.md`).

## Следующий шаг дизайнера
1. Сгенери лендинг по `landing/brief.md` → экспорт `landing.html` в `open-design/landing/export/`
2. Сделай 3 виджета по `console/widgets-spec.md` → экспорт в `console/export/`
3. Сделай Combined Quota bar + вкладку по `console/dashboard-plan.md` → экспорт в `console/export/`
4. Пингани — интегрирую статику в `go:embed` (single binary `2papi`)
