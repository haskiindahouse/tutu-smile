/* «Улыбка» — фронтенд одностраничника.
   Живое табло на SSE, конструктор события, карта прибытий, карточка гостя. */

'use strict';

// ---------------- config & api ----------------
const CFG = { mapbox_token: '', llm_enabled: false, recheck_sec: 45 };

const api = {
  async get(p) { const r = await fetch(p); if (!r.ok) throw new Error((await r.json()).error || r.statusText); return r.json(); },
  async post(p, body) {
    const r = await fetch(p, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
    if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error; } catch {} throw new Error(m); }
    return r.json();
  },
};

// ---------------- helpers ----------------
const el = (tag, attrs = {}, ...kids) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = v;
    else if (k === 'html') e.innerHTML = v;
    else if (k.startsWith('on') && typeof v === 'function') e.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined && v !== false) e.setAttribute(k, v);
  }
  for (const kid of kids.flat()) { if (kid == null) continue; e.append(kid.nodeType ? kid : document.createTextNode(kid)); }
  return e;
};
const $ = (s, r = document) => r.querySelector(s);
const clear = (n) => { while (n.firstChild) n.removeChild(n.firstChild); };

const MODE_IC = { avia: '', railway: '', bus: '', etrain: '' };
const modeIc = () => '';

function fmtTime(iso) { if (!iso) return '—'; const d = new Date(iso); return isNaN(d) ? '—' : d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', timeZone: 'Europe/Moscow' }); }
function fmtDay(iso) { if (!iso) return ''; const d = new Date(iso); return isNaN(d) ? '' : d.toLocaleDateString('ru-RU', { day: '2-digit', month: 'short', timeZone: 'Europe/Moscow' }); }
function money(n) { return (n || 0).toLocaleString('ru-RU', { maximumFractionDigits: 0 }) + ' ₽'; }
function fmtDur(min) { if (!min) return ''; const h = Math.floor(min / 60), m = min % 60; return (h ? h + ' ч ' : '') + (m ? m + ' мин' : ''); }
const STATUS_RU = { assembled: 'собран', risk: 'риск', waiting: 'ждёт решения', reassembled: 'пересобран', needs_help: 'нужна помощь', planning: 'считаю…', purchased: 'куплен ' };

function toast(msg) {
  const t = el('div', { class: 'toast' }, msg);
  document.body.append(t);
  setTimeout(() => t.remove(), 3200);
}

async function copyText(text, okMsg) {
  try { await navigator.clipboard.writeText(text); toast(okMsg || 'Скопировано'); }
  catch {
    const ta = el('textarea', { style: 'position:fixed;opacity:0' }, text);
    document.body.append(ta); ta.select();
    try { document.execCommand('copy'); toast(okMsg || 'Скопировано'); } catch { toast('Не удалось скопировать'); }
    ta.remove();
  }
}

// ---------------- voice input (Web Speech API) ----------------
// Returns a mic button that dictates into `apply(text)`, or null when the
// browser has no speech recognition (Chrome/Edge/Safari have it for ru-RU).
function micButton(getText, apply) {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) return null;
  let rec = null, base = '';
  const btn = el('button', { class: 'btn ghost mic', title: 'надиктовать голосом' }, 'надиктовать');
  const stop = () => { if (rec) { try { rec.stop(); } catch {} rec = null; } btn.classList.remove('rec'); btn.textContent = 'надиктовать'; };
  btn.addEventListener('click', () => {
    if (rec) { stop(); return; }
    rec = new SR();
    rec.lang = 'ru-RU'; rec.continuous = true; rec.interimResults = true;
    base = getText();
    if (base && !/[\s.,;]$/.test(base)) base += '. ';
    rec.onresult = (e) => {
      let final = '', interim = '';
      for (const r of e.results) (r.isFinal ? (final += r[0].transcript) : (interim += r[0].transcript));
      apply((base + final + interim).trimStart());
      if (final) base = (base + final).replace(/\s+$/, '') + ' ';
    };
    rec.onerror = (e) => {
      const msg = { 'not-allowed': 'Разрешите доступ к микрофону', 'network': 'В этом браузере голосовой ввод недоступен — набери текстом', 'audio-capture': 'Микрофон не найден' }[e.error];
      toast(msg || ('Микрофон: ' + e.error)); stop();
    };
    rec.onend = () => stop();
    try { rec.start(); btn.classList.add('rec'); btn.textContent = '■ слушаю…'; } catch { stop(); }
  });
  return btn;
}

// ---------------- smart intake: text/voice/chat → draft ----------------
const DRAFT_KEY = 'smile_draft';

async function parseIntake(text, draft) {
  return api.post('/api/parse', { text, draft: draft || null });
}

function stashDraft(draft) { sessionStorage.setItem(DRAFT_KEY, JSON.stringify(draft)); }
function takeDraft() {
  const raw = sessionStorage.getItem(DRAFT_KEY);
  if (!raw) return null;
  sessionStorage.removeItem(DRAFT_KEY);
  try { return JSON.parse(raw); } catch { return null; }
}

// draftComplete: enough understood to go straight to the live board.
function draftComplete(d) {
  return d && (d.destination || d.vibe) && d.date && d.guests && d.guests.some(g => (g.city || '').trim());
}

// createFromDraft builds the event straight from a parsed draft — the fast
// path: one field → live board, no constructor.
async function createFromDraft(d) {
  const body = {
    name: d.name || 'Событие', input_mode: 'place', destination: d.destination || '',
    vibe: d.vibe || '', date: d.date, deadline: d.deadline || '15:00',
    buffer_hours: d.buffer_hours || 2, spacing_min: d.spacing_min || 20,
    budget_per_person: d.budget_per_person || 0, totalizator: true,
    guests: (d.guests || []).filter(g => (g.city || '').trim()).map(g => ({
      name: g.name || 'Гость', city: g.city, profile: g.profile === 'faster' ? 'faster' : 'cheaper',
      adults: g.adults || 1, children: g.children || 0,
      needs_lodging: !!g.needs_lodging, find_companions: !!g.find_companions,
    })),
  };
  const { id } = await api.post('/api/events', body);
  return id;
}

// quickParseGuest: instant client-side guest from one line — no LLM, no wait.
// «Паша из Перми, быстрее, с ночёвкой» / «тётя Лена с мужем из Нижнего» / «Пермь».
function quickParseGuest(s) {
  s = (s || '').trim();
  if (!s) return null;
  const g = {
    name: '', city: '', profile: /быстрее|летит|самол|спешит/i.test(s) ? 'faster' : 'cheaper',
    adults: /вдво[её]м|с\s+(жен|муж)/i.test(s) ? 2 : 1, children: 0,
    needs_lodging: /ночлег|ноч[её]вк|гостиниц|отел/i.test(s),
    find_companions: /попутчик|вместе/i.test(s),
  };
  const mAd = s.match(/(\d+)\s*(взросл|чел)/i); if (mAd) g.adults = +mAd[1];
  const mCh = s.match(/(\d+)\s*(дет|реб)/i); if (mCh) g.children = +mCh[1];
  const m = s.match(/^(.+?)\s+из\s+([А-Яа-яЁё\- ]+)/);
  if (m) { g.name = m[1].replace(/,.*$/, '').trim(); g.city = m[2].trim(); }
  else g.city = s.split(/[,.;]/)[0].trim();
  g.city = normalizeCityInput(g.city);
  if (!g.city) return null;
  return g;
}

// normalizeCityInput snaps free typing onto a known city (case-insensitive),
// otherwise returns the trimmed input as-is.
function normalizeCityInput(city) {
  const c = (city || '').trim();
  if (!c || !CFG.cities) return c;
  const low = c.toLowerCase();
  for (const known of CFG.cities) if (known.toLowerCase() === low) return known;
  return c;
}

