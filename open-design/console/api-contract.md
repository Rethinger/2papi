# API контракт для виджетов (бэк реализует после дизайна)

## Endpoints (prefix `/api`)

### `GET /api/widgets` — список доступных виджетов
```json
[
  {"id":"latency","title":"Latency","size":"M","color":"mint","endpoint":"/api/metrics/latency"},
  {"id":"saved","title":"Saved Tokens","size":"M","color":"yellow","endpoint":"/api/metrics/saved"},
  {"id":"health","title":"Health Matrix","size":"L","color":"red","endpoint":"/api/health"},
  {"id":"routing","title":"Routing Sankey","size":"XL","color":"blue","endpoint":"/api/routing"},
  {"id":"cache","title":"Cache Hit","size":"M","color":"pink","endpoint":"/api/cache/stats"},
  {"id":"cost","title":"Cost","size":"M","color":"mint","endpoint":"/api/cost"}
]
```

### `GET /api/metrics/latency` — для виджета Latency
```json
{"p50":4,"p95":7,"ttfb":3,"series":[4,5,3,6,4]}
```

### `GET /api/widgets/layout` / `POST /api/widgets/layout`
```json
// GET
{"layout":[{"id":"latency","x":0,"y":0,"w":4,"h":2},{"id":"health","x":4,"y":0,"w":4,"h":4}]}
// POST body same
```

### `POST /api/widgets/custom`
```json
{"title":"My Widget","size":"M","color":"yellow","apiUrl":"/api/metrics/custom","template":"Latency {{p95}}ms"}
```

## Хедеры от gateway (уже есть)
- `X-Gateway-Route`, `X-Gateway-Attempts`, `X-Gateway-Upstream-MS`, `X-Gateway-Saved-Bytes`, `X-Gateway-Cache: HIT/MISS`, `X-Gateway-Caveman`, `X-Gateway-Headroom`

## Хранение
- `widgets_layout` table (or `~/.2papi/layout.json` в lite mode)

Бэк: `control-plane/app/api/widgets/*` + `internal/dashboard` embed.
