/**
 * Records the approved iRon support flow in a real browser tab.
 * The coordinator supplies the browser runtime and output path when invoking
 * this function through the browser eval environment.
 *
 * @param {object} browser Browser eval API.
 * @param {string} outputPath Destination MP4 path.
 * @returns {Promise<object>} Recorder metadata returned by the browser runtime.
 */
async function recordIronSupportDemo(browser, outputPath) {
  const tab = await browser.open({
    name: "iRon support demo",
    url: "http://127.0.0.1:8033",
    viewport: { width: 1600, height: 1100 },
  });
  let recordingStarted = false;
  let recordingMetadata;
  let failure;

  try {
    await tab.recordStart(outputPath);
    recordingStarted = true;

    // Start a new browser-session conversation without changing application data.
    await tab.evaluate("sessionStorage.clear(); location.reload();");
    await tab.waitForSelector("#user-select:not(:disabled)", { timeout: 60000 });
    await tab.waitForSelector('[data-ticket-id="IRON-101"]', { timeout: 60000 });
    await tab.waitForSelector("#message-input:not(:disabled)", { timeout: 60000 });
    await tab.waitForText("IRON-101", { timeout: 60000 });
    await tab.run(async ({ page, wait }) => {
      await wait(6000);
      await page.waitForFunction(() => {
        const selected = document.querySelector('[data-ticket-id="IRON-101"]');
        return selected?.getAttribute("aria-current") === "true" &&
          document.querySelector("#ticket-config")?.textContent.includes('"ghost": "off"');
      }, { timeout: 60000 });
    }, { timeout: 65 });

    await askAndWait(tab, "Почему не отображается пунктирный график Delta?");
    await pause(tab, 10000);
    await inspectEvidence(tab);
    await pause(tab, 8000);

    await tab.click('[data-ticket-id="IRON-102"]');
    await tab.waitForSelector("#message-input:not(:disabled)", { timeout: 60000 });
    await tab.run(async ({ page }) => {
      await page.waitForFunction(() =>
        document.querySelector('[data-ticket-id="IRON-102"]')?.getAttribute("aria-current") === "true" &&
        document.querySelector("#ticket-config")?.textContent.includes("Нет данных"),
      { timeout: 60000 });
    }, { timeout: 65 });
    await pause(tab, 4000);

    await askAndWait(tab, "Почему не отображается пунктирный график Delta?");
    await pause(tab, 10000);
    await askAndWait(tab, "В config.json стоит ghost: off. Как включить сравнение с последним кругом?");
    await pause(tab, 6000);
    await inspectEvidence(tab);
    await pause(tab, 2600);
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
  return recordingMetadata;
}

async function askAndWait(tab, question) {
  const previousAnswers = await tab.run(async ({ page }) =>
    page.evaluate(() => document.querySelectorAll("#conversation .message-row.assistant").length),
  { timeout: 10 });
  await tab.fill("#message-input", question);
  await tab.click("#send-button");
  await tab.run(async ({ page }, previousCount) => {
    await page.waitForFunction((count) => {
      const error = document.querySelector("#error-message");
      if (error && !error.hidden && error.textContent.trim()) {
        throw new Error(`Ошибка приложения: ${error.textContent.trim()}`);
      }
      const answers = document.querySelectorAll("#conversation .message-row.assistant");
      const input = document.querySelector("#message-input");
      return answers.length > count && input && !input.disabled &&
        !document.querySelector("#stream-status")?.textContent.trim();
    }, { timeout: 60000 }, previousCount);
  }, { args: [previousAnswers], timeout: 65 });
  await tab.run(async ({ page, assert }, previousCount) => {
    const state = await page.evaluate((count) => ({
      error: document.querySelector("#error-message:not([hidden])")?.textContent.trim() || "",
      answerCount: document.querySelectorAll("#conversation .message-row.assistant").length,
    }), previousCount);
    assert(!state.error, `Support UI reported an error: ${state.error}`);
    assert(state.answerCount > previousCount, "A new assistant answer was not rendered");
    await page.evaluate(() => {
      document.querySelector("#conversation .message-row.assistant:last-of-type")?.scrollIntoView({ block: "center", behavior: "smooth" });
    });
  }, { args: [previousAnswers], timeout: 10 });
}

async function inspectEvidence(tab) {
  await tab.run(async ({ page }) => {
    await page.evaluate(() => {
      for (const selector of ["#context-panel", "#sources-details", "#observations-details"]) {
        const details = document.querySelector(selector);
        if (details && !details.open) details.querySelector(":scope > summary")?.click();
      }
      document.querySelector("#sources-details")?.scrollIntoView({ block: "start", behavior: "smooth" });
    });
  }, { timeout: 10 });
  await tab.run(async ({ page, wait }) => {
    await wait(500);
    await page.evaluate(() => {
      for (const card of document.querySelectorAll("#sources-list .source-card")) {
        if (!card.open) card.querySelector(":scope > summary")?.click();
      }
    });
    await wait(8000);
    await page.evaluate(() => {
      document.querySelector("#observations-details")?.scrollIntoView({ block: "end", behavior: "smooth" });
    });
    await wait(8000);
  }, { timeout: 20 });
}
async function pause(tab, milliseconds) {
  await new Promise((resolve) => setTimeout(resolve, milliseconds));
}

