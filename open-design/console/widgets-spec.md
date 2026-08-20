# Консоль — виджеты как в iOS / Android

## Грид
- 12 колонок, gap 16px, max-width 1280px, padding 24px.
- Виджеты размеры: `S 2×2` (160×160), `M 4×2` (340×160), `L 4×4` (340×340), `XL 8×2` (700×160). Масштабируется.
- Breakpoints: 1280 → 768 → 375 (1 колонка, виджеты full-width).

## Виджеты (6 базовых)
1. **Latency** (M): p50/p95/TTFB график (recharts), `X-Gateway-Upstream-MS` live.
2. **Saved Tokens** (M): `X-Gateway-Saved-Bytes /4` → tokens, `RTK/Caveman` toggle.
3. **Health Matrix** (L): таблица `Account × Model` — 🟢/🟡/🔴 (cooldown/circuit), кнопка Reset.
4. **Routing Sankey** (XL): `VirtualKey → Alias → Account` потоки.
5. **Cache Hit** (M): `HIT/MISS` из `X-Gateway-Cache`, hit rate %.
6. **Cost** (M): `BudgetUSD / Team` прогресс бар.

## Взаимодействие (iOS-like)
- `Long press / Edit button` → режим jiggle (wobble 0.6s как у карточек) + `×` удалить + `◢` resize.
- Drag: `⋮⋮` handle сверху, `react-grid-layout` или `gridstack.js`, snap к 12 колонкам, анимация 180ms.
- `+ Add custom widget` — карточка пунктиром, клик → модалка: `Title, API URL (/api/...), Template (handlebars), Size, Color`.
- Layout сохраняется `POST /api/widgets/layout` → `localStorage` + бэк `widgets_layout` table.

## Custom виджет
- Юзер вводит `{{latency}} {{saved}}` → рендер. Безопасный sandbox (no JS).
- Цвета — выбор из палитры карандашей.

## Состояния
- Loading — штриховка, Error — красный карандаш, Empty — "+ Add".
