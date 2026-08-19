/* «Улыбка» — живое табло события и карточка гостя. Загружается после app.js. */
'use strict';

let BC = null; // board context

function Board(id) {
  teardownBoard();
  const root = app(); clear(root);
  BC = { id, es: null, board: null, event: null, map: null, mapReady: false, prevStatus: {}, selected: null, tick: null };

  root.append(el('div', { class: 'empty' }, el('span', { class: 'spinner' }), ' загружаю событие…'));

  api.get('/api/events/' + id).then(({ event, board }) => {
    BC.event = event;
    renderShell();
    if (board) applyBoard(board);
    openStream(id);
    BC.tick = setInterval(updateCountdown, 1000);
  }).catch((e) => { clear(root); root.append(el('div', { class: 'empty' }, 'Событие не найдено: ' + e.message)); });
}

function teardownBoard() {
  if (BC && BC.es) BC.es.close();
  if (BC && BC.tick) clearInterval(BC.tick);
  if (BC && BC.map) { try { BC.map.remove(); } catch {} }
  BC = null;
}

function renderShell() {
  const root = app(); clear(root);
  const ev = BC.event;
  // native append() stringifies null — wrap in el() which skips null kids
  root.append(el('div', {},
    el('div', { style: 'display:flex;align-items:center;gap:10px;margin-bottom:6px' },
      el('button', { class: 'btn ghost sm', onclick: () => Router.go('/') }, '← события'),
      el('div', { style: 'flex:1' }),
      el('button', { class: 'btn sm', onclick: copyJoinLink, title: 'одна ссылка в чат — гости отмечаются сами' }, 'позвать гостей'),
      el('a', { class: 'btn ghost sm', href: '/api/events/' + BC.id + '/ics', title: 'сбор — в календарь' }, 'в календарь'),
      el('button', { class: 'btn ghost sm', id: 'btn-recheck', onclick: recheck }, '⟳ перепроверить'),
      el('button', { class: 'btn ghost sm', onclick: demoCollapse, title: 'демо: имитировать распад маршрута' }, 'рассыпать'),
    ),
    boardAmendBar(),
    el('div', { class: 'board-head' },
      el('div', { class: 'title' },
        el('h1', {}, ev.name || 'Событие'),
        el('div', { class: 'meta', id: 'b-meta' }),
      ),
      el('div', { class: 'spacer' }),
      el('div', { class: 'countdown' },
        el('div', { class: 'big', id: 'b-cd' }, '—:—:—'),
        el('div', { class: 'lbl' }, 'до сбора'),
      ),
    ),
    el('div', { class: 'assembled-bar' },
      el('div', { class: 'track' }, el('div', { class: 'fill', id: 'b-fill' })),
      el('div', { class: 'count', id: 'b-count' }, '—'),
      el('button', { class: 'btn ghost sm', onclick: copyAllInvites, title: 'скопировать приглашения всем гостям одним текстом' }, 'всем'),
    ),
    el('div', { id: 'b-voice' }),
    el('div', { class: 'split' },
      el('div', {},
        el('div', { class: 'tablo' },
          el('div', { class: 'tablo-header' },
            el('div', {}, ''), el('div', {}, 'Гость'), el('div', {}, 'Маршрут'),
            el('div', { style: 'text-align:right' }, 'Прибыт.'), el('div', { style: 'text-align:right' }, 'Цена'), el('div', { style: 'text-align:right' }, 'Статус')),
          el('div', { id: 'tablo-body' }),
        ),
        el('div', { id: 'b-detail' }),
      ),
      el('div', {},
        el('div', { class: 'side-card' }, el('h3', {}, 'Карта сбора'), el('div', { id: 'map' }),
          el('div', { class: 'map-legend' },
            el('span', {}, el('i', { style: 'background:var(--ok)' }), 'собран'),
            el('span', {}, el('i', { style: 'background:var(--dawn1)' }), 'риск'),
            el('span', {}, el('i', { style: 'background:var(--red)' }), 'помощь'),
            el('span', {}, el('i', { style: 'background:var(--red)' }), 'место'))),
        el('div', { class: 'side-card' }, el('h3', {}, 'Волна прибытий', el('span', { class: 'k', id: 'wave-k' })), el('div', { id: 'b-wave' })),
        el('div', { class: 'side-card', id: 'comp-card', style: 'display:none' }, el('h3', {}, 'Попутчики'), el('div', { id: 'b-comp' })),
        el('div', { class: 'side-card', id: 'bets-card', style: 'display:none' }, el('h3', {}, 'Тотализатор', el('span', { class: 'k' }, 'кто опоздает')), el('div', { id: 'b-bets' })),
      ),
    ),
  ));
  const meta = $('#b-meta');
  meta.textContent = `${ev.destination} · ${fmtDay(ev.date)} · сбор ${ev.deadline} · буфер ${ev.buffer_hours} ч · зазор ${ev.spacing_min} мин`;
  initMap();
}

