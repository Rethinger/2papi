# Дашборд — план (квота + виджеты + навигация)

Продолжение `widgets-spec.md` + `api-contract.md`. Добавляет **квоту единым %-баром** и
вкладку деталей. Frontend-спека — дизайнеру рядом с виджетами.

## 1. Главная (Overview) — 3 зоны

### A. Combined Quota bar (sticky, сверху)
```
┌───────────────────────────────────────────────────────────┐
│  QUOTA · 62%                              claude 62 ·      │
│  ████████████░░░░░░░░░░░░░░░░░░  (1 бар, % от всех)      │
│  used/total · сброс: claude 3д4ч · free 12%               │
│  [ Подробнее → ]                                          │
└───────────────────────────────────────────────────────────┘
```
- **Один % + бар** = `min(Σ used_i / Σ limit_i, 1)` по активным аккаунтам.
- Цвет: `<70%` зелёный · `70-90%` жёлтый · `>90%` красный (карандаш-палитра).
- Кнопка `Подробнее` → вкладка `/quota`.
- Small print под баром: `claude 62% · codex 40% · free 12%`.

### B. Виджетный грид (ios-like, `widgets-spec.md`)
- 6 карточек: Latency(4×2) · Saved Tokens(4×2) · Cache Hit(4×2) · Cost(4×2)
  · Health Matrix(8×4) · Routing Sankey(XL).
- Drag/resize/`+ Custom`, pencil-hover (wobble 0.6s) — уже в спека.
- Квота sticky над гридом, не виджет.

### C. Ниже (не меняем)
- Trend-график (`request-trends`), Account pool, Snapshot rail.

## 2. Вкладка `/quota` (куда «Подробнее»)

### Нав
- Новый view `quota` в sidebar (иконка Gauge), путь `/quota` (`view-router.ts`).

### Таблица по провайдерам
| Провайдер | Тип | Адаптер | Исп/Лимит | % | Бар | Сброс | Статус |
|---|---|---|---|---|---|---|---|
| Cursor | oauth | cursor | 120k/200k | 60% | ████░ | 3д4ч | active |
| Claude Pro | cookie | claudeai | 41/90 msg | 45% | ███░░ | 5h | active |
| Codex | oauth | codex | 340k/1M | 34% | ██░░░ | вскр | active |
| OpenCode | free | opencode | 12k/∞ | — | ───── | monthly | free |

- Сводный mini-бар сверху.
- `Reset` (где есть reset-credit, напр. codex) или `Open usage page` ссылка.
- `Enable/Disable` на строку.

## 3. Бэкенд-контракт `GET /api/control/v1/quota`
```json
{
  "percent": 42,
  "providers": [
    {
      "account": "claude-primary", "kind": "oauth", "family": "claude",
      "used": 120000, "limit": 200000, "percent": 60,
      "reset_at": "2026-08-23T18:00:00Z", "status": "active"
    }
  ]
}
```
- `percent` = сводный (used/limit по всем), `providers` — per-account.
- Источник: `internal/quota` + headers `X-Provider-Quota-*` + codex credits.
- Для `free` `limit=null`, bar залит серым.

## 4. Навигация (финальная)
```
Overview · Requests · Providers · Models · Keys · Teams · Quota · Plugins · Audit · Settings
```
- `overview` → Quota bar sticky + грид + тренд + pool
- `quota` → детализация (via «Подробнее»)
- `providers` → OAuth/Cookie/Free + Connect (после backend-PR1)
- `plugins` → sidecar-плагины toggle (после backend-PR3)
- остальные — без изменений.

## 5. Порядок реализаии (бэк → фронт)
1. `/api/control/v1/quota` (бэк, из `internal/quota`).
2. Вкладка `/quota` (таблица).
3. Combined bar на главной + «Подробнее».
4. Виджет-грид (drag) — после кастомизации.
5. Providers/Plugins вкладки — после их backend PR-ов.
