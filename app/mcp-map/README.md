# mcp-map — полная карта MCP-сервера tutu (mcp.tutu.ru/mcp)

Снято анонимно (без кредов, stateless) в ходе кампании tutu, 2026-08-19.
Все файлы — сырые JSON-RPC ответы сервера (`result` внутри). Снапшот актуален
на дату снятия; сервер stateless, схемы версионируются `schema_fingerprint`
(см. resource-version.json).

## Протокол и контракты

| Файл | Что внутри |
|---|---|
| `initialize.json` | serverInfo (tutu-mcp-server 0.38.0), capabilities, полные server instructions |
| `tools-list.json` | все 16 tools с **полными inputSchema** (search_{hotels,avia,rail,bus,etrain,multitransport}, get_offer_details, get_rail_seatmap, create_checkout_link, fetch_resource, 6× get_*_instructions) |
| `prompts-list.json` | 1 prompt: plan_trip (origin/destination/dates/budget_rub) |
| `resources-list.json` | все 7 ресурсов с дескрипторами |

## Плейбуки (внутренняя документация по доменам: поля, edge-cases, правила)

| Файл | Домен | Размер |
|---|---|---|
| `playbook-avia.json` | авиа | ~10 КБ |
| `playbook-rail.json` | жд | ~28 КБ |
| `playbook-bus.json` | автобусы | ~4.5 КБ |
| `playbook-etrain.json` | электрички | ~2.7 КБ |
| `playbook-hotels.json` | отели | ~10 КБ |
| `playbook-multitransport.json` | мультитранспорт | ~4.2 КБ |

## Ресурсы (полные тела)

| Файл | Содержимое |
|---|---|
| `resource-status.json` | карта 6 внутренних апстримов + живость + семантика состояний (находка a003) |
| `resource-debug-memory.json` | RSS/GC/перепись кучи, active_mcp_sessions (a003) |
| `resource-version.json` | python 3.12.14, app 0.38.0, schema_fingerprint, uptime |
| `resource-geo.json` | внутренние geo_id/point_id |
| `resource-amenities.json` | апстримовые коды удобств (rail/bus) |
| `resource-special-offers.json` | демо-офферы |
| `resource-help-overview.json` | внутренний флоу search→details→checkout, legacy-алиасы |

## Модели ответов (живые образцы — вне этого каталога)

Сырые полные ответы tools на реальных вызовах лежат в evidence находок:
- `findings/a004-offer-hash-idor/evidence/` — search_avia (2 маршрута), search_rail, get_offer_details (avia+rail), анализ offer_hash
- `findings/a005-search-rate-limit/evidence/` — серия search_avia (series/1..5.body), search_multitransport (08-heavy-multitransport.json)
- `findings/a001-checkout-tampering/evidence/` — create_checkout_link req05-13 (baseline + 8 мутаций)
- `findings/a006-prompt-surface-injection/evidence/` — prompts/get plan_trip (рендер, 08/12/13), тексты ошибок с echo

## Чего в карте нет (принципиально)

- outputSchema на уровне tools/list сервер не публикует — модели ответов восстановлены по живым образцам (выше) и плейбукам.
- Внутренние апстримы из resource-status вне SCOPE — не опрашивались.
