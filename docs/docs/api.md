# HTTP API

Все маршруты регистрируются в `internal/api/server.go` через `http.NewServeMux` (паттерны Go 1.22+) за middleware `withCORS` (Allow-Origin `*`, GET/POST/OPTIONS). Статика отдаётся с SPA-фолбэком на `index.html` и заголовком `Cache-Control: no-cache`. Ошибки приходят как `{"error": msg}`.

## Служебные

| Метод | Путь | Что делает |
|---|---|---|
| GET | `/healthz` | Статус: `llm_enabled`, счётчики `mcp_calls`/`mcp_cache_hits`, состояние breaker, пул прокси, число событий |
| GET | `/api/config` | `mapbox_token`, `llm_enabled`, `recheck_sec`, список городов для автокомплита |

## Умный ввод и подбор

| Метод | Путь | Вход → выход |
|---|---|---|
| POST | `/api/parse` | `{text, draft?}` → LLM `ParseEvent` → `{draft}`. 503 без ключа LLM. Таймаут 45 с |
| POST | `/api/vibe` | `{vibe, date, deadline, buffer_hours(=2), guests[]}` → `ExpandVibe` + `RankVibeCities` → `{spec, cities}`. 60 с |
| POST | `/api/meet` | «Увидеться вдвоём»: `{city_a, city_b, date, deadline(=15:00), interests[]}` → города встречи, ранжированные полной ценой для двоих. Пул кандидатов: плитки интересов → LLM → хардкод из 12 хабов. 90 с |
| POST | `/api/spots` | `{city, interests[]}` → лента впечатлений. 503 без LLM. 50 с |
| POST | `/api/cityplan` | `{city, interests[], days(1..7)}` → пеший план по дням. 60 с |

## Рулетка

| Метод | Путь | Вход → выход |
|---|---|---|
| POST | `/api/roulette/pool` | Мгновенно, без MCP: перемешанный пул городов под фильтры. 400 если пусто |
| POST | `/api/roulette` | Батч: `{origin, date, days(1..14), budget, interests[], scope, visa_free, exclude_modes[], cities[]}` → полные цены (туда + обратно + отель) по ≤ 8 кандидатам. Если оценено меньше двух и origin не хаб — плечо до `NearestHub` и пересчёт. 404 если ничего живого. 150 с |
| POST | `/api/roulette/price` | `{origin, city, backups[](до 2), ...}`: город и запасные считаются параллельно, отдаётся первый живой. `landed ≠ city` → `note` «колесо довернулось». 120 с |

## События

| Метод | Путь | Что делает |
|---|---|---|
| POST | `/api/events` | Создаёт событие. Валидирует `destination` и непустой `guests`, применяет дефолты (buffer 2ч, spacing 20 мин, профиль cheaper), отдаёт плейсхолдер-борд мгновенно и собирает маршруты в фоне (90 с). 201 `{id, event}` |
| GET | `/api/events` | Список событий |
| GET | `/api/events/{id}` | `{event, board}` |
| GET | `/api/events/{id}/board` | Текущий борд |
| GET | `/api/events/{id}/stream` | SSE: `retry: 3000`, события `event: board`, keep-alive-комментарий каждые 20 с |
| GET | `/api/events/{id}/ics` | VCALENDAR/VEVENT на момент сбора (+3 ч), файл `ulybka-{id}.ics` |
| POST | `/api/events/{id}/recheck` | 202, фоновая пересборка с `fresh=true` (60 с) |
| POST | `/api/events/{id}/guests` | Добавляет гостя (ID `g<N>` если пуст) и пересобирает борд |
| POST | `/api/events/{id}/amend` | Управление словами: LLM парсит правку поверх текущего события, мерж без удалений. Ответ `{status, changes[]}`. 503 без LLM |
| GET | `/api/events/{id}/join` | Публичный минимум приглашения: имя, место, дата, дедлайн, число гостей |
| POST | `/api/events/{id}/join` | Саморегистрация гостя `{name, city, profile, adults, children, needs_lodging, find_companions}`. Дедуп по name+city (обновляет, не дублирует). Ответ `{guest_id, event_id}` |
| POST | `/api/events/{id}/demo/collapse` | Сценарная кнопка для демо: у первого подходящего гостя chosen меняется на альтернативу со статусом `reassembled`. Триггер ручной, маршрут реальный |

## Карточка гостя

| Метод | Путь | Что делает |
|---|---|---|
| GET | `/api/events/{id}/guest/{gid}` | Карточка: строка гостя + попутчики (чужое имя скрыто до взаимного согласия) + флаги `purchased/pinned_key/find_companions/companion_consent` |
| POST | `…/guest/{gid}/choose` | Пин варианта по `{key}`: swap chosen ↔ alternative на клоне борда, запись `pinned` в журнал, фоновая пересборка. 409 если билет куплен |
| POST | `…/guest/{gid}/purchased` | Фриз/анфриз строки. При покупке `PinnedKey = chosen.Key`, статус `purchased`; при снятии — обратно в `assembled` + пересборка |
| POST | `…/guest/{gid}/consent` | `{consent}`: пересчитывает попутчиков на текущем борде, сохраняя `SeatHint` |

Конкурентность обслуживают два хелпера: `cloneBoard` делает глубокую копию борда через JSON, потому что SSE-подписчики держат ссылки на старый борд; `kickBuild`/`kickRebuild` запускают пересборку в горутине с 90-секундным таймаутом.