// ---------------- router ----------------
const Router = {
  routes: [],
  add(re, fn) { this.routes.push([re, fn]); },
  go(path) { location.hash = '#' + path; },
  resolve() {
    const path = (location.hash || '#/').slice(1);
    for (const [re, fn] of this.routes) {
      const m = path.match(re);
      if (m) { fn(...m.slice(1)); return; }
    }
    Home();
  },
};
window.addEventListener('hashchange', () => Router.resolve());

const app = () => $('#app');

// ================= HOME =================
function Home() {
  const root = app(); clear(root);

  // Smart intake: one field — type, dictate, or paste the group chat.
  // The draft parses live while you type; when it's complete, one click goes
  // straight to the live board (the constructor is only for fine-tuning).
  let intakeText = '', liveDraft = null, parseSeq = 0, debounceT = null;
  const preview = el('div', { class: 'intake-preview' });

  function renderPreview(d) {
    clear(preview);
    if (!d) return;
    const chips = [];
    const chip = (ic, txt, cls) => chips.push(el('span', { class: 'chip ' + (cls || '') }, ic + ' ' + txt));
    if (d.destination) chip('', d.destination);
    else if (d.vibe) chip('', d.vibe.slice(0, 40));
    if (d.date) chip('', fmtDay(d.date + 'T12:00:00') || d.date);
    if (d.deadline) chip('', 'сбор ' + d.deadline);
    if (d.budget_per_person) chip('', d.budget_per_person + '₽/чел');
    for (const g of (d.guests || []).slice(0, 12)) {
      if (!(g.city || '').trim()) continue;
      let t = (g.name || 'Гость') + ' · ' + g.city;
      if (g.profile === 'faster') t += ' '; if (g.needs_lodging) t += ' '; if (g.find_companions) t += ' ';
      chip('', t, 'guest');
    }
    if (d.missing && d.missing.length) chip('', 'не хватает: ' + d.missing.join(', '), 'missing');
    if (chips.length) preview.append(el('div', { class: 'chip-row' }, ...chips));
  }

  function scheduleParse() {
    if (!CFG.llm_enabled) return;
    clearTimeout(debounceT);
    if (intakeText.trim().length < 15) { liveDraft = null; renderPreview(null); return; }
    debounceT = setTimeout(async () => {
      const seq = ++parseSeq;
      try {
        const { draft } = await parseIntake(intakeText);
        if (seq !== parseSeq) return; // a newer parse superseded this one
        liveDraft = draft; renderPreview(draft);
        goBtn.textContent = draftComplete(draft) ? 'Сразу на табло' : 'Собрать событие';
      } catch { /* preview is best-effort */ }
    }, 1200);
  }

  const ta = el('textarea', {
    class: 'intake-ta', rows: '4',
    placeholder: 'Например: «Свадьба в Казани 5 сентября, сбор в 15:00. Бабушка Нина из Москвы подешевле и с ночёвкой, Артём из Питера побыстрее, тётя Лена с мужем из Нижнего, дядя Витя из Калуги, Паша летит из Перми»\n…или вставьте переписку из чата — гостей вытащим сами.',
    oninput: (e) => { intakeText = e.target.value; scheduleParse(); },
  });
  const goBtn = el('button', { class: 'btn primary', onclick: runIntake }, 'Собрать событие');
  async function runIntake() {
    if (!intakeText.trim()) { toast('Опишите событие — или нажмите и расскажите'); return; }
    goBtn.disabled = true; goBtn.textContent = 'разбираю…';
    try {
      let draft = liveDraft;
      if (!draft) ({ draft } = await parseIntake(intakeText));
      if (draftComplete(draft) && draft.destination) {
        // Fast path: everything understood — straight to the live board.
        const id = await createFromDraft(draft);
        toast('Событие собрано — считаю маршруты');
        Router.go('/e/' + id);
        return;
      }
      stashDraft(draft);
      Router.go('/new' + (!draft.destination && draft.vibe ? '?vibe=1' : ''));
    } catch (e) { toast(e.message); }
    goBtn.disabled = false; goBtn.textContent = 'Собрать событие';
  }
  const mic = micButton(() => intakeText, (t) => { intakeText = t; ta.value = t; scheduleParse(); });

  root.append(
    el('div', { class: 'hero' },
      el('h1', {}, 'Собрать всех — ', el('span', { class: 'accent' }, 'к одному часу')),
      el('p', { class: 'lead' }, 'Гости приезжают из разных городов, а встреча одна. «Улыбка» строит каждому маршрут от дедлайна назад, держит живое табло прибытий и выдаёт ссылки «дожать» на чекаут Туту. Без аккаунтов — карточка это ссылка.'),
      el('div', { class: 'intake' },
        ta,
        preview,
        el('div', { class: 'intake-row' },
          mic,
          el('span', { class: 'faint', style: 'font-size:12px' },
            CFG.llm_enabled ? (mic ? 'расскажите голосом, напечатайте или вставьте чат — разбор идёт по мере набора' : 'напечатайте или вставьте переписку — разбор идёт по мере набора')
                            : 'умный ввод требует ключ OpenRouter — или заполните форму вручную'),
          el('div', { style: 'flex:1' }),
          goBtn,
        ),
      ),
      el('div', { class: 'cta-row', style: 'margin-top:18px' },
        el('button', { class: 'btn ghost', onclick: () => Router.go('/spin') }, 'Рулетка: куда ехать'),
        el('button', { class: 'btn ghost', onclick: () => Router.go('/meet') }, 'Увидеться вдвоём'),
        el('button', { class: 'btn ghost', onclick: () => Router.go('/new') }, 'Заполнить вручную'),
        el('button', { class: 'btn ghost', onclick: () => Router.go('/new?vibe=1') }, 'Вайб'),
        el('button', { class: 'btn ghost', onclick: () => seedDemo() }, 'Демо'),
      ),
    ),
    recentEvents(),
  );
}

function recentEvents() {
  const box = el('div', { class: 'recent' }, el('h4', {}, 'Недавние события'));
  api.get('/api/events').then(({ events }) => {
    if (!events || !events.length) { box.append(el('div', { class: 'empty' }, 'пока пусто — создайте первое событие')); return; }
    events.sort((a, b) => (b.created_at || '').localeCompare(a.created_at || ''));
    for (const ev of events) {
      box.append(el('div', { class: 'recent-item', onclick: () => Router.go('/e/' + ev.id) },
        el('span', { class: 'mono', style: 'color:var(--red);font-weight:600' }, ':)'),
        el('div', {}, el('div', {}, el('b', {}, ev.name || 'Событие'), ' → ', ev.destination),
          el('div', { class: 'faint mono', style: 'font-size:12px' }, `${fmtDay(ev.date)} · сбор ${ev.deadline} · ${ev.guests.length} гост.`)),
        el('div', { class: 'spacer', style: 'flex:1' }),
        el('span', { class: 'faint mono', style: 'font-size:12px' }, ev.id),
      ));
    }
  }).catch(() => {});
  return box;
}

async function seedDemo() {
  const ev = {
    name: 'Свадьба Оли и Миши', input_mode: 'place', destination: 'Казань',
    date: '2026-09-05', deadline: '15:00', buffer_hours: 2, spacing_min: 20,
    budget_per_person: 9000, totalizator: true,
    guests: [
      { name: 'Бабушка Нина', city: 'Москва', profile: 'cheaper', adults: 1, needs_lodging: true, find_companions: true },
      { name: 'Сергей', city: 'Москва', profile: 'cheaper', adults: 1, find_companions: true },
      { name: 'Друг Артём', city: 'Санкт-Петербург', profile: 'faster', adults: 1 },
      { name: 'Тётя Лена', city: 'Нижний Новгород', profile: 'cheaper', adults: 2 },
      { name: 'Марина', city: 'Чебоксары', profile: 'cheaper', adults: 1 },
      { name: 'Кузен Паша', city: 'Пермь', profile: 'faster', adults: 1 },
      { name: 'Дядя Витя', city: 'Калуга', profile: 'cheaper', adults: 1 },
    ],
  };
  try { const { id } = await api.post('/api/events', ev); Router.go('/e/' + id); }
  catch (e) { toast('Ошибка: ' + e.message); }
}

