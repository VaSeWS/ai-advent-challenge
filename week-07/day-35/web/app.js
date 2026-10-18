(() => {
  'use strict';
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const panels = { template: $('#panel-template'), schedule: $('#panel-schedule'), tournaments: $('#panel-tournaments') };
  const state = { persisted: null, draft: null, revision: 0, savedRevision: 0, activeTab: 'template', preview: null, selectedStarts: new Set(), tournaments: [], selectedTournamentId: null, chat: [], proposal: null, chatPending: false, previewPending: false, createPending: false, previewMessage: '', createMessage: '', provider: null, rawOpen: false, rawText: null, jsonError: '', recurrenceFriendly: null, jsonInvalid: false };

  function clone(value) { return JSON.parse(JSON.stringify(value)); }
  function escapeHTML(value) { return String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]); }
  function safeText(value) { return String(value ?? ''); }
  function apiError(body, response) { return body?.error || body?.message || `Ошибка сервера (${response.status})`; }
  function validateRawConfig(value) {
    const object = (item, path) => {
      if (!item || typeof item !== 'object' || Array.isArray(item)) throw new Error(`${path} должен быть объектом.`);
    };
    const string = (item, path) => { if (typeof item !== 'string') throw new Error(`${path} должно быть строкой.`); };
    const number = (item, path) => { if (typeof item !== 'number' || !Number.isFinite(item)) throw new Error(`${path} должно быть числом.`); };
    object(value, 'Конфигурация'); object(value.template, 'template'); object(value.template.entry, 'template.entry');
    for (const key of ['id','name','description','tie_policy']) string(value.template[key], `template.${key}`);
    number(value.template.max_players, 'template.max_players'); string(value.template.entry.type, 'template.entry.type'); string(value.template.entry.ticket_id, 'template.entry.ticket_id');
    if (!Array.isArray(value.template.rounds)) throw new Error('template.rounds должен быть массивом.');
    value.template.rounds.forEach((round, index) => {
      const path = `template.rounds[${index}]`; object(round, path); object(round.rules, `${path}.rules`);
      number(round.number, `${path}.number`); string(round.name, `${path}.name`); string(round.description, `${path}.description`);
      number(round.duration_minutes, `${path}.duration_minutes`); number(round.rules.attempt_limit, `${path}.rules.attempt_limit`); string(round.rules.score_aggregation, `${path}.rules.score_aggregation`);
    });
    if (!Array.isArray(value.template.rewards)) throw new Error('template.rewards должен быть массивом.');
    value.template.rewards.forEach((reward, index) => { const path = `template.rewards[${index}]`; object(reward, path); number(reward.place, `${path}.place`); string(reward.type, `${path}.type`); number(reward.amount, `${path}.amount`); });
    object(value.schedule, 'schedule');
    for (const key of ['id','template_id','dtstart','timezone','rrule']) string(value.schedule[key], `schedule.${key}`);
    for (const key of ['registration_open_offset_seconds','registration_close_offset_seconds']) number(value.schedule[key], `schedule.${key}`);
    if (typeof value.schedule.enabled !== 'boolean') throw new Error('schedule.enabled должно быть логическим значением.');
    return value;
  }
  async function request(url, options = {}) {
    const response = await fetch(url, { ...options, headers: { ...(options.body ? { 'Content-Type': 'application/json' } : {}), ...options.headers } });
    let body;
    try { body = await response.json(); } catch { throw new Error(`Сервер вернул некорректный ответ (${response.status})`); }
    if (!response.ok) throw new Error(apiError(body, response));
    return body;
  }
  function setGlobalError(message = '') {
    const el = $('#global-error'); el.textContent = message; el.hidden = !message;
  }
  function touchDraft() {
    state.revision += 1;
    state.preview = null; state.previewMessage = ''; state.selectedStarts.clear();
    state.proposal = state.proposal ? { ...state.proposal, stale: true } : null;
    setGlobalError('');
    updateSaveStatus(); updateJSON(); updatePreviewArea(); renderProposal();
    $('#preview-button')?.toggleAttribute('disabled', state.jsonInvalid || state.previewPending || state.createPending);
  }
  function updateSaveStatus(message) {
    const el = $('#save-status'); const dirty = state.revision !== state.savedRevision;
    el.className = `save-status ${message ? 'is-error' : dirty ? 'is-dirty' : state.persisted ? 'is-saved' : ''}`;
    el.textContent = message || (dirty ? 'Есть несохранённые изменения' : state.persisted ? 'Сохранено' : '');
    $('#save-button').disabled = !state.draft || state.jsonInvalid || !dirty;
  }
  function clearJSONError() {
    state.jsonInvalid = false; state.jsonError = ''; state.rawText = null;
  }
  function syncFriendlyControls(rule) {
    const active=panels.schedule; const end=$('[data-recurrence-end]',active);
    const freq=$('[data-path="recurrence.freq"]',active);if(freq)freq.value=rule.freq;
    const interval=$('[data-path="recurrence.interval"]',active);if(interval)interval.value=rule.interval;
    const endType=$('[data-path="recurrence.endType"]',active);if(endType)endType.value=rule.endType;
    const wrap=$('#byday-wrap',active);if(wrap)wrap.hidden=rule.freq==='DAILY';
    $$('[data-day]',active).forEach(day=>day.checked=rule.byday.includes(day.dataset.day));
    if(end){
      end.type=rule.endType==='COUNT'?'number':'text';
      if(rule.endType==='COUNT'){end.min='1';end.step='1';end.removeAttribute('placeholder');}
      else{end.removeAttribute('min');end.removeAttribute('step');end.placeholder='YYYYMMDDTHHMMSSZ';}
      const label=$('label[for="recurrence-end"]',active);
      if(label)label.textContent=rule.endType==='COUNT'?'Количество повторов':'Заканчивается (UNTIL, UTC)';
      end.value=rule.end;
    }
  }
  function syncPendingControls() {
    const disabled=state.previewPending||state.createPending;
    Object.values(panels).forEach(panel=>panel.querySelectorAll('input,textarea,select,button').forEach(control=>{
      if(disabled){
        if(!control.hasAttribute('data-pending-disabled'))control.dataset.pendingDisabled=String(control.disabled);
        control.disabled=true;
      }else if(control.hasAttribute('data-pending-disabled')){
        control.disabled=control.dataset.pendingDisabled==='true';delete control.dataset.pendingDisabled;
      }
    }));
    const previewButton=$('#preview-button');
    if(previewButton){previewButton.disabled=disabled||state.jsonInvalid||!state.draft;previewButton.textContent=state.previewPending?'Рассчитываем…':'Обновить предпросмотр';}
  }
  function field(label, key, value, type = 'text', extra = '') {
    return `<div class="field ${extra}"><label for="f-${key}">${label}</label><input id="f-${key}" data-path="${key}" type="${type}" value="${escapeHTML(value)}"></div>`;
  }
  function textarea(label, key, value, extra = '') {
    return `<div class="field ${extra}"><label for="f-${key}">${label}</label><textarea id="f-${key}" data-path="${key}">${escapeHTML(value)}</textarea></div>`;
  }
  function select(label, key, value, options, extra = '') {
    return `<div class="field ${extra}"><label for="f-${key}">${label}</label><select id="f-${key}" data-path="${key}">${options.map(([v, text]) => `<option value="${escapeHTML(v)}" ${String(value) === String(v) ? 'selected' : ''}>${escapeHTML(text)}</option>`).join('')}</select></div>`;
  }
  function num(label, key, value, min = '', step = '1', extra = '') {
    return `<div class="field ${extra}"><label for="f-${key}">${label}</label><input id="f-${key}" data-path="${key}" type="number" ${min !== '' ? `min="${min}"` : ''} step="${step}" value="${escapeHTML(value)}"></div>`;
  }
  function templateHTML() {
    const t = state.draft.template;
    const rounds = (t.rounds || []).map((r, i) => `<article class="repeat-row"><div class="repeat-head"><strong>Раунд ${i + 1}</strong><button type="button" class="button button-small button-danger" data-remove-round="${i}" aria-label="Удалить раунд ${i + 1}">Удалить</button></div><div class="repeat-grid round-grid">${num('Номер', `template.rounds.${i}.number`, r.number)}${field('Название', `template.rounds.${i}.name`, r.name)}${num('Длительность', `template.rounds.${i}.duration_minutes`, r.duration_minutes, '1')} ${num('Лимит попыток', `template.rounds.${i}.rules.attempt_limit`, r.rules?.attempt_limit, '1')}${select('Подсчёт результата', `template.rounds.${i}.rules.score_aggregation`, r.rules?.score_aggregation, [['best','Лучший результат'],['sum','Сумма результатов']])}${textarea('Описание', `template.rounds.${i}.description`, r.description, 'full')}</div></article>`).join('');
    const rewards = (t.rewards || []).map((r, i) => `<article class="repeat-row"><div class="repeat-head"><strong>Награда ${i + 1}</strong><button type="button" class="button button-small button-danger" data-remove-reward="${i}" aria-label="Удалить награду ${i + 1}">Удалить</button></div><div class="repeat-grid">${num('Место', `template.rewards.${i}.place`, r.place, '1')}${select('Тип', `template.rewards.${i}.type`, r.type, [['points','Баллы']])}${num('Количество баллов', `template.rewards.${i}.amount`, r.amount, '0')}</div></article>`).join('');
    return `<div class="section-head"><div><h2>Шаблон турнира</h2><p>Фиксированный формат с накоплением очков между раундами.</p></div></div>
      <div class="panel-card"><h3 class="card-title">Основные параметры</h3><div class="form-grid">${field('Идентификатор шаблона','template.id',t.id)}${field('Название','template.name',t.name)}${textarea('Описание','template.description',t.description,'full')}${num('Максимум игроков','template.max_players',t.max_players,'1')}${select('Участие','template.entry.type',t.entry?.type,[['free','Бесплатно'],['ticket','По билету']])}<div class="field"><label for="f-template.entry.ticket_id">Идентификатор билета</label><input id="f-template.entry.ticket_id" data-path="template.entry.ticket_id" type="text" value="${escapeHTML(t.entry?.ticket_id)}"><span class="help">Для бесплатного участия идентификатор не используется.</span></div></div></div>
      <div class="panel-card"><div class="section-head"><div><h3 class="card-title">Раунды</h3><p>Очки накапливаются; каждый раунд имеет отдельные правила.</p></div><button type="button" class="button button-small button-secondary" data-add-round>＋ Добавить раунд</button></div><div class="repeater">${rounds || '<div class="empty-state">Раундов пока нет. Добавьте первый раунд, чтобы описать этапы турнира.</div>'}</div></div>
      <div class="panel-card"><div class="section-head"><div><h3 class="card-title">Награды</h3><p>При равенстве мест каждый участник получает полную награду этого места.</p></div><button type="button" class="button button-small button-secondary" data-add-reward>＋ Добавить награду</button></div><div class="repeater">${rewards || '<div class="empty-state">Наград пока нет. Добавьте места и количество баллов.</div>'}</div><div class="field" style="margin-top:14px"><span class="field-label">Политика ничьей</span><span class="help">Совместное место · каждому участнику полная награда за место (shared_place_full_reward)</span></div></div>${rawBlock()}`;
  }
  function parseRule(raw) {
    const parts = raw.split(';'); const map = {};
    for (const part of parts) { const index = part.indexOf('='); if (index < 1) return { error: 'Каждая часть RRULE должна иметь формат КЛЮЧ=ЗНАЧЕНИЕ.' }; const key = part.slice(0,index).toUpperCase(); if (map[key] !== undefined) return { error: `Повторяется параметр ${key}.` }; map[key] = part.slice(index+1).toUpperCase(); }
    const allowed = new Set(['FREQ','INTERVAL','BYDAY','COUNT','UNTIL']); const unsupported = Object.keys(map).filter(key => !allowed.has(key));
    if (unsupported.length) return { error: `Неподдерживаемые параметры RRULE: ${unsupported.join(', ')}. Их можно сохранить в исходном правиле, но переключение на удобный редактор запрещено.` };
    if (!['DAILY','WEEKLY'].includes(map.FREQ) || (map.INTERVAL !== undefined && (!/^\d+$/.test(map.INTERVAL) || Number(map.INTERVAL) < 1)) || (map.COUNT !== undefined && map.UNTIL !== undefined) || (map.COUNT === undefined && map.UNTIL === undefined)) return { error: 'Удобный редактор поддерживает DAILY/WEEKLY, INTERVAL ≥ 1 и ровно одно из COUNT или UNTIL.' };
    if (map.FREQ === 'DAILY' && map.BYDAY !== undefined) return { error: 'BYDAY для DAILY не поддерживается удобным редактором.' };
    if (map.BYDAY !== undefined && !/^(MO|TU|WE|TH|FR|SA|SU)(,(MO|TU|WE|TH|FR|SA|SU))*$/.test(map.BYDAY)) return { error: 'В BYDAY допустимы только дни MO,TU,WE,TH,FR,SA,SU без повторов.' };
    const days = map.BYDAY ? map.BYDAY.split(',') : [];
    if (days.length !== new Set(days).size) return { error: 'В BYDAY есть повторяющиеся дни.' };
    if (map.COUNT !== undefined && (!/^\d+$/.test(map.COUNT) || Number(map.COUNT) < 1)) return { error: 'COUNT должен быть положительным целым числом.' };
    if (map.UNTIL !== undefined && !/^\d{8}T\d{6}Z$/.test(map.UNTIL)) return { error: 'UNTIL должен иметь формат UTC YYYYMMDDTHHMMSSZ.' };
    return { value: { freq: map.FREQ, interval: map.INTERVAL === undefined ? 1 : Number(map.INTERVAL), byday: days, endType: map.COUNT !== undefined ? 'COUNT' : 'UNTIL', end: map.COUNT ?? map.UNTIL } };
  }
  function buildRule(friendly) {
    const chunks = [`FREQ=${friendly.freq}`, `INTERVAL=${friendly.interval}`];
    if (friendly.freq === 'WEEKLY' && friendly.byday.length) chunks.push(`BYDAY=${friendly.byday.join(',')}`);
    chunks.push(`${friendly.endType}=${friendly.end}`); return chunks.join(';');
  }
  function scheduleHTML() {
    const s = state.draft.schedule; const parsed = parseRule(s.rrule || '');
    const recurrence = state.recurrenceFriendly || parsed.value || { freq:'WEEKLY', interval:1, byday:[], endType:'COUNT', end:'10' };
    const days = [['MO','Пн'],['TU','Вт'],['WE','Ср'],['TH','Чт'],['FR','Пт'],['SA','Сб'],['SU','Вс']].map(([v,label]) => `<label class="checkbox-field"><input type="checkbox" data-day="${v}" ${recurrence.byday.includes(v)?'checked':''}><span>${label}</span></label>`).join('');
    const error = parsed.error;
    return `<div class="section-head"><div><h2>Расписание серии</h2><p>Время указывается в локальном часовом поясе; регистрационные смещения — знаковые секунды до старта.</p></div></div>
      ${error && !state.recurrenceFriendly ? `<div class="notice notice-error" role="alert">${escapeHTML(error)} Сначала исправьте исходное правило; переключение редактора не изменит RRULE.</div>` : ''}
      <div class="panel-card"><h3 class="card-title">Основные параметры</h3><div class="form-grid">${field('Идентификатор расписания','schedule.id',s.id)}${field('Идентификатор шаблона','schedule.template_id',s.template_id)}<div class="field"><label for="f-schedule.dtstart">Дата и время первого старта</label><input id="f-schedule.dtstart" data-path="schedule.dtstart" type="datetime-local" step="1" value="${escapeHTML(s.dtstart||'')}"><span class="help">Локальное время ${escapeHTML(s.timezone || 'часового пояса')}.</span></div>${field('Часовой пояс IANA','schedule.timezone',s.timezone)}<div class="field"><label for="f-schedule.registration_open_offset_seconds">Открытие регистрации</label><div class="input-with-suffix"><input id="f-schedule.registration_open_offset_seconds" data-path="schedule.registration_open_offset_seconds" type="number" step="1" value="${escapeHTML(s.registration_open_offset_seconds)}"><span>секунды</span></div><span class="help">Знаковое смещение от старта; например −86400 — за сутки.</span></div><div class="field"><label for="f-schedule.registration_close_offset_seconds">Закрытие регистрации</label><div class="input-with-suffix"><input id="f-schedule.registration_close_offset_seconds" data-path="schedule.registration_close_offset_seconds" type="number" step="1" value="${escapeHTML(s.registration_close_offset_seconds)}"><span>секунды</span></div><span class="help">Знаковое смещение от старта; например −3600 — за час.</span></div><label class="checkbox-field"><input id="f-schedule.enabled" data-path="schedule.enabled" type="checkbox" ${s.enabled?'checked':''}><span>Расписание включено</span></label></div></div>
      <div class="panel-card"><h3 class="card-title">Повторение</h3><div class="recurrence-layout"><div><fieldset class="recurrence-controls" ${error && !state.recurrenceFriendly?'disabled':''}><div class="form-grid">${select('Частота','recurrence.freq',recurrence.freq,[['DAILY','Ежедневно'],['WEEKLY','Еженедельно']])}${num('Интервал','recurrence.interval',recurrence.interval,'1')}<div class="field full" id="byday-wrap" ${recurrence.freq==='DAILY'?'hidden':''}><span class="field-label">Дни недели</span><div class="inline-actions">${days}</div></div>${select('Ограничить по','recurrence.endType',recurrence.endType,[['COUNT','Количество повторов'],['UNTIL','Дата окончания (UTC)']])}<div class="field"><label for="recurrence-end">${recurrence.endType==='COUNT'?'Количество повторов':'Заканчивается (UNTIL, UTC)'}</label><input id="recurrence-end" data-recurrence-end type="${recurrence.endType==='COUNT'?'number':'text'}" ${recurrence.endType==='COUNT'?'min="1" step="1"':'placeholder="YYYYMMDDTHHMMSSZ"'} value="${escapeHTML(recurrence.end)}"></div></div></fieldset></div><div><div class="rrule-row"><label for="rrule-raw">Исходное RRULE</label></div><textarea id="rrule-raw" class="raw-editor" data-path="schedule.rrule" aria-label="Исходное правило RRULE">${escapeHTML(s.rrule)}</textarea><p id="rrule-validation" class="rrule-help" role="status">${error?escapeHTML(error):'Редактирование правила напрямую синхронизирует поля выше. Неизвестные параметры сохраняются без изменений; для перехода к удобным полям правило должно быть поддерживаемым.'}</p></div></div></div>
      <div class="panel-card"><div class="section-head"><div><h3 class="card-title">Предпросмотр дат</h3><p>Проверьте даты серии и выберите конкретные старты для ручного создания.</p></div><button id="preview-button" type="button" class="button button-secondary" ${state.draft && !state.previewPending && !state.createPending?'':'disabled'}>${state.previewPending?'Рассчитываем…':'Обновить предпросмотр'}</button></div><div id="preview-area"></div><div class="inline-actions" style="margin-top:12px"><button id="create-button" type="button" class="button button-primary" disabled>Создать выбранные турниры</button><span id="create-status" class="help" role="status">${escapeHTML(state.createMessage)}</span></div></div>${rawBlock()}`;
  }
  function rawBlock() {
    const value = state.jsonInvalid && state.rawText !== null ? state.rawText : JSON.stringify(state.draft,null,2);
    return `<details class="panel-card" ${state.rawOpen?'open':''} data-json-details><summary class="json-toggle"><h3>Конфигурация JSON</h3><span class="help">Просмотр и редактирование целиком</span></summary><p class="help">Изменение JSON относится к черновику; некорректный JSON не применяется.</p><textarea id="json-editor" class="raw-editor" aria-label="Конфигурация JSON" spellcheck="false">${escapeHTML(value)}</textarea><div id="json-error" class="help" role="alert">${escapeHTML(state.jsonError)}</div></details>`;
  }
  function snapshotDetail(t) {
    if (!t) return '<div class="empty-state"><strong>Выберите турнир</strong>Снимки конфигурации сохраняются отдельно от текущего шаблона и расписания.</div>';
    return `<div class="snapshot-box"><h3>Снимок турнира</h3><div class="snapshot-meta">ID: ${escapeHTML(t.id)}<br>Статус: ${escapeHTML(t.status)}<br>Старт: ${escapeHTML(t.starts_at)}<br>Открытие регистрации: ${escapeHTML(t.registration_opens_at)}<br>Закрытие регистрации: ${escapeHTML(t.registration_closes_at)}</div><h3>Шаблон на момент создания</h3><pre class="json-view snapshot-json"></pre><h3 style="margin-top:14px">Расписание на момент создания</h3><pre class="json-view snapshot-json schedule-snapshot"></pre></div>`;
  }
  function tournamentsHTML() {
    const list = state.tournaments || [];
    const items = list.map((t,i) => `<button class="tournament-item ${t.id===state.selectedTournamentId?'is-selected':''}" type="button" data-tournament-id="${escapeHTML(t.id)}"><strong>Турнир ${i+1}</strong><span>${escapeHTML(t.starts_at)}</span></button>`).join('');
    return `<div class="section-head"><div><h2>Созданные турниры</h2><p>Сохранённые турниры содержат неизменяемые снимки шаблона и расписания.</p></div></div>${list.length?`<div class="detail-layout"><div class="tournament-list">${items}</div><div id="snapshot-detail"></div></div>`:'<div class="empty-state"><strong>Турниров ещё нет</strong>Настройте расписание, обновите предпросмотр и вручную выберите даты для создания.</div>'}${rawBlock()}`;
  }
  function updateJSON() {
    const input = $('#json-editor');
    if (input && document.activeElement !== input) input.value = state.jsonInvalid && state.rawText !== null ? state.rawText : JSON.stringify(state.draft,null,2);
    const error = $('#json-error'); if (error) error.textContent = state.jsonError;
  }
  function renderProposal() {
    const area = $('#proposal-area'); const p = state.proposal;
    if (!p) { area.hidden = true; area.replaceChildren(); return; }
    area.hidden = false; area.replaceChildren();
    const title = document.createElement('h3'); title.textContent = p.config ? 'Предложение изменений' : 'Уточнение'; area.append(title);
    const text = document.createElement('p'); text.textContent = p.message; area.append(text);
    if (p.changes?.length) { const ul = document.createElement('ul'); ul.className='proposal-changes'; for(const item of p.changes){const li=document.createElement('li');li.textContent=item;ul.append(li);} area.append(ul); }
    const actions = document.createElement('div'); actions.className='proposal-actions';
    if (p.config) { const apply=document.createElement('button'); apply.type='button';apply.className='button button-small button-primary';apply.textContent='Применить к черновику';apply.disabled=!!p.stale||state.previewPending||state.createPending;apply.addEventListener('click',()=>{if(p.stale||state.revision!==p.revision||state.previewPending||state.createPending)return;state.draft=clone(p.config);clearJSONError();state.revision++;state.preview=null;state.previewMessage='';state.createMessage='';state.selectedStarts.clear();state.proposal=null;state.recurrenceFriendly=null;renderActive();updateSaveStatus();updatePreviewArea();});actions.append(apply); }
    if (p.stale) {const note=document.createElement('span');note.className='stale-note';note.textContent='Черновик изменён — запросите новое предложение.';actions.append(note);} area.append(actions);
  }
  function renderActive() {
    Object.entries(panels).forEach(([key,panel])=>{panel.hidden=key!==state.activeTab;if(key!==state.activeTab)panel.replaceChildren();});
    if(state.activeTab==='template')panels.template.innerHTML=templateHTML();
    else if(state.activeTab==='schedule')panels.schedule.innerHTML=scheduleHTML();
    else panels.tournaments.innerHTML=tournamentsHTML();
    syncPendingControls();
    if(state.activeTab==='tournaments')renderSelectedSnapshot();
    if(state.activeTab==='schedule')updatePreviewArea();
    $('#tournament-count').textContent=String(state.tournaments.length);
    $$('.tab').forEach(tab=>{const selected=tab.dataset.tab===state.activeTab;tab.classList.toggle('is-active',selected);tab.setAttribute('aria-selected',String(selected));});
    updateSaveStatus(); renderProposal();
  }
  function renderSelectedSnapshot() {
    const t=state.tournaments.find(item=>item.id===state.selectedTournamentId); const host=$('#snapshot-detail'); if(!host)return;
    host.innerHTML=snapshotDetail(t);
    const template=host.querySelector('pre'); const schedule=host.querySelector('.schedule-snapshot');
    if(template) template.textContent=JSON.stringify(t?.template_snapshot ?? {},null,2);
    if(schedule) schedule.textContent=JSON.stringify(t?.schedule_snapshot ?? {},null,2);
  }
  function updatePreviewArea() {
    const host=$('#preview-area'); if(!host)return;
    if(state.previewPending){host.innerHTML='<div class="empty-state">Рассчитываем даты серии…</div>';$('#create-button')?.setAttribute('disabled','');return;}
    if(!state.preview){host.innerHTML=state.previewMessage?`<div class="notice notice-error" role="alert">${escapeHTML(state.previewMessage)}</div>`:'<div class="empty-state"><strong>Предпросмотр не рассчитан</strong>Измените расписание при необходимости и нажмите «Обновить предпросмотр». Любая правка черновика сбрасывает результаты.</div>';$('#create-button')?.setAttribute('disabled','');return;}
    if(!state.preview.occurrences.length){host.innerHTML='<div class="empty-state"><strong>Нет дат в предпросмотре</strong>Проверьте RRULE, дату начала и параметры серии.</div>';return;}
    host.innerHTML=`<div class="notice notice-info">Ничьи используют общий накопительный зачёт: каждый участник получает полную награду за занятое место.</div><div class="table-wrap"><table class="data-table"><thead><tr><th><span class="sr-only">Выбрать</span></th><th>№</th><th>Начало</th><th>Регистрация открывается</th><th>Регистрация закрывается</th></tr></thead><tbody>${state.preview.occurrences.map((o,i)=>`<tr><td><input type="checkbox" data-occurrence="${i}" aria-label="Выбрать турнир ${i+1}" ${state.selectedStarts.has(o.starts_at)?'checked':''} ${state.createPending?'disabled':''}></td><td>${i+1}</td><td>${escapeHTML(o.starts_at)}</td><td>${escapeHTML(o.registration_opens_at)}</td><td>${escapeHTML(o.registration_closes_at)}</td></tr>`).join('')}</tbody></table></div>`;
    const create=$('#create-button');if(create)create.disabled=state.createPending||!state.selectedStarts.size;
  }
  function setPath(object, path, value) { const parts=path.split('.');let target=object;for(let i=0;i<parts.length-1;i++)target=target[parts[i]];target[parts.at(-1)]=value; }
  function readInputValue(el) { if(el.type==='checkbox')return el.checked;if(el.type==='number')return el.value===''?null:Number(el.value);if(el.type==='datetime-local'&&el.value.length===16)return `${el.value}:00`;return el.value; }
  function scheduleFriendlyChanged() {
    const panel=panels.schedule; const freq=$('[data-path="recurrence.freq"]',panel)?.value || 'WEEKLY';
    const interval=Number($('[data-path="recurrence.interval"]',panel)?.value || 1);
    const endType=$('[data-path="recurrence.endType"]',panel)?.value || 'COUNT'; const end=$('[data-recurrence-end]',panel)?.value || '';
    const byday=$$('[data-day]:checked',panel).map(el=>el.dataset.day);
    if(freq==='DAILY')byday.length=0;
    const friendly={freq,interval,byday,endType,end};state.recurrenceFriendly=friendly;
    setPath(state.draft,'schedule.rrule',buildRule(friendly));
    const raw=$('#rrule-raw',panel);if(raw)raw.value=state.draft.schedule.rrule;
    clearJSONError();touchDraft();
  }
  function appendChat(role, content, isError=false) {
    if(!isError)state.chat.push({role,content});const host=$('#chat-messages');const node=document.createElement('div');node.className=`message ${role}${isError?' error':''}`;node.textContent=content;host.append(node);host.scrollTop=host.scrollHeight;
  }
  function updateProvider(info) {
    state.provider=info;const name=info?.provider||'AI';const model=info?.model||'';const available=!!info?.available;
    $('#provider-status').textContent=`${name}${model?` · ${model}`:''} · ${available?'доступен':'недоступен'}`;
    $('#provider-dot').className=`provider-dot ${available?'is-online':'is-offline'}`;
  }
  async function loadInitial() {
    if(state.draft && (state.revision!==state.savedRevision || state.jsonInvalid) && !window.confirm('Отбросить несохранённые изменения и перезагрузить конфигурацию?'))return;
    state.revision+=1;
    const captured=state.revision;
    state.preview=null;state.selectedStarts.clear();state.proposal=null;state.rawText=null;state.jsonError='';state.jsonInvalid=false;state.recurrenceFriendly=null;
    $('#chat-pending')?.remove();renderProposal();updateSaveStatus();
    setGlobalError('');
    try {
      const results=await Promise.allSettled([request('/api/state'),request('/api/provider')]);
      if(results[0].status==='fulfilled' && state.revision===captured) {
        const data=results[0].value;state.persisted=clone(data.config);state.draft=clone(data.config);state.tournaments=data.tournaments||[];state.savedRevision=captured;state.selectedTournamentId=state.tournaments[0]?.id||null;renderActive();
      } else if(results[0].status==='rejected') setGlobalError(`Не удалось загрузить состояние: ${results[0].reason.message}`);
      if(results[1].status==='fulfilled')updateProvider(results[1].value);else updateProvider({provider:'AI',available:false});
      updateSaveStatus();
    } catch(error) {if(state.revision===captured)setGlobalError(error.message);}
  }
  async function saveConfig() {
    if(!state.draft)return;const captured=state.revision;const payload=clone(state.draft);$('#save-button').disabled=true;updateSaveStatus('Сохранение…');setGlobalError('');
    try {const data=await request('/api/config',{method:'PUT',body:JSON.stringify(payload)});state.persisted=clone(data.config);state.tournaments=data.tournaments||state.tournaments;if(state.revision===captured){state.savedRevision=captured;state.draft=clone(data.config);renderActive();}else{updateSaveStatus();$('#tournament-count').textContent=String(state.tournaments.length);}}
    catch(error){updateSaveStatus(error.message);setGlobalError(`Не удалось сохранить конфигурацию: ${error.message}`);}
  }
  async function preview() {
    if(state.previewPending||state.createPending||!state.draft||state.jsonInvalid)return;
    state.previewPending=true;state.preview=null;state.previewMessage='';state.createMessage='';state.selectedStarts.clear();
    const captured=state.revision;syncPendingControls();updatePreviewArea();
    try {
      const data=await request('/api/preview',{method:'POST',body:JSON.stringify({config:clone(state.draft),count:20})});
      if(state.revision===captured){state.preview={...data,revision:captured};state.previewMessage='';}
    } catch(error) {if(state.revision===captured)state.previewMessage=`Не удалось рассчитать даты: ${error.message}`;}
    finally {state.previewPending=false;syncPendingControls();if(state.activeTab==='schedule')updatePreviewArea();}
  }
  async function createSelected() {
    if(state.previewPending||state.createPending||!state.preview||state.jsonInvalid||state.preview.revision!==state.revision||!state.selectedStarts.size)return;
    state.createPending=true;state.createMessage='Создаём выбранные турниры…';
    const captured=state.revision;const starts=[...state.selectedStarts];syncPendingControls();updatePreviewArea();
    if(state.activeTab==='schedule'){const status=$('#create-status');if(status)status.textContent=state.createMessage;}
    try {
      const data=await request('/api/tournaments',{method:'POST',body:JSON.stringify({config:clone(state.draft),starts_at:starts})});
      state.tournaments=data.tournaments||state.tournaments;state.persisted=clone(data.config);state.selectedStarts.clear();state.selectedTournamentId=state.tournaments.at(-1)?.id||state.selectedTournamentId;
      if(state.revision===captured){state.savedRevision=captured;state.createMessage='Турниры созданы.';}
    } catch(error) {state.createMessage=`Не удалось создать турниры: ${error.message}`;}
    finally {
      state.createPending=false;syncPendingControls();
      if(state.revision===captured&&state.createMessage==='Турниры созданы.')renderActive();
      else if(state.activeTab==='schedule')updatePreviewArea();
      $('#tournament-count').textContent=String(state.tournaments.length);
      if(state.activeTab==='schedule'){const status=$('#create-status');if(status)status.textContent=state.createMessage;}
    }
  }
  async function sendChat(content) {
    if(state.chatPending||!state.draft||state.jsonInvalid||!content.trim())return;
    const captured=state.revision;appendChat('user',content);state.chatPending=true;$('#chat-send').disabled=true;
    const pending=document.createElement('div');pending.className='chat-pending';pending.textContent='Помощник формирует ответ…';pending.id='chat-pending';$('#chat-messages').append(pending);
    const messages=state.chat.slice(-24).map(item=>({role:item.role,content:item.content}));
    try {const result=await request('/api/chat',{method:'POST',body:JSON.stringify({config:clone(state.draft),revision:captured,messages})});
      $('#chat-pending')?.remove();
      if(state.revision!==captured)return;
      appendChat('assistant',result.message||'Ответ получен.');
      state.proposal={message:result.message||'',config:result.config||null,revision:captured,changes:result.changes||[],stale:result.revision!==captured};renderProposal();
    } catch(error) {$('#chat-pending')?.remove();if(state.revision===captured)appendChat('assistant',error.message,true);}
    finally {state.chatPending=false;$('#chat-send').disabled=state.jsonInvalid;}
  }

  document.addEventListener('click',event=>{
    const tab=event.target.closest('[data-tab]');if(tab){state.activeTab=tab.dataset.tab;renderActive();return;}
    if(event.target.closest('#save-button')){if(!state.previewPending&&!state.createPending)saveConfig();}
    if(event.target.closest('#reload-button')){if(!state.previewPending&&!state.createPending)loadInitial();}
    if(event.target.closest('#preview-button'))preview();
    if(event.target.closest('#create-button'))createSelected();
    if(state.previewPending||state.createPending)return;
    const addRound=event.target.closest('[data-add-round]');if(addRound){clearJSONError();state.draft.template.rounds.push({number:state.draft.template.rounds.length+1,name:'',description:'',duration_minutes:10,rules:{attempt_limit:1,score_aggregation:'best'}});touchDraft();renderActive();}
    const removeRound=event.target.closest('[data-remove-round]');if(removeRound){clearJSONError();state.draft.template.rounds.splice(Number(removeRound.dataset.removeRound),1);touchDraft();renderActive();}
    const addReward=event.target.closest('[data-add-reward]');if(addReward){clearJSONError();state.draft.template.rewards.push({place:state.draft.template.rewards.length+1,type:'points',amount:0});touchDraft();renderActive();}
    const removeReward=event.target.closest('[data-remove-reward]');if(removeReward){clearJSONError();state.draft.template.rewards.splice(Number(removeReward.dataset.removeReward),1);touchDraft();renderActive();}
    const tournament=event.target.closest('[data-tournament-id]');if(tournament){state.selectedTournamentId=tournament.dataset.tournamentId;renderActive();}
  });
  document.addEventListener('input',event=>{
    const el=event.target;
    if(state.previewPending||state.createPending)return;
    if(el.id==='json-editor') {
      state.rawText=el.value;
      try { state.draft=validateRawConfig(JSON.parse(el.value));state.jsonInvalid=false;state.jsonError='';state.recurrenceFriendly=null; }
      catch(error) { state.jsonInvalid=true;state.jsonError=error.message; }
      touchDraft();$('#chat-send').disabled=state.jsonInvalid||state.chatPending;return;
    }
    if(el.matches('[data-day]')){scheduleFriendlyChanged();return;}
    if(el.matches('[data-recurrence-end]')){scheduleFriendlyChanged();return;}
    const path=el.dataset.path;if(!path||path.startsWith('recurrence.'))return;
    const value=readInputValue(el);
    if(path==='schedule.rrule'){
      const parsed=parseRule(value);
      if(parsed.value){state.recurrenceFriendly=parsed.value;syncFriendlyControls(parsed.value);}
      else state.recurrenceFriendly=null;
      const validation=$('#rrule-validation');if(validation)validation.textContent=parsed.error||'Правило поддерживается: удобные поля синхронизированы.';
      const controls=$('.recurrence-controls',panels.schedule);if(controls)controls.disabled=!!parsed.error;
    }
    clearJSONError();setPath(state.draft,path,value);touchDraft();
  });
  document.addEventListener('change',event=>{
    const el=event.target;
    if(state.previewPending||state.createPending)return;
    if(el.id==='json-editor') { if(!state.jsonInvalid)renderActive(); return; }
    if(el.dataset.path==='recurrence.endType') {
      const previous=state.recurrenceFriendly?.endType || parseRule(state.draft.schedule.rrule||'').value?.endType || 'COUNT';
      if(previous!==el.value) {
        const end=$('[data-recurrence-end]',panels.schedule);
        if(end)end.value=el.value==='COUNT'?'10':'';
      }
      scheduleFriendlyChanged();renderActive();return;
    }
    if(el.dataset.path?.startsWith('recurrence.')){scheduleFriendlyChanged();renderActive();return;}
    if(el.matches('[data-day]')){scheduleFriendlyChanged();return;}
    if(el.matches('[data-occurrence]')){
      if(state.createPending||state.previewPending)return;
      const item=state.preview?.occurrences[Number(el.dataset.occurrence)];
      if(item){if(el.checked)state.selectedStarts.add(item.starts_at);else state.selectedStarts.delete(item.starts_at);}
      const button=$('#create-button');if(button)button.disabled=state.createPending||!state.selectedStarts.size||state.preview?.revision!==state.revision;
    }
  });
  document.addEventListener('toggle',event=>{if(event.target.matches('[data-json-details]'))state.rawOpen=event.target.open;},true);
  $('#chat-form').addEventListener('submit',event=>{event.preventDefault();const input=$('#chat-input');const content=input.value.trim();if(!content)return;input.value='';sendChat(content);});
  loadInitial();
})();
