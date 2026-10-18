/**
 * Records the approved Day 35 tournament-admin flow in a managed browser tab.
 * The coordinator supplies the browser runtime and destination path.
 *
 * @param {object} browser Managed browser API.
 * @param {string} outputPath Destination MP4 path when recording is enabled.
 * @param {{baseURL?: string, record?: boolean, pace?: number}} options Runtime options.
 * @returns {Promise<object>} Recording metadata and observed scenario results.
 */
async function recordTournamentDemo(browser, outputPath, options = {}) {
  const baseURL = options.baseURL ?? "http://127.0.0.1:8045";
  const record = options.record ?? true;
  const pace = options.pace ?? 1;
  if (!Number.isFinite(pace) || pace < 0) throw new Error("pace must be a finite non-negative number");

  const tab = await browser.open({
    name: "Day 35 tournament admin demo",
    url: baseURL,
    viewport: { width: 1600, height: 1000 },
  });
  let recordingStarted = false;
  let recordingMetadata;
  let failure;
  const observed = {};
  const hold = (milliseconds) => pauseTournamentDemo(tab, milliseconds * pace);

  try {
    await tab.run(async ({ page }) => page.evaluate(() => window.scrollTo(0, 0)), { timeout: 10 });
    await tab.waitForSelector("#provider-status", { timeout: 60000 });
    await tab.waitForText("доступен", { timeout: 60000 });
    await tab.waitForSelector('#save-status:not(:empty)', { timeout: 60000 });
    const initial = await tab.run(async ({ page, assert }) => {
      const response = await page.evaluate(async () => {
        const [stateResponse, providerResponse] = await Promise.all([
          fetch("/api/state"), fetch("/api/provider"),
        ]);
        return {
          stateStatus: stateResponse.status,
          providerStatus: providerResponse.status,
          state: await stateResponse.json(),
          provider: await providerResponse.json(),
        };
      });
      assert(response.stateStatus === 200, `State request failed (${response.stateStatus})`);
      assert(response.providerStatus === 200, `Provider request failed (${response.providerStatus})`);
      assert(response.provider.provider === "deepseek" && response.provider.available === true,
        `DeepSeek must be available; received ${JSON.stringify(response.provider)}`);
      assert(Array.isArray(response.state.tournaments) && response.state.tournaments.length === 0,
        `Demo requires fresh state with zero tournaments; found ${response.state.tournaments?.length ?? "unknown"}`);
      assert(response.state.config?.template && response.state.config?.schedule,
        "Initial state is missing its template or schedule");
      return response;
    }, { timeout: 20 });
    observed.initialProvider = initial.provider.provider;
    observed.initialTournamentCount = initial.state.tournaments.length;
    observed.originalConfig = initial.state.config;
    await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => {
        const provider = document.querySelector("#provider-status")?.textContent || "";
        const error = document.querySelector("#global-error:not([hidden])")?.textContent.trim() || "";
        return {
          provider,
          error,
          welcomeVisible: !!document.querySelector("#chat-messages .chat-welcome"),
          templateVisible: !!document.querySelector("#panel-template h2"),
        };
      });
      assert(!result.error, `Initial UI error: ${result.error}`);
      assert(result.provider.includes("deepseek") && result.provider.includes("доступен"),
        `DeepSeek availability is not visible: ${result.provider}`);
      assert(result.welcomeVisible && result.templateVisible,
        "The original template and assistant chat must both be visible");
      await page.evaluate(() => window.scrollTo(0, 0));
    }, { timeout: 10 });

    if (record) {
      await tab.recordStart(outputPath);
      recordingStarted = true;
    }

    // Scene 1 — original template, free entry, and the live assistant.
    await hold(16000);

    // Scene 2 — ask the real provider, inspect the proposal, then explicitly apply and save.
    const question = "Назови турнир «Вечерний кубок», установи 8 игроков и награды 120, 70 и 30 очков за первые три места. Вход оставь бесплатным, остальные настройки сохрани.";
    const previousAssistantCount = await tab.run(async ({ page }) => page.evaluate(
      () => document.querySelectorAll("#chat-messages .message.assistant").length,
    ), { timeout: 10 });
    await tab.fill("#chat-input", question);
    await tab.click("#chat-send");
    await tab.run(async ({ page }, previousCount) => {
      await page.waitForFunction((count) => {
        const error = document.querySelector("#chat-messages .message.error");
        if (error) throw new Error(`Chat failed: ${error.textContent.trim()}`);
        const proposal = document.querySelector("#proposal-area");
        return !document.querySelector("#chat-pending") &&
          document.querySelectorAll("#chat-messages .message.assistant").length > count &&
          proposal && !proposal.hidden && proposal.querySelector("button");
      }, { timeout: 120000 }, previousCount);
    }, { args: [previousAssistantCount], timeout: 130 });
    await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => ({
        error: document.querySelector("#chat-messages .message.error")?.textContent.trim() || "",
        applyVisible: !!document.querySelector('#proposal-area button:not(:disabled)'),
        proposalText: document.querySelector("#proposal-area")?.innerText || "",
        saveDisabled: document.querySelector("#save-button")?.disabled,
        tournamentCount: document.querySelector("#tournament-count")?.textContent.trim(),
      }));
      assert(!result.error, `Chat error: ${result.error}`);
      assert(result.applyVisible, "The AI proposal is missing or cannot be explicitly applied");
      assert(result.saveDisabled, "AI response unexpectedly saved the configuration");
      assert(result.tournamentCount === "0", "AI response unexpectedly created a tournament");
      return result;
    }, { timeout: 10 });
    await hold(10000);
    await tab.click('#proposal-area button');
    await tab.waitForSelector('textarea#json-editor', { timeout: 10000 });
    const proposedConfig = await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => {
        const editor = document.querySelector("#json-editor");
        if (!editor) return { error: "JSON configuration view is absent" };
        try { return { config: JSON.parse(editor.value) }; }
        catch (error) { return { error: `Draft JSON is invalid: ${error.message}` }; }
      });
      assert(!result.error, result.error || "Could not read draft JSON");
      const config = result.config;
      assert(config.template.name === "Вечерний кубок", `Unexpected proposed name: ${config.template.name}`);
      assert(config.template.max_players === 8, `Unexpected proposed player limit: ${config.template.max_players}`);
      assert(config.template.entry.type === "free", `Entry must remain free: ${config.template.entry.type}`);
      assert(JSON.stringify(config.template.rewards.map(({ place, type, amount }) => ({ place, type, amount }))) ===
        JSON.stringify([
          { place: 1, type: "points", amount: 120 },
          { place: 2, type: "points", amount: 70 },
          { place: 3, type: "points", amount: 30 },
        ]), "Proposal rewards do not match the requested 120/70/30 points");
      return config;
    }, { timeout: 10 });
    const unchangedConfig = JSON.parse(JSON.stringify(initial.state.config));
    unchangedConfig.template.name = proposedConfig.template.name;
    unchangedConfig.template.max_players = proposedConfig.template.max_players;
    unchangedConfig.template.rewards = proposedConfig.template.rewards;
    if (JSON.stringify(proposedConfig) !== JSON.stringify(unchangedConfig)) {
      throw new Error("AI proposal changed fields outside the requested name, player limit, and rewards");
    }
    observed.proposedName = proposedConfig.template.name;
    observed.proposedMaxPlayers = proposedConfig.template.max_players;
    observed.proposedRewards = proposedConfig.template.rewards;
    await clickTournamentControl(tab, "#tab-template");
    await tab.waitForSelector('[id="f-template.max_players"]', { timeout: 10000 });
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector('[id="f-template.max_players"]')?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(10000);
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector('[data-path="template.rewards.0.amount"]')?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(8000);
    await clickTournamentControl(tab, "#save-button");
    await tab.waitForText("Сохранено", { timeout: 30000 });
    observed.savedConfig = await readTournamentState(tab, "configuration");
    if (observed.savedConfig.template.name !== "Вечерний кубок" || observed.savedConfig.template.max_players !== 8) {
      throw new Error("Saved configuration does not contain the accepted proposal values");
    }
    if (observed.savedConfig.template.rewards.map((reward) => reward.amount).join(",") !== "120,70,30") {
      throw new Error("Saved configuration does not contain the accepted rewards");
    }
    await hold(10000);

    // Scene 3 — expose recurrence and second-based registration offsets, preview, and select two dates.
    await clickTournamentControl(tab, "#tab-schedule");
    await tab.waitForSelector("#preview-button", { timeout: 10000 });
    const scheduleForm = await tab.run(async ({ page, assert }) => {
      const fields = await page.evaluate(() => ({
        rrule: document.querySelector("#rrule-raw")?.value || "",
        openOffset: document.querySelector('[id="f-schedule.registration_open_offset_seconds"]')?.value || "",
        closeOffset: document.querySelector('[id="f-schedule.registration_close_offset_seconds"]')?.value || "",
        dtstart: document.querySelector('[id="f-schedule.dtstart"]')?.value || "",
      }));
      assert(fields.rrule.includes("FREQ=") && /COUNT=\d+/.test(fields.rrule), `Expected bounded RRULE; got ${fields.rrule}`);
      assert(fields.rrule.includes("BYDAY="), `Expected weekly RRULE; got ${fields.rrule}`);
      assert(fields.openOffset === "-86400" && fields.closeOffset === "-3600",
        `Expected signed registration offsets in seconds; got ${fields.openOffset}/${fields.closeOffset}`);
      return fields;
    }, { timeout: 10 });
    observed.schedule = scheduleForm;
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector("#rrule-raw")?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(12000);
    await tab.click("#preview-button");
    await tab.waitForSelector('#preview-area input[data-occurrence="0"]', { timeout: 30000 });
    await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => ({
        occurrences: [...document.querySelectorAll("#preview-area input[data-occurrence]")].length,
        error: document.querySelector("#preview-area [role=alert]")?.textContent.trim() || "",
      }));
      assert(!result.error, `Preview error: ${result.error}`);
      assert(result.occurrences >= 2, `Expected at least two preview dates, found ${result.occurrences}`);
      await page.evaluate(() => document.querySelector("#preview-area .table-wrap")?.scrollIntoView({ block: "center", behavior: "smooth" }));
      return result;
    }, { timeout: 10 });
    await hold(9000);
    await tab.click('#preview-area input[data-occurrence="0"]');
    await tab.click('#preview-area input[data-occurrence="1"]');
    await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => ({
        selected: [...document.querySelectorAll("#preview-area input[data-occurrence]:checked")].length,
        createDisabled: document.querySelector("#create-button")?.disabled,
      }));
      assert(result.selected === 2 && result.createDisabled === false,
        `Expected exactly two selected dates and an enabled create action; observed ${JSON.stringify(result)}`);
      return result;
    }, { timeout: 10 });
    observed.selectedStartCount = 2;
    await hold(8000);

    // Scene 4 — manually create the selected two and inspect their immutable snapshots.
    await tab.click("#create-button");
    await tab.waitForText("Турниры созданы.", { timeout: 30000 });
    await tab.waitForSelector('#tournament-count', { timeout: 10000 });
    const created = await waitForTournamentCount(tab, 2);
    observed.createdTournamentCount = created.tournaments.length;
    await clickTournamentControl(tab, "#tab-tournaments");
    await tab.waitForSelector("#snapshot-detail .snapshot-json", { timeout: 10000 });
    await tab.click("button[data-tournament-id]");
    await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => ({
        template: document.querySelector("#snapshot-detail .snapshot-json")?.textContent || "",
        schedule: document.querySelector("#snapshot-detail .schedule-snapshot")?.textContent || "",
      }));
      assert(result.template.length > 2 && result.schedule.length > 2, "Tournament snapshots are not displayed");
      return result;
    }, { timeout: 10 });
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector("#snapshot-detail")?.scrollIntoView({ block: "start", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(14000);
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector("#snapshot-detail .schedule-snapshot")?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(10000);

    // Scene 5 — change and save the source template, then prove the selected snapshot stayed unchanged.
    await clickTournamentControl(tab, "#tab-template");
    await tab.waitForSelector('textarea[data-path="template.rounds.0.description"]', { timeout: 10000 });
    const changedDescription = "Раунд для проверки неизменности снимка после создания.";
    await tab.fill('textarea[data-path="template.rounds.0.description"]', changedDescription);
    await clickTournamentControl(tab, "#save-button");
    await tab.waitForText("Сохранено", { timeout: 30000 });
    observed.currentConfig = await readTournamentState(tab, "configuration");
    if (observed.currentConfig.template.rounds[0].description !== changedDescription) {
      throw new Error("Updated round description was not persisted");
    }
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector('textarea[data-path="template.rounds.0.description"]')?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(16000);
    await clickTournamentControl(tab, "#tab-tournaments");
    await tab.waitForSelector("#snapshot-detail .snapshot-json", { timeout: 10000 });
    await tab.click("button[data-tournament-id]");
    const finalEvidence = await tab.run(async ({ page, assert }) => {
      const result = await page.evaluate(() => ({
        templateText: document.querySelector("#snapshot-detail .snapshot-json")?.textContent || "",
        scheduleText: document.querySelector("#snapshot-detail .schedule-snapshot")?.textContent || "",
      }));
      assert(result.templateText && result.scheduleText, "Snapshot details disappeared after returning to tournaments");
      return {
        template: JSON.parse(result.templateText),
        schedule: JSON.parse(result.scheduleText),
      };
    }, { timeout: 10 });
    const originalRoundDescription = initial.state.config.template.rounds[0].description;
    if (finalEvidence.template.rounds[0].description !== originalRoundDescription) {
      throw new Error("Tournament template snapshot changed after editing the source template");
    }
    if (finalEvidence.template.rounds[0].description === changedDescription) {
      throw new Error("Tournament snapshot unexpectedly contains the edited round description");
    }
    const originalSchedule = initial.state.config.schedule;
    for (const field of ["dtstart", "timezone", "rrule", "registration_open_offset_seconds", "registration_close_offset_seconds"]) {
      if (finalEvidence.schedule[field] !== originalSchedule[field]) {
        throw new Error(`Tournament schedule snapshot changed at ${field}`);
      }
    }
    const serverState = await readTournamentState(tab, "full");
    if (serverState.tournaments.length !== 2) throw new Error(`Expected two saved tournaments; found ${serverState.tournaments.length}`);
    const selectedTournament = serverState.tournaments.find((tournament) =>
      JSON.stringify(tournament.template_snapshot) === JSON.stringify(finalEvidence.template));
    if (!selectedTournament) throw new Error("Displayed template snapshot could not be cross-checked against persisted state");
    observed.immutableSnapshot = {
      originalRoundDescription,
      changedDescription,
      snapshotRoundDescription: finalEvidence.template.rounds[0].description,
      originalScheduleOffsets: {
        registration_open_offset_seconds: originalSchedule.registration_open_offset_seconds,
        registration_close_offset_seconds: originalSchedule.registration_close_offset_seconds,
      },
      snapshotScheduleOffsets: {
        registration_open_offset_seconds: finalEvidence.schedule.registration_open_offset_seconds,
        registration_close_offset_seconds: finalEvidence.schedule.registration_close_offset_seconds,
      },
      currentRoundDescription: observed.currentConfig.template.rounds[0].description,
    };
    await tab.run(async ({ page }) => page.evaluate(() => {
      document.querySelector("#snapshot-detail .snapshot-json")?.scrollIntoView({ block: "center", behavior: "smooth" });
    }), { timeout: 10 });
    await hold(12000);
  } catch (error) {
    failure = error;
  } finally {
    if (recordingStarted) {
      try {
        const stopped = await tab.recordStop();
        recordingMetadata = stopped ?? await tab.recording();
      } catch (error) {
        failure ??= error;
      }
    }
    try {
      await tab.close();
    } catch (error) {
      failure ??= error;
    }
  }

  if (failure) throw failure;
  return { recordingMetadata, observed };
}