// ================= CONSTRUCTOR =================
function applyDraft(state, draft) {
  if (!draft) return;
  if (draft.name) state.name = draft.name;
  if (draft.destination) { state.destination = draft.destination; state.vibe = false; }
  else if (draft.vibe) { state.vibe = true; state.vibeText = draft.vibe; }
  if (draft.date) state.date = draft.date;
  if (draft.deadline) state.deadline = draft.deadline;
  if (draft.buffer_hours) state.buffer = draft.buffer_hours;
  if (draft.spacing_min) state.spacing = draft.spacing_min;
  if (draft.budget_per_person) state.budget = draft.budget_per_person;
  if (draft.guests && draft.guests.length) {
    state.guests = draft.guests.map(g => ({
      name: g.name || '', city: g.city || '', profile: g.profile === 'faster' ? 'faster' : 'cheaper',
      adults: g.adults || 1, children: g.children || 0,
      needs_lodging: !!g.needs_lodging, find_companions: !!g.find_companions,
    }));
  }
  state.missing = draft.missing || [];
  state.parseNote = draft.note || '';
}

function stateToDraft(state) {
  return {
    name: state.name, destination: state.vibe ? '' : state.destination, vibe: state.vibeText,
    date: state.date, deadline: state.deadline, buffer_hours: state.buffer,
    spacing_min: state.spacing, budget_per_person: state.budget,
    guests: state.guests.filter(g => g.city.trim() || g.name.trim()),
  };
}

