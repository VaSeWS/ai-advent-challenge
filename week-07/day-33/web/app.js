(() => {
  "use strict";

  const $ = (selector) => document.querySelector(selector);
  const els = {
    user: $("#user-select"), tickets: $("#ticket-list"), ticketCount: $("#ticket-count"),
    eyebrow: $("#ticket-eyebrow"), title: $("#ticket-title"), subtitle: $("#ticket-subtitle"),
    status: $("#ticket-status"), conversation: $("#conversation"), composer: $("#composer"),
    input: $("#message-input"), send: $("#send-button"), stream: $("#stream-status"), error: $("#error-message"),
    description: $("#context-description"), meta: $("#ticket-meta"), config: $("#ticket-config"), checks: $("#ticket-checks"),
    sources: $("#sources-list"), sourceCount: $("#source-count"), observations: $("#observations-list"), observationCount: $("#observation-count"),
    sourceDetails: $("#sources-details"), observationDetails: $("#observations-details")
  };
  const SESSION_KEY = "iron-support-session-id";
  const sessionId = getSessionId();
  let activeUser = null;
  let activeTicket = null;
  let availableTickets = [];
  let messages = [];
  let selectionVersion = 0;
  let selectionController = null;
  let chatController = null;
  let busy = false;
  const drafts = new Map();

  function draftKey(userId, ticketId) {
    return JSON.stringify([userId, ticketId]);
  }

  function saveCurrentDraft() {
    if (!activeUser || !activeTicket) return;
    const key = draftKey(activeUser.id, activeTicket.id);
    if (els.input.value.trim()) drafts.set(key, els.input.value);
    else if (!busy) drafts.delete(key);
  }

  function restoreDraft(userId, ticketId) {
    els.input.value = drafts.get(draftKey(userId, ticketId)) || "";
    els.send.disabled = busy || !els.input.value.trim();
  }

  function getSessionId() {
    let id = sessionStorage.getItem(SESSION_KEY);
    if (!id) {
      if (globalThis.crypto && typeof globalThis.crypto.randomUUID === "function") {
        id = globalThis.crypto.randomUUID();
      } else {
        const bytes = new Uint8Array(16);
        globalThis.crypto.getRandomValues(bytes);
        id = [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
      }
      sessionStorage.setItem(SESSION_KEY, id);
    }
    return id;
  }

  function api(path, options = {}) {
    return fetch(path, {
      ...options,
      headers: { "X-Session-ID": sessionId, ...(options.headers || {}) }
    });
  }

  async function readJson(response) {
    let body;
    try { body = await response.json(); } catch { body = null; }
    if (!response.ok) throw new Error(body && body.error ? body.error : `Не удалось загрузить данные (${response.status}).`);
    return body;
  }

  function setError(text = "") {
    els.error.textContent = text;
    els.error.hidden = !text;
  }

  function setBusy(value) {
    busy = value;
    els.input.disabled = value || !activeTicket;
    els.send.disabled = value || !activeTicket || !els.input.value.trim();
    els.send.querySelector("span").textContent = value ? "Отвечает…" : "Отправить";
  }

  function addText(parent, tag, text, className) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    node.textContent = text == null ? "" : String(text);
    parent.append(node);
    return node;
  }

  function renderTickets() {
    els.tickets.replaceChildren();
    els.tickets.setAttribute("aria-busy", "false");
    els.ticketCount.textContent = String(availableTickets.length);
    if (!availableTickets.length) {
      addText(els.tickets, "p", "У этого профиля пока нет обращений.", "muted");
      return;
    }
    for (const ticket of availableTickets) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "ticket-button";
      button.dataset.ticketId = ticket.id;
      button.setAttribute("aria-current", String(Boolean(activeTicket && activeTicket.id === ticket.id)));
      const top = addText(button, "span", "", "ticket-button-top");
      addText(top, "span", ticket.id, "ticket-id");
      addText(top, "span", statusLabel(ticket.status), "mini-status");
      addText(button, "span", ticket.subject, "ticket-subject");
      button.addEventListener("click", () => selectTicket(ticket.id));
      els.tickets.append(button);
    }
  }

  function statusLabel(status) {
    return ({ open: "Открыто", in_progress: "В работе", resolved: "Решено" })[status] || String(status || "");
  }

  function clearContext() {
    els.description.textContent = "Выберите обращение, чтобы загрузить контекст.";
    els.description.classList.add("muted");
    els.meta.replaceChildren();
    els.config.textContent = "—";
    els.checks.replaceChildren();
    addText(els.checks, "li", "Нет данных", "muted");
  }

  function clearEvidence() {
    els.sources.replaceChildren();
    addText(els.sources, "p", "Источники появятся вместе с ответом.", "muted");
    els.observations.replaceChildren();
    addText(els.observations, "p", "Наблюдения появятся вместе с ответом.", "muted");
    els.sourceCount.textContent = "0";
    els.observationCount.textContent = "0";
    els.sourceDetails.open = false;
    els.observationDetails.open = false;
  }

  function renderContext(ticket) {
    els.description.textContent = ticket.description || "Описание не указано.";
    els.description.classList.toggle("muted", !ticket.description);
    els.meta.replaceChildren();
    for (const [label, value] of [
      ["Обращение", ticket.id],
      ["Статус", statusLabel(ticket.status)],
      ["Профиль", activeUser ? activeUser.name : ""],
      ["Версия приложения", activeUser ? activeUser.app_version : ""],
      ["Windows", activeUser ? activeUser.windows : ""],
      ["Среда", activeUser ? activeUser.environment : ""],
    ]) {
      if (!value) continue;
      const row = document.createElement("div");
      addText(row, "dt", label);
      addText(row, "dd", value);
      els.meta.append(row);
    }
    els.config.textContent = ticket.config && Object.keys(ticket.config).length ? JSON.stringify(ticket.config, null, 2) : "Нет данных";
    els.checks.replaceChildren();
    if (Array.isArray(ticket.checks) && ticket.checks.length) {
      for (const check of ticket.checks) addText(els.checks, "li", check);
    } else addText(els.checks, "li", "Нет данных", "muted");
  }

  function renderMessages() {
    els.conversation.replaceChildren();
    if (!messages.length) {
      const card = document.createElement("div");
      card.className = "welcome-card";
      addText(card, "span", "✳", "welcome-icon").setAttribute("aria-hidden", "true");
      addText(card, "p", "Помощник по iRon", "eyebrow");
      addText(card, "h2", "Разберёмся вместе");
      addText(card, "p", "Задайте вопрос по обращению. Ответ будет основан на документации и данных этого обращения.");
      addText(card, "div", "⌕  Сначала факты, затем рекомендации", "welcome-proof");
      els.conversation.append(card);
      return;
    }
    const guidance = document.createElement("div");
    guidance.className = "history-guidance";
    guidance.textContent = "Продолжите диалог или задайте новый вопрос по этому обращению. Ответы опираются на контекст и документацию.";
    els.conversation.append(guidance);
    for (const message of messages) {
      const user = message.role === "user";
      const row = document.createElement("article");
      row.className = `message-row ${user ? "user" : "assistant"}`;
      row.setAttribute("aria-label", user ? "Сообщение пользователя" : "Ответ помощника");
      addText(row, "span", user ? "Я" : "i", "avatar").setAttribute("aria-hidden", "true");
      const bubble = document.createElement("div");
      bubble.className = "message-bubble";
      addText(bubble, "div", user ? "Вы" : "Помощник iRon", "message-label");
      addText(bubble, "div", message.content || "");
      row.append(bubble);
      els.conversation.append(row);
      if (!user && (message.sources || message.observations)) renderEvidence(message.sources || [], message.observations || []);
    }
    els.conversation.scrollTop = els.conversation.scrollHeight;
  }

  function safeSourceURL(value) {
    if (typeof value !== "string" || !value) return "";
    try {
      const url = new URL(value);
      return (url.protocol === "http:" || url.protocol === "https:") && !url.username && !url.password ? url.href : "";
    } catch {
      return "";
    }
  }

  function renderEvidence(sources, observations) {
    els.sources.replaceChildren();
    els.sourceCount.textContent = String(sources.length);
    if (!sources.length) addText(els.sources, "p", "Для этого ответа источники не указаны.", "muted");
    for (const source of sources) {
      const detail = document.createElement("details");
      detail.className = "source-card";
      const summary = document.createElement("summary");
      summary.textContent = source.title || source.id || "Источник документации";
      detail.append(summary);
      const lineRange = source.start_line || source.end_line ? `Строки ${source.start_line || "?"}–${source.end_line || "?"}` : "Фрагмент документации";
      addText(detail, "p", `${source.path || ""}${source.path ? " · " : ""}${lineRange}`, "source-meta");
      const sourceURL = safeSourceURL(source.url);
      if (sourceURL) {
        const link = addText(detail, "a", "Открыть источник ↗", "source-link");
        link.href = sourceURL;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.setAttribute("aria-label", `Открыть источник: ${source.title || source.id || "документация"} (в новой вкладке)`);
      }
      addText(detail, "p", source.text || "Текст фрагмента отсутствует.");
      els.sources.append(detail);
    }
    els.observations.replaceChildren();
    els.observationCount.textContent = String(observations.length);
    if (!observations.length) addText(els.observations, "p", "Для этого ответа наблюдения не указаны.", "muted");
    for (const observation of observations) {
      const card = document.createElement("div");
      card.className = "observation-card";
      addText(card, "strong", observation.label || "Наблюдение");
      addText(card, "p", observation.value || "");
      els.observations.append(card);
    }
  }

  function appendMessage(role, content, extra = {}) {
    messages.push({ role, content, ...extra });
    renderMessages();
  }

  function resetForSelection() {
    activeTicket = null;
    messages = [];
    els.title.textContent = "Загрузка обращения…";
    els.eyebrow.textContent = activeUser ? activeUser.name : "Центр поддержки";
    els.subtitle.textContent = "Загружаем историю выбранного обращения.";
    els.status.hidden = true;
    els.stream.textContent = "";
    setError("");
    els.input.value = "";
    setBusy(false);
    clearContext();
    clearEvidence();
    renderMessages();
    renderTickets();
  }

  async function selectUser(userId) {
    selectionVersion += 1;
    const version = selectionVersion;
    if (selectionController) selectionController.abort();
    saveCurrentDraft();
    if (chatController) chatController.abort();
    selectionController = new AbortController();
    activeUser = null;
    availableTickets = [];
    resetForSelection();
    els.tickets.setAttribute("aria-busy", "true");
    try {
      const ticketResponse = await api(`/api/tickets?user_id=${encodeURIComponent(userId)}`, { signal: selectionController.signal });
      const ticketData = await readJson(ticketResponse);
      if (version !== selectionVersion) return;
      availableTickets = Array.isArray(ticketData.tickets) ? ticketData.tickets : [];
      renderTickets();
      const userOption = [...els.user.options].find((option) => option.value === userId);
      activeUser = userOption ? { id: userId, name: userOption.textContent } : { id: userId, name: userId };
      if (!availableTickets.length) {
        els.title.textContent = "Нет обращений";
        els.subtitle.textContent = "У этого демонстрационного профиля нет доступных обращений.";
        els.input.disabled = true;
        return;
      }
      await loadTicket(availableTickets[0].id, version);
    } catch (error) {
      if (error.name === "AbortError" || version !== selectionVersion) return;
      els.title.textContent = "Не удалось загрузить профиль";
      els.subtitle.textContent = "Проверьте соединение с локальным сервисом.";
      setError(error.message || "Ошибка загрузки обращений.");
      els.tickets.setAttribute("aria-busy", "false");
    }
  }

  async function selectTicket(ticketId) {
    if (!activeUser || (activeTicket && activeTicket.id === ticketId)) return;
    saveCurrentDraft();
    selectionVersion += 1;
    const version = selectionVersion;
    if (selectionController) selectionController.abort();
    if (chatController) chatController.abort();
    selectionController = new AbortController();
    await loadTicket(ticketId, version);
  }

  async function loadTicket(ticketId, version) {
    const ticket = availableTickets.find((item) => item.id === ticketId);
    if (!ticket) return;
    resetForSelection();
    els.title.textContent = ticket.subject || ticket.id;
    els.eyebrow.textContent = ticket.id;
    els.subtitle.textContent = "Загружаем историю и контекст обращения…";
    try {
      const response = await api(`/api/ticket?user_id=${encodeURIComponent(activeUser.id)}&ticket_id=${encodeURIComponent(ticketId)}`, { signal: selectionController.signal });
      const data = await readJson(response);
      if (version !== selectionVersion) return;
      activeTicket = data.ticket;
      activeUser = data.user || activeUser;
      messages = Array.isArray(data.messages) ? data.messages : [];
      els.title.textContent = activeTicket.subject || activeTicket.id;
      els.eyebrow.textContent = activeTicket.id;
      els.subtitle.textContent = activeUser.name ? `Обращение пользователя ${activeUser.name}` : "Контекст текущего обращения";
      els.status.hidden = false;
      els.status.textContent = statusLabel(activeTicket.status);
      renderContext(activeTicket);
      restoreDraft(activeUser.id, activeTicket.id);
      renderMessages();
      renderTickets();
      setBusy(false);
      els.input.focus({ preventScroll: true });
    } catch (error) {
      if (error.name === "AbortError" || version !== selectionVersion) return;
      els.subtitle.textContent = "Не удалось загрузить историю обращения.";
      setError(error.message || "Ошибка загрузки обращения.");
    }
  }

  function addUserMessage(text) {
    appendMessage("user", text);
  }

  async function submitMessage(event) {
    event.preventDefault();
    const text = els.input.value.trim();
    if (!text || busy || !activeUser || !activeTicket) return;
    const version = selectionVersion;
    const selectedUserId = activeUser.id;
    const selectedTicketId = activeTicket.id;
    const controller = new AbortController();
    const key = draftKey(selectedUserId, selectedTicketId);
    drafts.set(key, text);
    chatController = controller;
    setError("");
    els.stream.textContent = "";
    els.input.value = "";
    addUserMessage(text);
    setBusy(true);
    try {
      const response = await api("/api/chat", {
        method: "POST",
        signal: controller.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ user_id: selectedUserId, ticket_id: selectedTicketId, message: text })
      });
      if (!response.ok) await readJson(response);
      if (!response.body) throw new Error("Сервис не вернул поток ответа.");
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      let answered = false;
      while (true) {
        const { value, done } = await reader.read();
        if (version !== selectionVersion) { await reader.cancel(); return; }
        buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
        let newline;
        while ((newline = buffer.indexOf("\n")) >= 0) {
          const line = buffer.slice(0, newline).trim();
          buffer = buffer.slice(newline + 1);
          if (!line) continue;
          const eventData = JSON.parse(line);
          if (eventData.type === "stage") {
            els.stream.textContent = stageLabel(eventData.stage);
          } else if (eventData.type === "answer") {
            els.stream.textContent = "";
            appendMessage("assistant", eventData.answer || "", { sources: eventData.sources || [], observations: eventData.observations || [] });
            answered = true;
          } else if (eventData.type === "error") {
            throw new Error(eventData.error || "Не удалось подготовить ответ.");
          }
        }
        if (done) break;
      }
      const finalLine = buffer.trim();
      if (finalLine) {
        const eventData = JSON.parse(finalLine);
        if (eventData.type === "answer") {
          els.stream.textContent = "";
          appendMessage("assistant", eventData.answer || "", { sources: eventData.sources || [], observations: eventData.observations || [] });
          answered = true;
        } else if (eventData.type === "error") throw new Error(eventData.error || "Не удалось подготовить ответ.");
        else if (eventData.type === "stage") els.stream.textContent = stageLabel(eventData.stage);
      }
      if (!answered) throw new Error("Поток завершился без ответа.");
      drafts.delete(key);
    } catch (error) {
      if (error.name !== "AbortError" && version === selectionVersion) {
        messages = messages.filter((message, index) => !(message.role === "user" && message.content === text && index === messages.length - 1));
        renderMessages();
        els.input.value = text;
        setError(error.message || "Не удалось отправить вопрос. Повторите попытку.");
      }
    } finally {
      if (version === selectionVersion) {
        els.stream.textContent = "";
        setBusy(false);
        chatController = null;
      }
    }
  }

  function stageLabel(stage) {
    return ({ mcp: "Проверяем данные обращения…", retrieval: "Ищем подтверждения в документации…", deepseek: "Формируем ответ…" })[stage] || "Обрабатываем запрос…";
  }

  async function bootstrap() {
    try {
      const response = await api("/api/bootstrap");
      const data = await readJson(response);
      const users = Array.isArray(data.users) ? data.users : [];
      els.user.replaceChildren();
      for (const user of users) {
        const option = document.createElement("option");
        option.value = user.id;
        option.textContent = `${user.name} · демо`;
        els.user.append(option);
      }
      els.user.disabled = users.length === 0;
      if (!users.length) throw new Error("Сервис не вернул демонстрационные профили.");
      await selectUser(users[0].id);
    } catch (error) {
      els.user.disabled = true;
      els.user.replaceChildren(new Option("Сервис недоступен", ""));
      els.title.textContent = "Сервис поддержки недоступен";
      els.subtitle.textContent = "Не удалось получить начальные данные.";
      setError(error.message || "Не удалось подключиться к сервису.");
    }
  }

  els.user.addEventListener("change", () => {
    if (els.user.value) selectUser(els.user.value);
  });
  els.input.addEventListener("input", () => {
    els.send.disabled = busy || !activeTicket || !els.input.value.trim();
  });
  els.input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      if (!els.send.disabled) els.composer.requestSubmit();
    }
  });
  els.composer.addEventListener("submit", submitMessage);
  bootstrap();
})();
