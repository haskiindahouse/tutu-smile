# MCP-клиент и слой tutu

## internal/mcp: клиент к Tutu MCP

JSON-RPC 2.0 поверх HTTP POST, метод `tools/call`. Payload приходит двойно закодированным: JSON-строка внутри `result.content[0].text`. Заголовок `Accept: application/json, text/event-stream`; `decodeRPC` понимает и plain JSON, и SSE (буфер до 8 МБ). HTTP ≥ 500 считается retryable, ≥ 400 нет; `isError=true` в ответе становится `ToolError`.

Живой прогон показал: неограниченный фан-аут ловит TLS-бан WAF'а. Поэтому нагрузку на MCP держат четыре механизма.

**Кэш TTL.** Ключ = sha256(`tool` + `\x00` + JSON args)[:16] в hex, дефолтный TTL 90 с. Флаги `noCache`/`fresh` обходят кэш; `create_checkout_link` всегда идёт без кэша.

**Single-flight.** Одновременные одинаковые вызовы схлопываются в один сетевой: map `flight[key] → *call{done, payload, err}`.

**Ретраи.** Backoff 500 мс × 2 на попытку, попыток `retries` (дефолт 2), но не меньше числа прокси в пуле, чтобы обойти мёртвые IP.

**Politeness-gate.** Семафор-канал (потолок 4 одновременных) плюс минимальный зазор 150 мс между запросами через общий `lastReq`. При N > 1 прокси зазор делится на N.

**Circuit breaker.** 5 подряд транспортных ошибок открывают breaker на 60 с; вызовы падают мгновенно с `ErrCircuitOpen` («сервер отдыхает»). Успех сбрасывает счётчик. Состояние торчит в `/healthz` и останавливает планировщик перепроверок: табло держит последнее честное состояние, а шапка фронта показывает «Туту остывает».

**Прокси-пул.** `ParseProxy` принимает `http://user:pass@host:port` и `host:port[:user:pass]`. У прокси-соединений свои таймауты: dial 4 с, TLS handshake 5 с, ResponseHeaderTimeout равен общему таймауту, поэтому мёртвый IP не сжигает бюджет запроса. Round-robin по здоровым; прокси с 2 подряд ошибками бенчится на 10 минут; если отбенчены все, берётся следующий по кругу («вдруг ожил»). Метрики: `Stats()`, `ProxyStats()`, `ProxyCount()`.

## internal/tutu: типизированный домен

Дергаемые MCP-методы:

| Метод MCP | Обёртка | Детали |
|---|---|---|
| `search_multitransport` | `SearchMulti` | `optimize_for ∈ {price,time}`, `page_size=30`, флаг `fresh` обходит кэш |
| `search_rail` | `SearchRail` | Парсит `meta.interchange_routes` в `[]InterchangePlan` |
| `search_avia` | `SearchAvia` | Взрослые + дети |
| `create_checkout_link` | `CreateCheckoutLink` | `checkout_ref` передаётся байт-в-байт, никогда не пересобирается |
| `search_hotels` | `SearchHotels` | На входе цена за ночь, в ответе `best_offer.price` — total за весь стей |
| `get_rail_seatmap` | `SeatsTogether` | `{details_ref, task:"together", seats_together:N}`; статус ≠ "ok" — не ошибка |

Полный чейн до Туту: поиск → `checkout_ref` → `create_checkout_link` → deeplink «дожать». Seatmap читается по `details_ref` тем же чейном. `SeatGroup.Human()` рендерит «вагон N (купе 2Ш), места X и Y рядом — Z₽ за двоих».

**Гео-индекс** (geo.go): хардкод-таблица `cityCoords` (~75 городов с алиасами «спб»/«петербург») нужна, потому что гео-id MCP не совпадают с lon/lat. Тут же 11 транспортных хабов, `IsHub`, `NearestHub` (ближайший по квадрату дистанции, иначе Москва) и `Cities()` для автокомплита.

**Теги интересов** (tags.go): 11 интересов (fishing, swim, food, nature, mountains, history, party, chill, spa, insta, exotic), ~60 городов РФ и безвизовых направлений, у заграничных флаг `visaFree`. `CitiesByInterests` делает OR-матч со скорингом по числу пересечений и исключает город отправления.

## internal/llm: OpenRouter

Тонкий клиент `POST /api/v1/chat/completions`, temperature 0.4, `response_format: json_object` в jsonMode. Fallback-цепочка перебирает модели по порядку, первая ответившая побеждает. Дефолт: `google/gemini-3.6-flash` → `openai/gpt-5.6-luna` → `z-ai/glm-5.2`. `extractJSON` снимает ```` ```json ````-фенсы и вырезает подстроку `{…}`.

Задачи LLM:

- `ParseEvent(text, prior, now)` — свободный текст, диктовка или переписка чата → `EventDraft`. Промпт фиксирует «сегодня» и день недели, разворачивает относительные даты, запрещает выдумки. С `prior` получается мерж-правка (так работает «управление словами»).
- `ExpandVibe(wish, guestCities)` → `VibeSpec{cities(4–8), budget_rub, constraints}`.
- `WriteCard(input)` — тёплая карточка маршрута, максимум 2 предложения.
- `CityPlan(city, interests, days)` — пеший план, 4–6 стопов на день, помечен «сгенерировано ИИ».
- `Spots(city, interests)` — 6–8 фотогеничных мест для ленты впечатлений.

Деградация без ключа: `ParseEvent` и `Spots` возвращают ошибку (API отвечает 503 с пояснением). `ExpandVibe` переходит на `fallbackVibe` (эвристика по подстрокам: «море» → Сочи, «горы» → Пятигорск, «история» → Золотое кольцо). `WriteCard` — на `fallbackCard` (детерминированное предложение из фактов). `CityPlan` — на `fallbackPlan` («Классический круг»: площадь, музей, рынок, набережная).