function Constructor() {
  const root = app(); clear(root);
  const vibeMode = new URLSearchParams(location.hash.split('?')[1] || '').get('vibe') === '1';
  const state = {
    vibe: vibeMode, name: '', destination: '', vibeText: '',
    date: '2026-09-05', deadline: '15:00', buffer: 2, spacing: 20, budget: 0, totalizator: true,
    guests: [ { name: '', city: '', profile: 'cheaper', adults: 1, children: 0, needs_lodging: false, find_companions: false } ],
    candidates: null, spec: null, missing: [], parseNote: '',
  };
  applyDraft(state, takeDraft());

  const render = () => {
    clear(root);
    // el() skips null kids; native append() would print «null».
    root.append(el('div', {},
      el('div', { style: 'display:flex;align-items:center;gap:12px;margin-bottom:20px' },
        el('button', { class: 'btn ghost sm', onclick: () => Router.go('/') }, '← назад'),
        el('h1', { style: 'margin:0;font-size:24px' }, state.vibe ? 'Событие по вайбу' : 'Новое событие'),
      ),
      state.parseNote || (state.missing && state.missing.length)
        ? el('div', { class: 'parse-note' },
            state.parseNote ? el('div', {}, '' + state.parseNote) : null,
            state.missing && state.missing.length ? el('div', { class: 'faint', style: 'margin-top:4px' }, 'Не хватает: ' + state.missing.join(', ')) : null)
        : null,
      amendBar(state, render),
      // frame card
      el('div', { class: 'card' },
        el('h2', {}, 'Рамки события'),
        el('div', { class: 'card-sub' }, 'Задайте место (или желание), дату и час сбора — остальное соберём.'),
        el('div', { class: 'field' },
          el('label', {}, 'Название'),
          el('input', { value: state.name, placeholder: 'Свадьба, турнир, встреча выпускников…', oninput: (e) => state.name = e.target.value }),
        ),
        state.vibe
          ? el('div', { class: 'field' },
              el('label', {}, 'Желание свободным текстом'),
              el('textarea', { placeholder: 'хотим к морю, недорого, чтобы все успели к вечеру', oninput: (e) => state.vibeText = e.target.value }, state.vibeText),
              el('div', { class: 'hint' }, CFG.llm_enabled ? 'ИИ развернёт это в города-кандидаты, живые цены их ранжируют.' : 'ИИ выключен — подберём кандидатов эвристикой.'),
            )
          : el('div', { class: 'field' },
              el('label', {}, 'Город события'),
              el('input', { value: state.destination, placeholder: 'Казань', oninput: (e) => state.destination = e.target.value }),
            ),
        el('div', { class: 'form-grid' },
          el('div', { class: 'field' }, el('label', {}, 'Дата'), el('input', { type: 'date', value: state.date, oninput: (e) => state.date = e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Час сбора'), el('input', { type: 'time', value: state.deadline, oninput: (e) => state.deadline = e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Буфер, ч'), el('input', { type: 'number', step: '0.5', min: '0', value: state.buffer, oninput: (e) => state.buffer = +e.target.value }), el('div', { class: 'hint' }, 'быть в городе за N часов до начала')),
          el('div', { class: 'field' }, el('label', {}, 'Зазор волны, мин'), el('input', { type: 'number', min: '0', value: state.spacing, oninput: (e) => state.spacing = +e.target.value }), el('div', { class: 'hint' }, 'встречающий один — минимум между прибытиями')),
          el('div', { class: 'field' }, el('label', {}, 'Бюджет на чел., ₽'), el('input', { type: 'number', min: '0', value: state.budget, placeholder: '0 — без бюджета', oninput: (e) => state.budget = +e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Тотализатор'),
            el('div', { class: 'chip-toggle' },
              el('button', { class: state.totalizator ? 'on' : '', onclick: () => { state.totalizator = true; render(); } }, 'вкл'),
              el('button', { class: !state.totalizator ? 'on' : '', onclick: () => { state.totalizator = false; render(); } }, 'выкл'),
            ), el('div', { class: 'hint' }, 'рофл-ставки «кто опоздает», честная вероятность')),
        ),
      ),
      // guests card
      guestsCard(state, render),
      // vibe candidates
      state.candidates ? vibeCandidatesCard(state, render) : null,
      // submit
      el('div', { style: 'display:flex;gap:12px;justify-content:flex-end;margin-top:8px' },
        state.vibe && !state.candidates
          ? el('button', { class: 'btn primary', onclick: () => runVibe(state, render) }, 'Подобрать города')
          : el('button', { class: 'btn primary', onclick: () => submitEvent(state) }, 'Собрать событие'),
      ),
    ));
  };
  render();
}

// amendBar lets the organizer keep talking to the form: «добавь Пашу из
// Перми», «бюджет 8000», «перенеси на субботу» — the parser merges the change
// into the current draft.
function amendBar(state, render) {
  if (!CFG.llm_enabled) return null;
  let text = '';
  const input = el('input', {
    placeholder: 'дополнить: «добавь Пашу из Перми, летит, ищет попутчиков»…',
    style: 'flex:1',
    oninput: (e) => text = e.target.value,
    onkeydown: (e) => { if (e.key === 'Enter') apply(); },
  });
  const btn = el('button', { class: 'btn sm', onclick: apply }, 'применить');
  async function apply() {
    if (!text.trim()) return;
    btn.disabled = true; btn.textContent = '…';
    try {
      const { draft } = await parseIntake(text, stateToDraft(state));
      applyDraft(state, draft);
      render();
      toast('Черновик обновлён');
    } catch (e) { toast(e.message); btn.disabled = false; btn.textContent = 'применить'; }
  }
  const mic = micButton(() => text, (t) => { text = t; input.value = t; });
  return el('div', { class: 'amend-bar' }, mic, input, btn);
}

function guestsCard(state, render) {
  const card = el('div', { class: 'card' },
    el('h2', {}, 'Гости'),
    el('div', { class: 'card-sub' }, 'Город выезда и профиль каждого. «Дешевле» → цена, «Быстрее» → время. Флаги — по желанию.'),
  );
  const header = el('div', { class: 'guest-row', style: 'color:var(--ink-faint);font-size:12px;font-family:var(--mono)' },
    el('div', {}, 'Имя'), el('div', {}, 'Город'), el('div', {}, 'Профиль'),
    el('div', {}, 'Ночлег'), el('div', {}, 'Попутчики'), el('div', {}, ''));
  card.append(header);
  // Quick-add: one line, instant, no LLM — «Паша из Перми, быстрее, с ночёвкой».
  let quick = '';
  const quickIn = el('input', {
    placeholder: 'быстрое добавление: «Паша из Перми, быстрее, с ночёвкой» — Enter', style: 'flex:1',
    oninput: (e) => quick = e.target.value,
    onkeydown: (e) => {
      if (e.key !== 'Enter') return;
      const g = quickParseGuest(quick);
      if (!g) { toast('Не понял — формат: «Имя из Город»'); return; }
      state.guests = state.guests.filter(x => x.city.trim() || x.name.trim());
      state.guests.push(g); quick = '';
      render(); toast('Гость добавлен: ' + (g.name || 'Гость') + ' · ' + g.city);
    },
  });
  card.append(el('div', { style: 'display:flex;gap:8px;margin-bottom:12px' }, quickIn));

  state.guests.forEach((g, i) => {
    card.append(el('div', { class: 'guest-row' },
      el('input', { value: g.name, placeholder: 'Имя', oninput: (e) => g.name = e.target.value }),
      el('input', { value: g.city, placeholder: 'Город', list: 'cities', onchange: (e) => g.city = normalizeCityInput(e.target.value), oninput: (e) => g.city = e.target.value }),
      el('select', { onchange: (e) => g.profile = e.target.value },
        el('option', { value: 'cheaper', selected: g.profile === 'cheaper' }, 'дешевле'),
        el('option', { value: 'faster', selected: g.profile === 'faster' }, 'быстрее')),
      el('div', { class: 'chip-toggle' },
        el('button', { class: g.needs_lodging ? 'on' : '', onclick: () => { g.needs_lodging = !g.needs_lodging; render(); } }, g.needs_lodging ? 'да' : 'нет')),
      el('div', { class: 'chip-toggle' },
        el('button', { class: g.find_companions ? 'on' : '', onclick: () => { g.find_companions = !g.find_companions; render(); } }, g.find_companions ? 'да' : 'нет')),
      el('button', { class: 'btn ghost sm danger', onclick: () => { state.guests.splice(i, 1); render(); } }, ''),
    ));
  });
  card.append(el('button', { class: 'btn ghost sm', style: 'margin-top:12px', onclick: () => { state.guests.push({ name: '', city: '', profile: 'cheaper', adults: 1, children: 0, needs_lodging: false, find_companions: false }); render(); } }, '＋ гость'));
  return card;
}

function vibeCandidatesCard(state, render) {
  const card = el('div', { class: 'card' },
    el('h2', {}, 'Города-кандидаты'),
    el('div', { class: 'card-sub' }, state.spec && state.spec.note ? state.spec.note : 'Ранжировано по полной цене сбора всех гостей к дедлайну.'),
  );
  state.candidates.forEach((c) => {
    const chosen = state.destination === c.city;
    card.append(el('div', { class: 'recent-item', style: chosen ? 'box-shadow:inset 0 0 0 2px var(--red)' : '', onclick: () => { state.destination = c.city; state.vibe = false; render(); } },
      el('div', { style: 'font-size:20px' }, chosen ? '' : ''),
      el('div', {}, el('b', {}, c.city),
        el('div', { class: 'faint mono', style: 'font-size:12px' }, `успевают ${c.reachable}/${state.guests.length}` + (c.max_arrival ? ` · последний в ${c.max_arrival}` : '') + (c.note ? ` · ${c.note}` : ''))),
      el('div', { style: 'flex:1' }),
      el('div', { class: 'mono', style: 'text-align:right' }, el('b', {}, c.total_price ? money(c.total_price) : '—'), el('div', { class: 'faint', style: 'font-size:11px' }, 'сбор всех')),
    ));
  });
  return card;
}

async function runVibe(state, render) {
  if (!state.vibeText.trim()) { toast('Опишите желание'); return; }
  if (!state.guests.some(g => g.city.trim())) { toast('Добавьте хотя бы одного гостя с городом'); return; }
  toast('Разворачиваю вайб и считаю живые цены…');
  try {
    const res = await api.post('/api/vibe', {
      vibe: state.vibeText, date: state.date, deadline: state.deadline, buffer_hours: state.buffer,
      guests: state.guests.filter(g => g.city.trim()),
    });
    state.candidates = res.cities; state.spec = res.spec; render();
  } catch (e) { toast('Ошибка вайба: ' + e.message); }
}

async function submitEvent(state) {
  if (!state.destination.trim()) { toast(state.vibe ? 'Выберите город из кандидатов' : 'Укажите город события'); return; }
  const guests = state.guests.filter(g => g.city.trim());
  if (!guests.length) { toast('Добавьте хотя бы одного гостя'); return; }
  const body = {
    name: state.name || 'Событие', input_mode: 'place', destination: state.destination,
    vibe: state.vibeText, date: state.date, deadline: state.deadline, buffer_hours: state.buffer,
    spacing_min: state.spacing, budget_per_person: state.budget, totalizator: state.totalizator, guests,
  };
  try { const { id } = await api.post('/api/events', body); Router.go('/e/' + id); }
  catch (e) { toast('Ошибка: ' + e.message); }
}

// ================= SPIN (рулетка: куда ехать) =================
// Лекарство от паралича выбора: интересы + бюджет + даты → живые полные цены
// поездок (туда+обратно+отель) → барабан крутится и выбрасывает город.
const INTERESTS = [
  ['fishing', 'рыбалка'], ['swim', 'купаться'], ['food', 'покушать'],
  ['nature', 'природа'], ['mountains', 'горы'], ['history', 'история'],
  ['party', 'тусовки'], ['chill', 'отдых'], ['spa', 'бани и спа'],
  ['insta', 'инста-туры'], ['exotic', 'экзотика'],
];
const SPIN_LINES = ['кручу живые цены…', 'торгуюсь с поездами…', 'спрашиваю отели…', 'сверяю бюджет…', 'подглядываю в расписания…'];

function interestTiles(sel, onchange) {
  return el('div', { class: 'tiles' }, ...INTERESTS.map(([k, label]) =>
    el('button', { class: 'tile' + (sel.has(k) ? ' on' : ''), onclick: () => { sel.has(k) ? sel.delete(k) : sel.add(k); onchange(); } }, label)));
}

function SpinPage() {
  const root = app(); clear(root);
  const state = {
    origin: '', date: defaultMeetDate(), days: 2, budget: 15000, scope: 'rf',
    interests: new Set(), exclude: new Set(), visaFree: false,
    pool: null, tried: new Set(), current: null,
    wheel: false, pricing: false, plan: null, planBusy: false, spots: null, spotsBusy: false,
  };

  const render = () => {
    clear(root);
    const scopeBtn = (v, label) => el('button', { class: state.scope === v ? 'on' : '', onclick: () => { state.scope = v; state.pool = null; render(); } }, label);
    const exBtn = (mode, label) => el('button', {
      class: 'tile small' + (state.exclude.has(mode) ? ' off' : ''),
      title: 'исключить ' + label,
      onclick: () => { state.exclude.has(mode) ? state.exclude.delete(mode) : state.exclude.add(mode); render(); },
    }, (state.exclude.has(mode) ? '' : '') + label);
    root.append(el('div', {},
      el('div', { style: 'display:flex;align-items:center;gap:12px;margin-bottom:20px' },
        el('button', { class: 'btn ghost sm', onclick: () => Router.go('/') }, '← назад'),
        el('h1', { style: 'margin:0;font-size:24px' }, 'Рулетка: куда ехать')),
      el('div', { class: 'card' },
        el('div', { class: 'card-sub', style: 'margin-bottom:12px' }, 'Сложно выбрать? Не выбирайте. Колесо падает на город сразу — живая поездка (туда, обратно, отель) собирается под выпавший.'),
        interestTiles(state.interests, () => { state.pool = null; render(); }),
        el('div', { class: 'form-grid', style: 'margin-top:16px' },
          el('div', { class: 'field' }, el('label', {}, 'Откуда'),
            el('input', { value: state.origin, placeholder: 'Москва', list: 'cities', oninput: (e) => state.origin = e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Когда'),
            el('input', { type: 'date', value: state.date, oninput: (e) => state.date = e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Дней'),
            el('input', { type: 'number', min: '1', max: '14', value: state.days, oninput: (e) => state.days = +e.target.value })),
          el('div', { class: 'field' }, el('label', {}, 'Бюджет всего, ₽'),
            el('input', { type: 'number', min: '0', step: '1000', value: state.budget, oninput: (e) => state.budget = +e.target.value }),
            el('div', { class: 'hint' }, 'дорога туда-обратно + отель на все ночи')),
          el('div', { class: 'field' }, el('label', {}, 'Куда смотрим'),
            el('div', { class: 'chip-toggle' }, scopeBtn('rf', 'РФ'), scopeBtn('abroad', 'заграница'), scopeBtn('any', 'всё равно')),
            state.scope !== 'rf' ? el('label', { class: 'visa-check' },
              el('input', { type: 'checkbox', checked: state.visaFree || null, onchange: (e) => { state.visaFree = e.target.checked; state.pool = null; } }),
              ' только безвиз для РФ') : null),
          el('div', { class: 'field' }, el('label', {}, 'Транспорт'),
            el('div', { class: 'tiles' }, exBtn('avia', 'самолёты'), exBtn('railway', 'поезда'), exBtn('bus', 'автобусы')),
            el('div', { class: 'hint' }, 'нажмите, чтобы исключить')),
        ),
        el('div', { style: 'display:flex;justify-content:center;margin-top:8px' },
          el('button', { class: 'btn primary spin-btn', disabled: state.wheel || state.pricing, onclick: spin },
            state.wheel ? 'кручу…' : state.pricing ? 'собираю поездку…' : 'КРУТИТЬ')),
      ),
      el('div', { id: 'slot-zone' }),
    ));
    renderSlot();
  }

  function renderSlot() {
    const zone = $('#slot-zone'); if (!zone) return; clear(zone);
    if (state.wheel) {
      zone.append(el('div', { class: 'slot' },
        el('div', { class: 'slot-reel', id: 'slot-reel' }, '…'),
        el('div', { class: 'slot-sub', id: 'slot-sub' }, 'колесо крутится…')));
      return;
    }
    if (state.pricing && state.current) {
      zone.append(el('div', { class: 'slot' },
        el('div', { class: 'slot-reel hit' }, state.current),
        el('div', { class: 'slot-sub', id: 'slot-sub' }, SPIN_LINES[0])));
      return;
    }
    if (state.current && typeof state.current === 'object') zone.append(resultCard(state));
  }

  // spin: колесо падает СРАЗУ (реальный рандом по всему пулу), и только
  // потом ищется живая поездка в выпавший город. Нет дороги — честный
  // автоперекрут на следующий.
  async function spin() {
    if (!state.origin.trim()) { toast('Откуда едем?'); return; }
    state.plan = null; state.spots = null;
    try {
      if (!state.pool) {
        const res = await api.post('/api/roulette/pool', {
          origin: normalizeCityInput(state.origin), interests: [...state.interests],
          scope: state.scope, visa_free: state.visaFree,
        });
        state.pool = res.cities; state.tried = new Set();
      }
    } catch (e) { toast(e.message); return; }

    let fresh = state.pool.filter(c => !state.tried.has(c));
    if (!fresh.length) { state.tried = new Set(); fresh = state.pool.slice(); }
    // Победитель + два запасных: сервер считает их параллельно и, если у
    // выпавшего нет живой дороги, колесо доворачивается — без второго круга.
    const shuffled = fresh.slice().sort(() => Math.random() - 0.5);
    const winner = shuffled[0];
    const backups = shuffled.slice(1, 3);
    state.tried.add(winner);
    await wheelTo(winner);
    await priceCity(winner, backups);
  }

  // wheelTo: ~2.5 секунды честной прокрутки по реальному пулу с замедлением.
  function wheelTo(winner) {
    return new Promise((resolve) => {
      state.wheel = true; state.current = null; render();
      const reel = $('#slot-reel');
      const names = state.pool;
      let i = Math.floor(Math.random() * names.length), delay = 60;
      const tick = () => {
        const r = $('#slot-reel'); if (!r) { state.wheel = false; resolve(); return; }
        r.textContent = names[i % names.length]; i++;
        if (delay < 380) { delay *= 1.14; setTimeout(tick, delay); }
        else {
          r.textContent = winner; r.classList.add('hit');
          setTimeout(() => { state.wheel = false; resolve(); }, 500);
        }
      };
      tick();
    });
  }

  async function priceCity(city, backups) {
    state.pricing = true; state.current = city; render();
    const lineT = setInterval(() => {
      const sub = $('#slot-sub'); if (sub) sub.textContent = SPIN_LINES[Math.floor(Math.random() * SPIN_LINES.length)];
    }, 1500);
    try {
      const res = await api.post('/api/roulette/price', {
        origin: normalizeCityInput(state.origin), city, backups: backups || [],
        date: state.date, days: state.days,
        budget: state.budget, exclude_modes: [...state.exclude],
      });
      clearInterval(lineT);
      if (res.landed && res.landed !== city) {
        // Доворот: короткая доп-прокрутка на живой запасной город.
        state.tried.add(res.landed);
        toast('' + (res.note || 'колесо довернулось') + ' → ' + res.landed);
        await wheelTo(res.landed);
      }
      state.pricing = false; state.current = res.option; render();
      smileRainSmall();
      loadSpots(); // лента впечатлений подъезжает сама
    } catch (e) {
      clearInterval(lineT);
      state.pricing = false; state.current = null; render();
      const untried = state.pool.filter(c => !state.tried.has(c));
      if (untried.length) { toast('' + e.message); spin(); }
      else { toast('Дорог не нашлось нигде из пула — смените дату или транспортные фильтры'); }
    }
  }

  function resultCard(state) {
    const o = state.current;
    const leg = (icon, title, l) => l ? el('div', { class: 'trip-leg' },
      el('span', {}, icon + ' ' + title + ': '),
      el('b', {}, l.mode_human + (l.number ? ' ' + l.number : '')),
      el('span', { class: 'faint' }, ' ' + fmtTime(l.departure_at) + ' → ' + fmtTime(l.arrival_at) + ' · ' + fmtDay(l.departure_at)),
      el('span', { class: 'mono', style: 'margin-left:auto' }, money(l.price)),
      l.checkout_url ? el('a', { class: 'btn ghost sm', href: l.checkout_url, target: '_blank', rel: 'noopener' }, 'билет ↗') : null,
    ) : el('div', { class: 'trip-leg faint' }, icon + ' ' + title + ': не нашлось');
    return el('div', { class: 'card slot-win' },
      el('div', { style: 'display:flex;align-items:baseline;gap:12px;flex-wrap:wrap' },
        el('h2', { style: 'font-size:30px;margin:0' }, '' + o.city),
        el('div', {},
          o.abroad ? el('span', { class: 'tag ' + (o.visa_free ? 'visa-ok' : 'visa-need') }, o.visa_free ? 'безвиз для РФ' : 'нужна виза/по прибытии') : null,
          ...(o.tags || []).map(t => el('span', { class: 'tag' }, t))),
        el('div', { style: 'flex:1' }),
        el('div', { class: 'mono', style: 'text-align:right' },
          el('b', { style: 'font-size:22px' }, money(o.total)),
          el('div', { class: 'faint', style: 'font-size:11px' }, o.in_budget ? 'влезает в бюджет ' : 'дороже бюджета')),
      ),
      el('div', { style: 'margin-top:14px' },
        o.via ? el('div', { class: 'trip-leg via' },
          el('span', {}, 'плечо до хаба: '),
          el('b', {}, (o.feeder ? o.feeder.mode_human : '') + ' до ' + o.via),
          o.feeder ? el('span', { class: 'faint' }, ' ' + fmtTime(o.feeder.departure_at) + ' · и обратно') : null,
          o.feeder ? el('span', { class: 'mono', style: 'margin-left:auto' }, money(o.feeder.price + (o.feeder_back ? o.feeder_back.price : 0))) : null,
        ) : null,
        leg('', 'туда' + (o.via ? ' (из ' + o.via + ')' : ''), o.there),
        leg('', 'обратно' + (o.via ? ' (до ' + o.via + ')' : ''), o.back),
        o.hotel ? el('div', { class: 'trip-leg' },
          el('span', {}, 'отель: '), el('b', {}, o.hotel.name),
          el('span', { class: 'faint' }, ` ${o.hotel.rating}/10 · ${o.nights} ноч.`),
          el('span', { class: 'mono', style: 'margin-left:auto' }, money(o.hotel.price)),
          o.hotel.checkout_url ? el('a', { class: 'btn ghost sm', href: o.hotel.checkout_url, target: '_blank', rel: 'noopener' }, 'отель ↗') : null,
        ) : el('div', { class: 'trip-leg faint' }, 'отель: не нашёлся — посмотрите на месте'),
      ),
      el('div', { style: 'display:flex;gap:10px;justify-content:center;margin-top:18px;flex-wrap:wrap' },
        el('button', { class: 'btn', onclick: spin }, 'крутить ещё'),
        el('button', { class: 'btn', onclick: () => showBoardingPasses(state) }, 'посадочные'),
        el('button', { class: 'btn', disabled: state.planBusy, onclick: loadPlan }, state.planBusy ? 'составляю…' : 'маршрут по городу'),
      ),
      spotsBlock(state),
      state.plan ? planBlock(state.plan) : null,
    );
  }

  // Лента впечатлений: LLM называет места, Википедия даёт фото, Google
  // Maps держит точку. Карусель как в «Авиасейлс Впечатления».
  async function loadSpots() {
    if (!CFG.llm_enabled || !state.current || typeof state.current !== 'object') return;
    state.spotsBusy = true; render();
    try {
      const res = await api.post('/api/spots', { city: state.current.city, interests: [...state.interests] });
      state.spots = res.spots;
      state.spotsBusy = false; render();
      hydrateSpotPhotos(state.spots, state.current.city, render);
    } catch { state.spotsBusy = false; render(); }
  }

  function spotsBlock(state) {
    if (state.spotsBusy) return el('div', { class: 'faint', style: 'text-align:center;margin-top:16px;font-size:13px' }, 'собираю ленту впечатлений…');
    if (!state.spots || !state.spots.length) return null;
    const o = state.current;
    return el('div', { style: 'margin-top:18px' },
      el('div', { class: 'section-title' }, 'Впечатления · ' + o.city),
      el('div', { class: 'spots' }, ...state.spots.map(s => el('a', {
        class: 'spot', target: '_blank', rel: 'noopener',
        href: 'https://www.google.com/maps/search/?api=1&query=' + encodeURIComponent(s.name + ', ' + o.city),
        style: s._photo ? `background-image:linear-gradient(180deg,rgba(8,10,14,.1) 30%,rgba(8,10,14,.88)),url('${s._photo}')` : '',
      },
        el('div', { class: 'spot-emoji' }, s.emoji || ''),
        el('div', { class: 'spot-name' }, s.name),
        el('div', { class: 'spot-why' }, s.why || ''),
      ))),
      el('div', { class: 'faint', style: 'font-size:11px;margin-top:6px' }, 'фото — Википедия · клик открывает место в Google Картах · подборка сгенерирована ИИ'),
    );
  }

  async function loadPlan() {
    state.planBusy = true; render();
    try {
      state.plan = await api.post('/api/cityplan', { city: state.current.city, interests: [...state.interests], days: state.days });
    } catch (e) { toast(e.message); }
    state.planBusy = false; render();
  }

  render();
}

// hydrateSpotPhotos pulls photos from Wikipedia with a fallback cascade —
// точная статья → полнотекстовый поиск с картинкой → фото самого города —
// so РФ-места без «идеальной» статьи всё равно получают снимок.
async function wikiSummaryPhoto(title) {
  try {
    const r = await fetch('https://ru.wikipedia.org/api/rest_v1/page/summary/' + encodeURIComponent(title.replace(/ /g, '_')));
    if (!r.ok) return '';
    const j = await r.json();
    return (j.thumbnail && j.thumbnail.source) ? j.thumbnail.source.replace(/\/(\d+)px-/, '/640px-') : '';
  } catch { return ''; }
}

async function wikiSearchPhoto(query) {
  try {
    const u = 'https://ru.wikipedia.org/w/api.php?action=query&format=json&origin=*' +
      '&generator=search&gsrlimit=3&gsrsearch=' + encodeURIComponent(query) +
      '&prop=pageimages&piprop=thumbnail&pithumbsize=640';
    const r = await fetch(u);
    if (!r.ok) return '';
    const j = await r.json();
    const pages = (j.query && j.query.pages) ? Object.values(j.query.pages) : [];
    pages.sort((a, b) => (a.index || 9) - (b.index || 9));
    for (const p of pages) if (p.thumbnail && p.thumbnail.source) return p.thumbnail.source;
    return '';
  } catch { return ''; }
}

async function hydrateSpotPhotos(spots, city, rerender) {
  // Городское фото — общий последний фолбэк, тянется один раз.
  const cityPhotoP = wikiSummaryPhoto(city);
  await Promise.all(spots.map(async (s) => {
    const title = (s.wiki || s.name || '').trim();
    if (!title) return;
    s._photo = await wikiSummaryPhoto(title)
      || await wikiSearchPhoto(s.name + ' ' + city)
      || await cityPhotoP
      || '';
  }));
  rerender();
}


// planBlock renders the walking plan with working Google Maps exports.
function planBlock(plan) {
  const box = el('div', { style: 'margin-top:18px' },
    el('div', { class: 'section-title' }, 'Маршрут по городу'),
    plan.note ? el('div', { class: 'faint', style: 'font-size:12px;margin-bottom:10px' }, plan.note) : null);
  for (const day of plan.days || []) {
    const stops = (day.stops || []).filter(s => s.name);
    box.append(el('div', { class: 'plan-day' },
      el('div', { style: 'display:flex;align-items:center;gap:10px' },
        el('b', {}, day.title || 'День'),
        el('div', { style: 'flex:1' }),
        stops.length >= 2 ? el('a', { class: 'btn ghost sm', href: gmapsUrl(plan.city, stops), target: '_blank', rel: 'noopener' }, 'открыть в Google Картах') : null,
      ),
      ...stops.map((s, i) => el('div', { class: 'plan-stop' },
        el('span', { class: 'mono faint' }, (i + 1) + '.'), ' ', el('b', {}, s.name),
        s.why ? el('span', { class: 'faint' }, ' — ' + s.why) : null)),
    ));
  }
  return box;
}

// gmapsUrl builds a Google Maps walking-directions deeplink through the
// day's stops — the export the pocket needs.
function gmapsUrl(city, stops) {
  const q = (s) => encodeURIComponent(s.name.includes(city) ? s.name : s.name + ', ' + city);
  const origin = q(stops[0]);
  const destination = q(stops[stops.length - 1]);
  const way = stops.slice(1, -1).map(q).join('%7C');
  return `https://www.google.com/maps/dir/?api=1&origin=${origin}&destination=${destination}` +
    (way ? `&waypoints=${way}` : '') + '&travelmode=walking';
}

// ================= BOARDING PASS =================
// Рендерит поездку как авиационный посадочный талон — для поезда и автобуса.
// Это НЕ билет: талон-приглашение, QR ведёт на реальный чекаут Туту.
const MODE_CODE = { railway: 'TRAIN', bus: 'BUS', avia: 'FLIGHT', etrain: 'LOCAL' };

function boardingPass(p) {
  // p: {from, to, date, dep, arr, mode, number, price, passenger, checkout_url}
  const qrBox = el('div', { class: 'bp-qr' });
  if (p.checkout_url && typeof QRCode !== 'undefined') {
    try { new QRCode(qrBox, { text: p.checkout_url, width: 92, height: 92, colorDark: '#221E1A', colorLight: '#F6EEDD', correctLevel: QRCode.CorrectLevel.M }); }
    catch { qrBox.textContent = '—'; }
  } else qrBox.textContent = '—';
  const fact = (k, v) => el('div', { class: 'bp-fact' }, el('div', { class: 'k' }, k), el('div', { class: 'v' }, v));
  return el('div', { class: 'bpass' },
    el('div', { class: 'bp-main' },
      el('div', { class: 'bp-head' },
        el('span', { class: 'bp-brand' }, 'УЛЫБКА'),
        el('span', { class: 'bp-kind' }, 'ПОСАДОЧНЫЙ / BOARDING'),
        el('span', { class: 'bp-mode' }, (MODE_IC[p.mode] || '') + ' ' + (MODE_CODE[p.mode] || 'TRIP') + (p.number ? ' ' + p.number : '')),
      ),
      el('div', { class: 'bp-route' },
        el('div', { class: 'bp-city' }, el('div', { class: 'big' }, p.from), el('div', { class: 'tm' }, p.dep)),
        el('div', { class: 'bp-arrow' }, ''.replace('', MODE_IC[p.mode] || '→')),
        el('div', { class: 'bp-city', style: 'text-align:right' }, el('div', { class: 'big' }, p.to), el('div', { class: 'tm' }, p.arr)),
      ),
      el('div', { class: 'bp-facts' },
        fact('ПАССАЖИР', p.passenger || 'ПАССАЖИР УЛЫБКИ'),
        fact('ДАТА', p.date || '—'),
        fact('РЕЙС', (MODE_CODE[p.mode] || '') + ' ' + (p.number || '—')),
        fact('МЕСТО', p.seat || 'выбор при покупке'),
      ),
      el('div', { class: 'bp-note' }, 'это не билет — приглашение к поездке; купить: QR → чекаут Туту'),
    ),
    el('div', { class: 'bp-stub' },
      qrBox,
      el('div', { class: 'bp-price' }, p.price != null ? money(p.price) : ''),
      p.checkout_url ? el('a', { class: 'bp-buy', href: p.checkout_url, target: '_blank', rel: 'noopener' }, 'дожать →') : null,
    ),
  );
}

function passModal(passes, title) {
  const overlay = el('div', { class: 'bp-overlay', onclick: (e) => { if (e.target === overlay) overlay.remove(); } },
    el('div', { class: 'bp-sheet' },
      el('div', { style: 'display:flex;align-items:center;gap:10px;margin-bottom:14px' },
        el('h2', { style: 'margin:0;font-size:18px;color:var(--ink)' }, title || 'Посадочные'),
        el('div', { style: 'flex:1' }),
        el('button', { class: 'btn ghost sm', onclick: () => overlay.remove() }, 'закрыть')),
      ...passes,
    ));
  document.body.append(overlay);
}

function legToPass(l, from, to, passenger) {
  return boardingPass({
    from, to, mode: l.mode, number: l.number,
    dep: fmtTime(l.departure_at), arr: fmtTime(l.arrival_at), date: fmtDay(l.departure_at),
    price: l.price, checkout_url: l.checkout_url, passenger,
  });
}

function showBoardingPasses(state) {
  const o = state.current; if (!o) return;
  const origin = normalizeCityInput(state.origin);
  const passes = [];
  if (o.feeder) passes.push(legToPass(o.feeder, origin, o.via, null));
  if (o.there) passes.push(legToPass(o.there, o.via || origin, o.city, null));
  if (o.back) passes.push(legToPass(o.back, o.city, o.via || origin, null));
  if (o.feeder_back) passes.push(legToPass(o.feeder_back, o.via, origin, null));
  passModal(passes, 'Поездка в ' + o.city);
}

function smileRainSmall() {
  for (let i = 0; i < 14; i++) {
    const s = el('span', { class: 'smile-drop' }, '🙂');
    s.style.left = (Math.random() * 100) + 'vw';
    s.style.animationDelay = (Math.random() * 0.5) + 's';
    s.style.fontSize = (14 + Math.random() * 16) + 'px';
    document.body.append(s);
    setTimeout(() => s.remove(), 3600);
  }
}

// ================= MEET (увидеться вдвоём) =================
// Not an event — just two people and the question «где увидеться дешевле».
// Live prices for both sides rank the meeting cities; one click turns the
// winner into a full event with a board and checkouts.
function MeetPage() {
  const root = app(); clear(root);
  const state = { a: '', b: '', date: defaultMeetDate(), deadline: '15:00', results: null, note: '', busy: false, interests: new Set() };

  const render = () => {
    clear(root);
    const form = el('div', { class: 'card' },
      el('h2', {}, 'Увидеться вдвоём'),
      el('div', { class: 'card-sub' }, 'Два города — и живые цены Туту скажут, где встретиться дешевле всего обоим. Повод не нужен. Плитки ниже сужают, ГДЕ именно хочется улыбнуться.'),
      interestTiles(state.interests, render),
      el('div', { style: 'height:12px' }),
      el('div', { class: 'form-grid' },
        el('div', { class: 'field' }, el('label', {}, 'Ты откуда'),
          el('input', { value: state.a, placeholder: 'Москва', list: 'cities', oninput: (e) => state.a = e.target.value })),
        el('div', { class: 'field' }, el('label', {}, 'Друг откуда'),
          el('input', { value: state.b, placeholder: 'Пермь', list: 'cities', oninput: (e) => state.b = e.target.value })),
        el('div', { class: 'field' }, el('label', {}, 'Когда'),
          el('input', { type: 'date', value: state.date, oninput: (e) => state.date = e.target.value })),
        el('div', { class: 'field' }, el('label', {}, 'Встретиться к'),
          el('input', { type: 'time', value: state.deadline, oninput: (e) => state.deadline = e.target.value })),
      ),
      el('div', { style: 'display:flex;justify-content:flex-end' },
        el('button', { class: 'btn primary', disabled: state.busy, onclick: run }, state.busy ? 'считаю живые цены…' : 'Где увидеться?')),
    );
    // el() skips null kids; native append() would print «null».
    root.append(el('div', {},
      el('div', { style: 'display:flex;align-items:center;gap:12px;margin-bottom:20px' },
        el('button', { class: 'btn ghost sm', onclick: () => Router.go('/') }, '← назад'),
        el('h1', { style: 'margin:0;font-size:24px' }, 'Увидеться вдвоём')),
      form,
      state.results ? meetResults(state) : null,
    ));
  };

  async function run() {
    if (!state.a.trim() || !state.b.trim()) { toast('Укажите оба города'); return; }
    state.busy = true; render();
    try {
      const res = await api.post('/api/meet', {
        city_a: normalizeCityInput(state.a), city_b: normalizeCityInput(state.b),
        date: state.date, deadline: state.deadline, interests: [...state.interests],
      });
      state.results = res.cities || []; state.note = res.note || '';
    } catch (e) { toast(e.message); }
    state.busy = false; render();
  }
  render();
}

function meetResults(state) {
  const both = state.results.filter(c => c.reachable === 2);
  const rest = state.results.filter(c => c.reachable !== 2);
  const card = el('div', { class: 'card' },
    el('h2', {}, 'Где улыбнётесь дешевле'),
    el('div', { class: 'card-sub' }, state.note || 'Полная цена встречи = дорога обоих. Успевают оба — иначе город ниже.'),
  );
  const row = (c, dim) => {
    const legs = (c.breakdown || []).map(l =>
      l.arrival ? `${l.guest} из ${l.from}: ${l.mode} к ${l.arrival} за ${money(l.price)}` : `${l.guest} из ${l.from}: не успевает`);
    return el('div', { class: 'recent-item', style: dim ? 'opacity:.55' : '' },
      el('div', { style: 'font-size:20px' }, dim ? '' : ''),
      el('div', {}, el('b', {}, c.city),
        el('div', { class: 'faint mono', style: 'font-size:12px' }, legs.join(' · '))),
      el('div', { style: 'flex:1' }),
      el('div', { class: 'mono', style: 'text-align:right' },
        el('b', {}, c.reachable === 2 ? money(c.total_price) : '—'),
        el('div', { class: 'faint', style: 'font-size:11px' }, 'встреча целиком')),
      c.reachable === 2 ? el('button', { class: 'btn sm', onclick: () => makeMeetEvent(state, c) }, 'собрать здесь') : null,
    );
  };
  both.forEach(c => card.append(row(c, false)));
  rest.forEach(c => card.append(row(c, true)));
  if (!state.results.length) card.append(el('div', { class: 'empty' }, 'не нашлось городов — попробуйте другую дату'));
  return card;
}

async function makeMeetEvent(state, c) {
  try {
    const { id } = await api.post('/api/events', {
      name: 'Увидеться в ' + c.city, destination: c.city,
      date: state.date, deadline: state.deadline, buffer_hours: 1, spacing_min: 0,
      totalizator: false,
      guests: [
        { name: 'Ты', city: normalizeCityInput(state.a), profile: 'cheaper', adults: 1, find_companions: true },
        { name: 'Друг', city: normalizeCityInput(state.b), profile: 'cheaper', adults: 1, find_companions: true },
      ],
    });
    toast('Встреча собрана — держите табло на двоих');
    Router.go('/e/' + id);
  } catch (e) { toast(e.message); }
}

function defaultMeetDate() {
  // Ближайшая суббота — день, когда люди видятся.
  const d = new Date();
  d.setDate(d.getDate() + ((6 - d.getDay() + 7) % 7 || 7));
  return d.toISOString().slice(0, 10);
}

// ================= JOIN (guest self-registration) =================
// The organizer sends ONE link to the group chat; every guest adds themselves
// in five seconds and lands on their own live card. Distributed data entry.
function JoinPage(id) {
  const root = app(); clear(root);
  root.append(el('div', { class: 'empty' }, el('span', { class: 'spinner' }), ' открываю приглашение…'));
  api.get('/api/events/' + id + '/join').then((info) => {
    clear(root);
    const g = { name: '', city: '', profile: 'cheaper', needs_lodging: false, find_companions: false, adults: 1 };
    const render = () => {
      clear(root);
      root.append(el('div', { class: 'guest-detail' },
        el('div', { class: 'ticket' },
          el('div', { class: 'head' },
            el('div', { class: 'ev' }, 'УЛЫБКА · приглашение'),
            el('h1', {}, info.name || 'Событие'),
            el('div', { class: 'faint mono', style: 'font-size:13px;margin-top:6px' },
              (info.destination ? info.destination + ' · ' : '') + fmtDay(info.date + 'T12:00:00') + ' · сбор в ' + info.deadline +
              (info.guests ? ` · уже едут: ${info.guests}` : '')),
          ),
          el('div', { class: 'body' },
            el('p', { style: 'margin:0 0 18px;font-size:15px' }, 'Отметьтесь — и «Улыбка» соберёт вам маршрут к сроку, а организатор увидит вас на живом табло.'),
            el('div', { class: 'field' }, el('label', {}, 'Как вас зовут'),
              el('input', { value: g.name, placeholder: 'Имя', oninput: (e) => g.name = e.target.value })),
            el('div', { class: 'field' }, el('label', {}, 'Откуда едете'),
              el('input', { value: g.city, placeholder: 'Город', list: 'cities', oninput: (e) => g.city = e.target.value })),
            el('div', { class: 'form-grid' },
              el('div', { class: 'field' }, el('label', {}, 'Что важнее'),
                el('div', { class: 'chip-toggle' },
                  el('button', { class: g.profile === 'cheaper' ? 'on' : '', onclick: () => { g.profile = 'cheaper'; render(); } }, 'дешевле'),
                  el('button', { class: g.profile === 'faster' ? 'on' : '', onclick: () => { g.profile = 'faster'; render(); } }, 'быстрее'))),
              el('div', { class: 'field' }, el('label', {}, 'Сколько вас'),
                el('input', { type: 'number', min: '1', value: g.adults, oninput: (e) => g.adults = +e.target.value })),
              el('div', { class: 'field' }, el('label', {}, 'Нужен ночлег'),
                el('div', { class: 'chip-toggle' },
                  el('button', { class: g.needs_lodging ? 'on' : '', onclick: () => { g.needs_lodging = !g.needs_lodging; render(); } }, g.needs_lodging ? 'да' : 'нет'))),
              el('div', { class: 'field' }, el('label', {}, 'Ищу попутчиков'),
                el('div', { class: 'chip-toggle' },
                  el('button', { class: g.find_companions ? 'on' : '', onclick: () => { g.find_companions = !g.find_companions; render(); } }, g.find_companions ? 'да' : 'нет')),
                el('div', { class: 'hint' }, 'имя откроется попутчику только с вашего согласия')),
            ),
            el('button', { class: 'btn primary dogzhat', onclick: submit }, 'Я еду'),
          ),
        ),
      ));
    };
    async function submit() {
      if (!g.city.trim()) { toast('Укажите город'); return; }
      g.city = normalizeCityInput(g.city);
      try {
        const res = await api.post('/api/events/' + id + '/join', g);
        toast('Вы на табло! Считаю ваш маршрут…');
        Router.go('/g/' + id + '/' + res.guest_id);
      } catch (e) { toast(e.message); }
    }
    render();
  }).catch((e) => { clear(root); root.append(el('div', { class: 'empty' }, 'Приглашение недоступно: ' + e.message)); });
}

// board & guest card live in board.js (loaded after)
window.__smile = { app, el, $, clear, api, CFG, MODE_IC, modeIc, fmtTime, fmtDay, money, fmtDur, STATUS_RU, toast, Router };

// ---------------- boot ----------------
Router.add(/^\/$/, Home);
Router.add(/^\/new/, Constructor);
Router.add(/^\/e\/([a-z0-9]+)$/, (id) => Board(id));
Router.add(/^\/g\/([a-z0-9]+)\/([a-z0-9]+)$/, (id, gid) => GuestCard(id, gid));
Router.add(/^\/j\/([a-z0-9]+)$/, (id) => JoinPage(id));
Router.add(/^\/meet/, MeetPage);
Router.add(/^\/spin/, SpinPage);

// boot() is invoked at the end of board.js, once Board/GuestCard are defined.
async function boot() {
  try { Object.assign(CFG, await api.get('/api/config')); } catch {}
  // City autocomplete shared by every city input (list="cities").
  if (CFG.cities && CFG.cities.length) {
    const dl = el('datalist', { id: 'cities' }, ...CFG.cities.map(c => el('option', { value: c })));
    document.body.append(dl);
  }
  Router.resolve();
}
window.boot = boot;