async function readTournamentState(tab, part) {
  return tab.run(async ({ page, assert }, requestedPart) => {
    const result = await page.evaluate(async (selection) => {
      const response = await fetch("/api/state");
      if (!response.ok) throw new Error(`State request failed (${response.status})`);
      const state = await response.json();
      if (selection === "configuration") return state.config;
      return state;
    }, requestedPart);
    assert(result, `Could not read persisted ${requestedPart}`);
    return result;
  }, { args: [part], timeout: 10 });
}

async function waitForTournamentCount(tab, expectedCount) {
  return tab.run(async ({ page, assert }, expected) => {
    await page.waitForFunction((count) => {
      const text = document.querySelector("#tournament-count")?.textContent.trim();
      return text === String(count);
    }, { timeout: 30000 }, expected);
    const state = await page.evaluate(async () => {
      const response = await fetch("/api/state");
      if (!response.ok) throw new Error(`State request failed (${response.status})`);
      return response.json();
    });
    assert(state.tournaments.length === expected, `Expected ${expected} persisted tournaments; found ${state.tournaments.length}`);
    return state;
  }, { args: [expectedCount], timeout: 35 });
}

async function pauseTournamentDemo(tab, milliseconds) {
  await tab.run(async ({ wait }, duration) => wait(duration), { args: [milliseconds], timeout: Math.ceil(milliseconds / 1000) + 5 });
}

async function clickTournamentControl(tab, selector) {
  await tab.run(async ({ page }, target) => page.evaluate((value) => {
    document.querySelector(value)?.scrollIntoView({ block: "center" });
  }, target), { args: [selector], timeout: 10 });
  await tab.click(selector);
}
