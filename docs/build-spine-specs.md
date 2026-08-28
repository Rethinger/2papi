# Build-хребет: спеки (цикл I, 2026-08-22)
Источник очереди: 2papi-3-editions-strategy.md v3 «Очередь Build».

## Шаг 1 — License-key сервис (Enterprise)

### Формат файла лицензии
`<prefix>:<base64url(payload)>.<base64url(sig)>` — одна строка, без
переносов. Prefix задаёт издание (`ent:` / `cloud:`), остальное — подпись.

### Payload — канонический JSON (сортировка ключей лексикографическая,
без пробелов), поля:
- `ed`   : "ent" | "cloud"            — издание
- `cid`  : строка                     — id клиента (для нашей учёбы)
- `cap`  : число                      — годовая ёмкость запросов (млн),
                                        для прайсинга/фейр-юза; 0 = без лимита
- `iat`  : unix sec                   — выпуск
- `exp`  : unix sec                   — истечение (trial = iat+30д)
- `f`    : массив строк               — включённые фичи
          ["sso","orgs","audit_export","secrets","ipacl","guardrails",
           "multiregion","branding","cc_gateway"]
- `nbf`  : опц., unix sec             — не раньше

### Подпись
- Ed25519 (golang.org/x/crypto/ed25519, в stdlib-экосистеме Go).
- Приватный ключ ТОЛЬКО у нас (генератор keygen в private-репо);
  публичный ключ зашит в бинарь (hex-константа + env override
  `2PAPI_LICENSE_PUBKEY` для ротации/тестов).
- sig = ed25519.Sign(priv, bytes(prefix + "." + b64payload)) — префикс
  входит в подпись, подмена издания невозможна.

### Валидация (internal/license)
1. Парс строки → prefix/payload/sig.
2. ed25519.Verify(pub, prefix+"."+b64payload, sig).
3. Проверки: exp > now ≥ nbf; ed совпадает с prefix; f ⊆ известные.
4. Кэш результата на время процесса + re-read файла по SIGHUP/mtime.
5. Любая ошибка ⇒ издание деградирует в oss (уже так делает edition).
Никаких сетевых вызовов — офлайн и air-gap работают из коробки.

### Генератор (private tool, НЕ в OSS-репо)
`licensegen -ed ent -cid acme -cap 50000 -days 365 -features sso,orgs,...`
→ печатает строку лицензии. Trial: те же флаги, days=30, cap=1000,
`-trial` ставит флаг в payload для аналитики конверсии триалов.

### Тесты
подпись валидна/битая; просрочка; nbf в будущем; подмена префикса
(ent→cloud при cloud-подписи) → reject; ротация pubkey через env.

## Шаг 2 — /metrics + аудит-экспорт (эскиз)
- Prometheus: стандартный /metrics (prometheus/client_golang):
  счётчики запросов по alias/source/status, гистограммы TTFB,
  gauge circuit states, cache hit rate, токены. OSS.
- Экспорт аудита: GET /api/admin/audit/export?from&to → NDJSON/CSV;
  гейт фичей `audit_export`.

## Шаг 3 — Organizations (эскиз)
- organizations (id, name, owner_user_id); teams += org_id NULLABLE
  (команды без орги = личные, back-compat); роли org_admin/team_admin
  на team_members.role расширением CHECK ('owner','member','org_admin');
  policy.go: бюджет орги как верхняя граница бюджетов команд.
