# Лендинг — секции (как я вижу)

## 1. Hero (100vh)
- Лево (55%): `h1` Caveat 48px "2papi — быстрее чем LiteLLM, легче чем 9Router" (карандаш underline жёлтым), `p` 18px "OpenAI-совместимый gateway. Виджет-консоль как в iOS. 1 бинарь, 0 Docker.", 2 CTA: primary стикер `curl -fsSL ... | sh` (жёлтый #FFD93D, поворот -1deg, hover выравнивается), secondary `GitHub` (outline).
- Право (45%): 3 карточки-виджета наложены с поворотом -2°/1°/ -0.5° как стикеры, каждая с карандаш-hover.

## 2. Features (3 колонки)
- Карточка 1 (голубой #95E1D3): иконка ⚡ от руки, "TTF <5мс" , "Zero-copy hot path, passthrough streaming, PoolTransport HTTP2".
- Карточка 2 (розовый #FF6B9D): "RTK + Caveman + Headroom", "20-40% input, 65% output, context pruning".
- Карточка 3 (мятный #4ECDC4): "1 бинарь без Docker", "go:embed dashboard, SQLite lite mode, brew/scoop".

## 3. Widgets Preview (консоль)
- Заголовок "Консоль — как в iOS", подзаг "Меняй виджеты местами, добавь свой".
- Грид 12 колонок, 6 виджетов: Latency (4×2), Saved Tokens (4×2), Health Matrix (4×4), Routing Sankey (8×2), Cache Hit (4×2), Cost (4×2).
- Drag handle `⋮⋮` сверху, resize угол `◢`, `+ Add custom widget` карточка пунктиром.
- Гифка 3с перетаскивания.

## 4. Benchmark
- Заголовок "Быстрее чем Python", график от руки (линия 2papi 4мс vs LiteLLM 22мс p95), подпись "ferro-labs benchmark, 100 RPS, 4k tokens".

## 5. Pricing / Install
- 3 тарифа стикерами: OSS / Pro / Team — но пока заглушка "Open Source — free".
- Install tabs: Docker / Binary / Brew — код-блоки как листочки.

## 6. Footer
- Бумага, ссылки GitHub / Docs / Discord, копирайт от руки.

Адаптив: 768px → 1 колонка, карточки стопкой, wobble остаётся.