// copyJoinLink: the group-planning move — one link, guests add themselves.
function copyJoinLink() {
  const ev = BC.event;
  const link = location.origin + '/#/j/' + BC.id;
  copyText(
    `${ev.name || 'Событие'} — ${ev.destination}, ${fmtDay(ev.date)}, сбор к ${ev.deadline}.\n` +
    `Отметься, откуда едешь — маршрут соберётся сам: ${link}`,
    'Ссылка-приглашение скопирована — киньте её в чат');
}

// boardAmendBar: run the event with words, right on the live board —
// «перенеси сбор на 16:00, добавь Свету из Уфы с ночёвкой» (or dictate it).
function boardAmendBar() {
  if (!CFG.llm_enabled) return null;
  let text = '';
  const input = el('input', {
    placeholder: 'скажи табло: «перенеси сбор на 16:00, добавь Свету из Уфы с ночёвкой»…', style: 'flex:1',
    oninput: (e) => text = e.target.value,
    onkeydown: (e) => { if (e.key === 'Enter') apply(); },
  });
  const btn = el('button', { class: 'btn sm', onclick: apply }, 'применить');
  async function apply() {
    if (!text.trim()) return;
    btn.disabled = true; btn.textContent = '…';
    try {
      const res = await api.post('/api/events/' + BC.id + '/amend', { text });
      toast(res.changes && res.changes.length ? '' + res.changes.join(' · ') : 'Изменений не увидел');
      const ev = await api.get('/api/events/' + BC.id);
      BC.event = ev.event;
      input.value = ''; text = '';
      // Light refresh: header facts + board; the map keeps its instance.
      const h1 = $('.board-head h1'); if (h1) h1.textContent = ev.event.name || 'Событие';
      const meta = $('#b-meta');
      if (meta) meta.textContent = `${ev.event.destination} · ${fmtDay(ev.event.date)} · сбор ${ev.event.deadline} · буфер ${ev.event.buffer_hours} ч · зазор ${ev.event.spacing_min} мин`;
      if (ev.board) applyBoard(ev.board);
    } catch (e) { toast(e.message); }
    btn.disabled = false; btn.textContent = 'применить';
  }
  const mic = micButton(() => text, (t) => { text = t; input.value = t; });
  return el('div', { class: 'amend-bar' }, mic, input, btn);
}

function openStream(id) {
  const es = new EventSource('/api/events/' + id + '/stream');
  BC.es = es;
  es.addEventListener('board', (e) => { try { applyBoard(JSON.parse(e.data)); } catch {} });
  es.onerror = () => { /* browser auto-reconnects */ };
}

function applyBoard(board) {
  BC.board = board;
  renderTablo(board);
  renderAssembled(board);
  renderVoice(board);
  renderWave(board);
  renderCompanions(board);
  renderBets(board);
  updateMap(board);
  updateCountdown();
  if (BC.selected) renderDetail(BC.selected); // refresh open detail
}

// «Голос события» (ТЗ §6): единственный случай, когда табло говорит — просит
// помощи, и только фактами.
function renderVoice(board) {
  const box = $('#b-voice'); if (!box) return; clear(box);
  const asks = [];
  for (const r of board.rows) {
    if (r.status !== 'needs_help') continue;
    const why = (r.risk_reasons && r.risk_reasons[0]) || 'маршрут не собирается';
    asks.push(`мне нужна помощь с гостем «${r.guest_name}» из ${r.city}: ${why}`);
  }
  if (!asks.length) return;
  box.append(el('div', { class: 'event-voice' },
    el('div', { class: 'ev-title' }, 'Событие просит помощи'),
    ...asks.map(a => el('div', { class: 'ev-line' }, '— ' + a)),
  ));
}

function copyAllInvites() {
  if (!BC.board) return;
  const parts = [];
  for (const r of BC.board.rows) {
    const link = location.origin + '/#/g/' + BC.id + '/' + r.guest_id;
    const routeLine = r.human_card
      || (r.chosen ? `Маршрут: ${r.chosen.mode_human} ${r.chosen.number || ''}, прибытие ${fmtTime(r.chosen.arrival_at)}, ${money(r.chosen.price)}.` : 'Маршрут ещё собирается.');
    parts.push(`${r.guest_name} (${r.city}):\n${routeLine}\nКарточка: ${link}`);
  }
  const ev = BC.event;
  copyText(`${ev.name || 'Событие'} — ${ev.destination}, ${fmtDay(ev.date)}, сбор к ${ev.deadline}.\n\n` + parts.join('\n\n'),
    'Приглашения всем скопированы — рассылайте');
}

