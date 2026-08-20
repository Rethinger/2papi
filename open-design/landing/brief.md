# Лендинг — промпт "сделай как я его вижу"

Скопируй целиком в генератор (Figma AI / OpenDesign / v0 / Lovable):

---

**Промпт:**
```
Сделай лендинг для 2papi — легковесный AI Gateway (аналог 9Router/OmniRoute но быстрее LiteLLM, TTF <5мс).

Стиль — рисованный цветными карандашами на бумаге #FFFBF0. Фон — бумага с точками (gridDot rgba(34,34,34,0.08)), лёгкая текстура. Шрифты: заголовки Caveat/Patrick Hand 600, тело Inter. Палитра карандашей: #F38181, #FFD93D, #4ECDC4, #95E1D3, #FF6B9D — заливки карточек opacity 0.14 + диагональная штриховка.

Карточки — hand-drawn: border 2px dashed #222, radius 14px 18px 16px 12px, shadow 2px 3px 0 #111. При hover: чёрная карандашная обводка 2.5px solid #111 с фильтром pencil-rough (feTurbulence baseFrequency 0.015-0.022), среднее покачивание wobble 0.6s infinite (±0.4deg rotate + 0.5px translate), живость — stroke-dashoffset 0.9s анимация, не статично. Transition 180ms. Смотри tokens.json.

Секции (см. sections.md):
1. Hero: слева заголовок рукой "2papi — быстрее чем LiteLLM, легче чем 9Router", подзаголовок "OpenAI-совместимый gateway с виджет-консолью как в iOS", справа 3 виджет-карточки (Latency, Saved Tokens, Health) уже с hover-эффектом, CTA "curl | sh — за 30с" как стикер жёлтый, второй CTA "GitHub".
2. Features: 3 колонки — TTF <5мс / RTK+Caveman+Headroom / 1 бинарь без Docker — каждая карточка свой цвет карандаша.
3. Widgets preview: грид 2×3 виджетов как в iOS, drag handle, "+ Add custom widget", гифка перетаскивания.
4. Benchmark: график от руки 2papi vs LiteLLM p95.
5. Footer бумажный.

Адаптив, 1200px max-width, 60fps анимация, без тяжёлых либ.
```

**Токены:** смотри `../tokens.json` — все цвета/радиусы/анимации там.
**Референсы:** `references/` — 3 ссылки на карандаш-стиль.
**Выход:** `export/landing.html` + `export/assets/` — интегрирую в `go:embed`.

---

## Что важно дизайнеру
- Не корпоративный SaaS — "скетчбук конструктор".
- Обводка чёрным карандашом — главный эффект, должна "дышать".
- Виджеты — превью консоли, они же потом в `console/`.