// ---------- tablo ----------
function renderTablo(board) {
  const body = $('#tablo-body'); if (!body) return;
  clear(body);
  for (const r of board.rows) {
    const changed = BC.prevStatus[r.guest_id] && BC.prevStatus[r.guest_id] !== r.status;
    BC.prevStatus[r.guest_id] = r.status;
    const c = r.chosen;
    const routeCell = c
      ? el('div', { class: 'route' },
          el('span', { class: 'mode-ic' }, modeIc(c.mode)),
          el('span', {}, c.mode_human, ' '), el('span', { class: 'num' }, c.number || ''),
          c.night_before ? el('span', { class: 'tag night' }, 'ночной') : null,
          c.complex ? el('span', { class: 'tag complex' }, c.via ? 'через ' + c.via : 'пересадка') : null,
          el('div', { class: 'sub' }, shortCity(c.from_station) + ' → ' + shortCity(c.to_station) + (c.duration_min ? ' · ' + fmtDur(c.duration_min) : '')))
      : el('div', { class: 'route faint' }, r.status === 'planning' ? 'считаю живые маршруты…' : 'нет маршрута к дедлайну');
    const row = el('div', { class: 'tablo-row status-' + r.status + (changed ? ' flap' : ''), onclick: () => selectRow(r.guest_id) },
      el('div', {}, el('div', { class: 'status-dot' })),
      el('div', { class: 'who' }, el('div', { class: 'name' }, r.guest_name,
        r.pinned && !r.purchased ? el('span', { class: 'tag pin', title: 'гость выбрал вариант сам' }, 'выбор гостя') : null,
        r.needs_lodging ? el('span', { class: 'tag lodging' }, 'ночлег') : null), el('div', { class: 'city' }, r.city)),
      routeCell,
      el('div', { class: 'arr' }, c ? fmtTime(c.arrival_at) : '—'),
      el('div', { class: 'price' }, c ? money(c.price) : '—'),
      el('div', { class: 'st' }, el('span', { class: 'badge ' + r.status }, STATUS_RU[r.status] || r.status)),
    );
    body.append(row);
  }
  body.append(addGuestRow());
}

// Inline add-guest: the board grows without leaving it.
function addGuestRow() {
  const g = { name: '', city: '', profile: 'cheaper' };
  const nameIn = el('input', { placeholder: '＋ имя', oninput: (e) => g.name = e.target.value, onclick: (e) => e.stopPropagation() });
  const cityIn = el('input', { placeholder: 'город', list: 'cities', oninput: (e) => g.city = e.target.value, onclick: (e) => e.stopPropagation(),
    onkeydown: (e) => { if (e.key === 'Enter') add(); } });
  async function add() {
    if (!g.city.trim()) { toast('Укажите город гостя'); return; }
    try {
      await api.post('/api/events/' + BC.id + '/guests', { name: g.name || 'Гость', city: normalizeCityInput(g.city), profile: g.profile, adults: 1 });
      toast('Гость добавлен — считаю маршрут…');
    } catch (e) { toast(e.message); }
  }
  return el('div', { class: 'tablo-row add-row', onclick: (e) => e.stopPropagation() },
    el('div', {}, ''),
    el('div', { style: 'display:flex;gap:8px' }, nameIn, cityIn),
    el('select', { onchange: (e) => g.profile = e.target.value, onclick: (e) => e.stopPropagation() },
      el('option', { value: 'cheaper' }, 'дешевле'), el('option', { value: 'faster' }, 'быстрее')),
    el('div', {}), el('div', {}),
    el('div', { class: 'st' }, el('button', { class: 'btn ghost sm', onclick: add }, '＋')),
  );
}

function renderAssembled(board) {
  const fill = $('#b-fill'), count = $('#b-count'); if (!fill) return;
  const pct = board.total ? Math.round(100 * board.assembled / board.total) : 0;
  fill.style.transform = 'scaleX(' + (pct / 100) + ')';
  clear(count);
  count.append(el('b', {}, board.assembled + ''), ' / ' + board.total + ' улыбок в сборе');
  // «Цена улыбки»: во сколько обходится один обнятый гость. Метрика бренда:
  // сервис продаёт не билеты, а встречу.
  const priced = board.rows.filter(r => r.chosen);
  if (priced.length) {
    const total = priced.reduce((s, r) => s + r.chosen.price, 0);
    count.append(el('span', { class: 'faint', title: 'дорога всех ÷ количество гостей' },
      ' · улыбка ≈ ' + money(total / priced.length)));
  }
  // Все в сборе — единственный момент, когда табло позволяет себе праздник.
  if (board.total > 0 && board.assembled === board.total && !BC._celebrated) {
    BC._celebrated = true;
    smileRain();
  }
  if (board.assembled < board.total) BC._celebrated = false;
}

// smileRain drops a brief shower of smiles when the last guest is assembled.
function smileRain() {
  const n = 36;
  for (let i = 0; i < n; i++) {
    const s = el('span', { class: 'smile-drop' }, Math.random() < 0.85 ? '🙂' : '💛');
    s.style.left = (Math.random() * 100) + 'vw';
    s.style.animationDelay = (Math.random() * 1.2) + 's';
    s.style.fontSize = (16 + Math.random() * 22) + 'px';
    document.body.append(s);
    setTimeout(() => s.remove(), 4200);
  }
  toast('Все улыбки в сборе :)');
}

function shortCity(s) { if (!s) return ''; const i = s.search(/[—(,]/); return i > 0 ? s.slice(0, i).trim() : s; }

// ---------- detail ----------
function selectRow(gid) { BC.selected = (BC.selected === gid) ? null : gid; renderDetail(BC.selected); }

function renderDetail(gid) {
  const box = $('#b-detail'); if (!box) return; clear(box);
  if (!gid || !BC.board) return;
  const r = BC.board.rows.find(x => x.guest_id === gid); if (!r) return;
  const c = r.chosen;
  const card = el('div', { class: 'card', style: 'margin-top:16px;box-shadow:inset 0 0 0 2px rgba(34,30,26,.28)' },
    el('div', { style: 'display:flex;align-items:center;gap:10px;margin-bottom:6px;flex-wrap:wrap' },
      el('h2', { style: 'font-size:18px;margin:0' }, r.guest_name),
      el('span', { class: 'badge ' + r.status }, STATUS_RU[r.status] || r.status),
      r.pinned && !r.purchased ? el('span', { class: 'tag pin' }, 'выбор гостя') : null,
      el('div', { style: 'flex:1' }),
      el('button', { class: 'btn ghost sm', onclick: () => copyInvite(r) }, 'приглашение'),
      el('button', { class: 'btn ghost sm', onclick: () => window.open('#/g/' + BC.id + '/' + gid, '_blank') }, 'карточка гостя'),
      el('button', { class: 'btn ghost sm', onclick: () => selectRow(gid) }, ''),
    ),
    r.human_card ? el('p', { style: 'margin:6px 0 14px;font-size:15px' }, r.human_card) : null,
  );
  if (c) {
    card.append(routeFacts(c));
    if (c.checkout_url)
      card.append(el('a', { class: 'btn primary dogzhat', href: c.checkout_url, target: '_blank', rel: 'noopener' },
        'Дожать на Туту — ' + money(c.price) + (c.complex ? ' (первый сегмент)' : '')));
    if (c.complex && c.leg_links && c.leg_links.length > 1)
      card.append(el('div', { class: 'faint', style: 'font-size:12px;text-align:center;margin-top:8px' }, 'Сложный маршрут — билеты берутся по сегментам, ссылки в карточке гостя.'));
  }
  if (r.risk_reasons && r.risk_reasons.length)
    card.append(el('div', { style: 'margin-top:14px' }, el('div', { class: 'section-title' }, 'Почему такой статус'),
      ...r.risk_reasons.map(x => el('div', { class: 'muted', style: 'font-size:13px' }, '• ' + x))));
  if (r.alternatives && r.alternatives.length && !r.purchased) {
    const alts = el('div', { style: 'margin-top:16px' }, el('div', { class: 'section-title' }, 'Альтернативы'));
    for (const a of r.alternatives)
      alts.append(el('div', { class: 'alt' }, el('span', {}, modeIc(a.mode)), el('span', {}, a.mode_human, ' '), el('span', { class: 'num' }, a.number || ''),
        a.night_before ? el('span', { class: 'tag night' }, 'ноч') : null,
        el('span', { class: 'faint' }, money(a.price)), el('span', { class: 'arr' }, fmtTime(a.arrival_at)),
        el('button', { class: 'btn ghost sm', title: 'закрепить этот вариант за гостем',
          onclick: (e) => { e.stopPropagation(); chooseOption(gid, a.key); } }, 'закрепить')));
    card.append(alts);
  }
  if (r.hotels && r.hotels.length) card.append(hotelsBlock(r.hotels));
  if (r.decisions && r.decisions.length) {
    const log = el('div', { style: 'margin-top:16px' }, el('div', { class: 'section-title' }, 'Лог решений — прозрачность'));
    const l = el('div', { class: 'log' });
    for (const d of r.decisions)
      l.append(el('div', { class: 'log-entry' }, el('span', { class: 'kind ' + d.kind }, d.kind), el('span', { class: 'd' }, d.detail)));
    log.append(l); card.append(log);
  }
  box.append(card);
  box.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

// hotelsBlock renders lodging offers (whole-stay prices) for a «ночлег» guest.
function hotelsBlock(hotels) {
  const box = el('div', { style: 'margin-top:16px' }, el('div', { class: 'section-title' }, 'Ночлег у места события'));
  for (const h of hotels) {
    box.append(el('div', { class: 'hotel' },
      el('div', { class: 'hn' },
        el('b', {}, h.name), h.stars ? el('span', { class: 'faint' }, ' ' + ''.repeat(h.stars)) : null,
        el('div', { class: 'faint', style: 'font-size:12px' },
          (h.rating ? `${h.rating}/10 · ${h.review_count} отз. · ` : '') + (h.address || '') + (h.free_cancellation ? ' · беспл. отмена' : '')),
      ),
      el('div', { style: 'flex:1' }),
      el('div', { class: 'mono', style: 'text-align:right' },
        el('b', {}, money(h.price)), el('div', { class: 'faint', style: 'font-size:11px' }, 'за весь стей')),
      h.checkout_url ? el('a', { class: 'btn ghost sm', href: h.checkout_url, target: '_blank', rel: 'noopener',
        onclick: (e) => e.stopPropagation() }, 'отель ↗') : null,
    ));
  }
  return box;
}

// copyInvite composes a forward-ready message for one guest.
function copyInvite(r) {
  const ev = BC.event;
  const link = location.origin + '/#/g/' + BC.id + '/' + r.guest_id;
  const lines = [];
  lines.push(`${r.guest_name}, встречаемся: ${ev.name || 'событие'} — ${ev.destination}, ${fmtDay(ev.date)}, сбор к ${ev.deadline}.`);
  if (r.human_card) lines.push(r.human_card);
  else if (r.chosen) lines.push(`Маршрут: ${r.chosen.mode_human} ${r.chosen.number || ''}, прибытие ${fmtTime(r.chosen.arrival_at)}, ${money(r.chosen.price)}.`);
  lines.push(`Твоя карточка (маршрут, билет, альтернативы): ${link}`);
  copyText(lines.join('\n'), 'Приглашение скопировано — перешлите гостю');
}

async function chooseOption(gid, key) {
  try {
    await api.post('/api/events/' + BC.id + '/guest/' + gid + '/choose', { key });
    toast('Вариант закреплён за гостем');
  } catch (e) { toast(e.message); }
}

function routeFacts(c) {
  const facts = el('div', { class: 'facts' });
  facts.append(
    fact('Отправление', fmtTime(c.departure_at) + ' · ' + fmtDay(c.departure_at)),
    fact('Прибытие', fmtTime(c.arrival_at) + ' · ' + fmtDay(c.arrival_at)),
    fact('Запас до сбора', c.margin_min != null ? fmtDur(c.margin_min) || (c.margin_min + ' мин') : '—'),
    fact('Транспорт', modeIc(c.mode) + ' ' + c.mode_human + (c.number ? ' ' + c.number : '')),
    fact('Пересадки', c.transfers ? c.transfers + (c.via ? ' · ' + c.via : '') : 'прямой'),
    fact('Цена', money(c.price)),
  );
  return facts;
}
function fact(k, v) { return el('div', { class: 'fact' }, el('div', { class: 'k' }, k), el('div', { class: 'v' }, v)); }

// ---------- wave ----------
function renderWave(board) {
  const box = $('#b-wave'); if (!box) return; clear(box);
  const arr = board.rows.filter(r => r.chosen && r.chosen.arrival_at).map(r => ({ r, t: new Date(r.chosen.arrival_at) }));
  arr.sort((a, b) => a.t - b.t);
  $('#wave-k').textContent = arr.length ? `${arr.length} прибыт.` : '';
  if (!arr.length) { box.append(el('div', { class: 'faint', style: 'font-size:13px' }, 'ждём первые маршруты…')); return; }
  let prev = null;
  const spacing = BC.event.spacing_min || 20;
  for (const { r, t } of arr) {
    const gapMin = prev ? Math.round((t - prev) / 60000) : null;
    const tight = gapMin != null && gapMin < spacing;
    box.append(el('div', { class: 'wave-item' },
      el('div', { class: 't' }, fmtTime(r.chosen.arrival_at)),
      el('div', { class: 'bar' }, el('div', { class: 'nm' }, r.guest_name), el('div', { class: 'dt' }, shortCity(r.chosen.to_station) + ' · ' + modeIc(r.chosen.mode) + ' ' + (r.chosen.number || ''))),
      el('div', { class: 'gap' + (tight ? ' tight' : '') }, gapMin != null ? '+' + gapMin + ' мин' : 'первый'),
    ));
    prev = t;
  }
}

// ---------- companions ----------
function renderCompanions(board) {
  const card = $('#comp-card'), box = $('#b-comp'); if (!box) return; clear(box);
  const comps = board.companions || [];
  if (!comps.length) { card.style.display = 'none'; return; }
  card.style.display = '';
  for (const cp of comps)
    box.append(el('div', { class: 'companion' }, el('span', { class: 'em' }, ''), cp.note,
      cp.seat_hint ? el('div', { class: 'seat-hint' }, '' + cp.seat_hint) : null,
      cp.mutual_consent ? el('div', { class: 'faint', style: 'font-size:11px;margin-top:3px' }, 'имена открыты обоим (взаимное согласие)') : null));
}

// ---------- bets ----------
function renderBets(board) {
  const card = $('#bets-card'), box = $('#b-bets'); if (!box) return; clear(box);
  const bets = board.bets || [];
  if (!bets.length) { card.style.display = 'none'; return; }
  card.style.display = '';
  for (const b of bets) {
    const pct = Math.round(b.late_chance * 100);
    const col = pct > 50 ? 'var(--red)' : pct > 25 ? 'var(--amber)' : 'var(--ok)';
    box.append(el('div', { class: 'bet-item', title: b.rationale },
      el('div', { class: 'nm' }, b.guest_name),
      el('div', { class: 'track' }, el('div', { class: 'fill', style: `width:${pct}%;background:${col}` })),
      el('div', { class: 'pct' }, pct + '%')));
  }
}

// ---------- countdown ----------
function updateCountdown() {
  const cd = $('#b-cd'); if (!cd || !BC.board) return;
  const g = BC.board.gather_at ? new Date(BC.board.gather_at) : null;
  if (!g || isNaN(g)) { cd.textContent = '—'; return; }
  let ms = g - new Date();
  if (ms <= 0) { cd.textContent = 'в сборе'; cd.style.color = 'var(--ok)'; return; }
  const d = Math.floor(ms / 86400000); ms -= d * 86400000;
  const h = Math.floor(ms / 3600000); ms -= h * 3600000;
  const m = Math.floor(ms / 60000); ms -= m * 60000;
  const s = Math.floor(ms / 1000);
  const pad = (n) => String(n).padStart(2, '0');
  cd.textContent = (d > 0 ? d + 'д ' : '') + `${pad(h)}:${pad(m)}:${pad(s)}`;
}

// ---------- actions ----------
async function recheck() {
  const b = $('#btn-recheck'); if (b) { b.disabled = true; b.textContent = '⟳ считаю…'; }
  try { await api.post('/api/events/' + BC.id + '/recheck'); toast('Перепроверяю живые цены…'); }
  catch (e) { toast('Ошибка: ' + e.message); }
  setTimeout(() => { if (b) { b.disabled = false; b.textContent = '⟳ перепроверить'; } }, 4000);
}
async function demoCollapse() {
  try { const res = await api.post('/api/events/' + BC.id + '/demo/collapse', {}); toast('Маршрут «' + res.guest + '» рассыпался → пересобираю'); }
  catch (e) { toast(e.message); }
}

// ================= MAP =================
function initMap() {
  const c = $('#map'); if (!c) return;
  if (!CFG.mapbox_token) { c.innerHTML = '<div class="empty">карта недоступна (нет mapbox токена)</div>'; return; }
  if (typeof mapboxgl === 'undefined' || !mapboxgl.supported || !mapboxgl.supported()) {
    c.innerHTML = '<div class="empty">карта недоступна в этом браузере (нет WebGL)</div>'; return;
  }
  let map;
  try {
    mapboxgl.accessToken = CFG.mapbox_token;
    map = new mapboxgl.Map({
      container: c, style: 'mapbox://styles/mapbox/light-v11',
      center: [50, 56], zoom: 3.1, attributionControl: false,
    });
  } catch (e) {
    c.innerHTML = '<div class="empty">карта недоступна: ' + e.message + '</div>'; return;
  }
  BC.map = map;
  map.on('error', () => {});
  map.on('load', () => {
    BC.mapReady = true;
    map.addSource('lines', { type: 'geojson', data: fc([]) });
    map.addLayer({ id: 'lines', type: 'line', source: 'lines', paint: { 'line-color': ['get', 'color'], 'line-width': 2, 'line-opacity': 0.7 } });
    map.addSource('cities', { type: 'geojson', data: fc([]) });
    map.addLayer({ id: 'cities', type: 'circle', source: 'cities', paint: { 'circle-radius': ['get', 'r'], 'circle-color': ['get', 'color'], 'circle-stroke-width': 1.5, 'circle-stroke-color': '#FFFFFF' } });
    if (BC.board) updateMap(BC.board);
  });
}

const STATUS_COLOR = { assembled: '#2E9E62', purchased: '#2E9E62', risk: '#F2762E', waiting: '#A29480', reassembled: '#F5A83B', needs_help: '#F9363C', planning: '#A29480' };
function fc(features) { return { type: 'FeatureCollection', features }; }

function updateMap(board) {
  if (!BC.map || !BC.mapReady) return;
  const dest = board.dest_coord;
  const lines = [], cities = [];
  if (dest) cities.push({ type: 'Feature', properties: { color: '#F9363C', r: 9 }, geometry: { type: 'Point', coordinates: [dest.lon, dest.lat] } });
  for (const r of board.rows) {
    if (!r.coord) continue;
    const col = STATUS_COLOR[r.status] || '#A29480';
    cities.push({ type: 'Feature', properties: { color: col, r: 6 }, geometry: { type: 'Point', coordinates: [r.coord.lon, r.coord.lat] } });
    if (dest) lines.push({ type: 'Feature', properties: { color: col }, geometry: { type: 'LineString', coordinates: [[r.coord.lon, r.coord.lat], [dest.lon, dest.lat]] } });
  }
  BC.map.getSource('lines').setData(fc(lines));
  BC.map.getSource('cities').setData(fc(cities));
  // fit bounds once
  if (!BC._fitted && (cities.length > 1)) {
    const b = new mapboxgl.LngLatBounds();
    cities.forEach(f => b.extend(f.geometry.coordinates));
    try { BC.map.fitBounds(b, { padding: 50, maxZoom: 6, duration: 800 }); BC._fitted = true; } catch {}
  }
}

// ================= GUEST CARD (shareable) =================
function GuestCard(id, gid) {
  teardownBoard();
  const root = app(); clear(root);
  root.append(el('div', { class: 'empty' }, el('span', { class: 'spinner' }), ' открываю карточку…'));
  loadGuestCard(id, gid);
}

async function loadGuestCard(id, gid) {
  const root = app();
  const reload = () => loadGuestCard(id, gid);
  try {
    const data = await api.get('/api/events/' + id + '/guest/' + gid);
    clear(root);
    const { event, row, companions, guest } = data;
    const c = row.chosen;
    const midClass = c ? (c.mode === 'railway' ? 'rail' : c.mode === 'bus' ? 'bus' : '') : '';
    const purchased = !!(guest && guest.purchased);

    const body = el('div', { class: 'body' },
      purchased ? el('div', { class: 'purchased-note' }, 'Билет куплен — маршрут закреплён, табло больше его не трогает.') : null,
      row.human_card ? el('div', { class: 'human' }, row.human_card) : null,
      c ? el('div', { class: 'route-line' },
        el('div', { class: 'end' }, el('div', { class: 'time' }, fmtTime(c.departure_at)), el('div', { class: 'place' }, shortCity(c.from_station))),
        el('div', { class: 'mid ' + midClass }, el('div', { class: 'dur' }, fmtDur(c.duration_min)), el('div', { class: 'track' }), el('div', { class: 'dur' }, c.number || '')),
        el('div', { class: 'end' }, el('div', { class: 'time' }, fmtTime(c.arrival_at)), el('div', { class: 'place' }, shortCity(c.to_station))),
      ) : el('div', { class: 'empty' }, 'Маршрут ещё собирается — обновите позже.'),
      c ? routeFacts(c) : null,
    );

    // Companions + consent (privacy: names unlock only mutually).
    if (companions && companions.length) {
      const compBox = el('div', { style: 'margin-top:8px' },
        ...companions.map(cp => el('div', { class: 'companion' }, '' + cp.note)));
      if (guest && !guest.companion_consent) {
        compBox.append(el('button', { class: 'btn ghost sm', style: 'margin-top:4px', onclick: async () => {
          try { await api.post(`/api/events/${id}/guest/${gid}/consent`, { consent: true }); toast('Согласие записано'); reload(); }
          catch (e) { toast(e.message); }
        } }, 'показать моё имя попутчику'));
      }
      body.append(compBox);
    }

    // Boarding pass — поездка как посадочный талон, QR ведёт на чекаут.
    if (c) {
      body.append(el('button', { class: 'btn ghost sm', style: 'display:block;margin:0 auto 6px', onclick: () => {
        passModal([boardingPass({
          from: shortCity(c.from_station), to: shortCity(c.to_station),
          mode: c.mode, number: c.number,
          dep: fmtTime(c.departure_at), arr: fmtTime(c.arrival_at), date: fmtDay(c.departure_at),
          price: c.price, checkout_url: c.checkout_url, passenger: row.guest_name,
        })], '' + row.guest_name + ' → ' + event.destination);
      } }, 'посадочный талон'));
    }

    // Checkout — the guest's own «дожать».
    if (c && c.checkout_url && !purchased)
      body.append(el('a', { class: 'btn primary dogzhat', href: c.checkout_url, target: '_blank', rel: 'noopener' }, 'Дожать на Туту — ' + money(c.price)));
    if (c && c.complex && c.leg_links && c.leg_links.length > 1) body.append(legLinks(c));

    // «Я купил» — freezes the row on the live board.
    if (c) {
      body.append(el('button', {
        class: 'btn ' + (purchased ? 'ghost' : '') + ' sm', style: 'display:block;margin:14px auto 0',
        onclick: async () => {
          try { await api.post(`/api/events/${id}/guest/${gid}/purchased`, { purchased: !purchased }); reload(); }
          catch (e) { toast(e.message); }
        },
      }, purchased ? 'снять отметку о покупке' : 'я купил билет'));
    }

    // Alternatives the guest can pick themselves.
    if (!purchased && row.alternatives && row.alternatives.length) {
      const alts = el('div', { style: 'margin-top:20px' }, el('div', { class: 'section-title' }, 'Не подходит? Выберите сами'));
      for (const a of row.alternatives) {
        alts.append(el('div', { class: 'alt' },
          el('span', {}, modeIc(a.mode)), el('span', {}, a.mode_human, ' '), el('span', { class: 'num' }, a.number || ''),
          a.night_before ? el('span', { class: 'tag night' }, 'ноч') : null,
          el('span', { class: 'faint' }, money(a.price)), el('span', { class: 'arr' }, fmtTime(a.arrival_at)),
          el('button', { class: 'btn ghost sm', onclick: async () => {
            try { await api.post(`/api/events/${id}/guest/${gid}/choose`, { key: a.key }); toast('Ваш выбор закреплён'); reload(); }
            catch (e) { toast(e.message); }
          } }, 'поеду этим'),
        ));
      }
      body.append(alts);
    }

    if (row.hotels && row.hotels.length) body.append(hotelsBlock(row.hotels));

    root.append(el('div', { class: 'guest-detail' },
      el('div', { class: 'ticket' },
        el('div', { class: 'head' },
          el('div', { class: 'ev' }, 'УЛЫБКА · ' + (event.name || 'Событие')),
          el('h1', {}, row.guest_name + ' → ' + event.destination),
          el('div', { class: 'faint mono', style: 'font-size:13px;margin-top:6px' },
            fmtDay(event.date) + ' · сбор в ' + event.deadline,
            ' · ', el('span', { class: 'badge ' + row.status }, STATUS_RU[row.status] || row.status)),
        ),
        body,
      ),
      el('div', { style: 'text-align:center;margin-top:16px' },
        el('button', { class: 'btn ghost sm', onclick: () => Router.go('/e/' + id) }, '← к табло события')),
    ));
  } catch (e) {
    clear(root); root.append(el('div', { class: 'empty' }, 'Карточка недоступна: ' + e.message,
      el('div', { style: 'margin-top:12px' }, el('button', { class: 'btn ghost sm', onclick: reload }, 'повторить'))));
  }
}

function legLinks(c) {
  const box = el('div', { style: 'margin-top:14px' }, el('div', { class: 'section-title' }, 'Сегменты маршрута (билеты отдельно)'));
  c.leg_links.forEach((u, i) => box.append(el('a', { class: 'btn ghost sm', style: 'margin:4px 6px 0 0', href: u, target: '_blank', rel: 'noopener' }, `сегмент ${i + 1} →`)));
  return box;
}

// kick off the app now that Board & GuestCard exist
boot();
