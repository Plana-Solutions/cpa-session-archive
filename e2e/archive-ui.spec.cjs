const { test, expect } = require("@playwright/test");

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      "cli-proxy-auth",
      JSON.stringify({
        state: {
          apiBase: "http://127.0.0.1:4173",
          managementKey: "mock-management-key",
        },
      }),
    );
  });
  await page.goto("/#/sessions");
  await expect(page.locator(".key-pill").first()).toHaveText("长文本测试 Key");
});

test("long previews remain lightweight while turn details expose full content", async ({
  page,
}) => {
  await page.locator("#filterSidebar > summary").click();
  await expect(page.getByText("项目与代码库")).toBeVisible();
  await page.locator(".filter-panel").getByRole("button", {name: "关闭", exact: true}).click();
  await expect(page.locator(".sessions-panel .conversation-meta").first()).toContainText(
    "mock-long-project",
  );

  await page.locator(".session-row").first().click();
  await expect(page.locator(".turn-card")).toBeVisible();
  await expect(page.locator(".turn-card")).not.toContainText(
    "END-OF-COMPLETE-USER-COMMAND",
  );

  await page.locator(".turn-card").click();
  await expect(page).toHaveURL(/\/turns\/mock-turn-long-content$/);
  const primary = page.locator("#turnPrimary");
  await expect(primary).not.toContainText("END-OF-COMPLETE-USER-COMMAND");
  await primary
    .getByRole("button", { name: "展开完整内容" })
    .first()
    .click();
  await expect(primary).toContainText("END-OF-COMPLETE-USER-COMMAND");
  await primary
    .locator(".turn-final")
    .getByRole("button", { name: "展开完整内容" })
    .click();
  await expect(primary).toContainText("END-OF-COMPLETE-ASSISTANT-ANSWER");

  await page
    .locator("#turnPagination")
    .getByRole("button", { name: "下一页" })
    .click();
  await expect(page.locator("#turnPageInfo")).toContainText("第 2 / 5 页");
  await page
    .locator("#turnPagination")
    .getByRole("button", { name: "上一页" })
    .click();
  await expect(page.locator("#turnPageInfo")).toContainText("第 1 / 5 页");

  await page.locator(".process-step").first().click();
  await expect(page.getByText("shell_command · 查看完整调用")).toBeVisible();
  await page.getByText("shell_command · 查看完整调用").click();
  await expect(page).toHaveURL(/\/tools\/call-long-output$/);
  await expect(page.locator("#subDetail")).not.toContainText(
    "END-OF-LONG-TOOL-OUTPUT",
  );
  await page
    .locator("#subDetail .tool-card")
    .last()
    .getByRole("button", { name: "展开完整内容" })
    .click();
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-LONG-TOOL-OUTPUT",
  );
});

test("system instructions and diagnostics are routed and loaded on demand", async ({
  page,
}) => {
  await page.locator(".session-row").first().click();
  await page.locator(".turn-card").click();
  await page.locator(".process-step").first().click();
  await page.getByRole("button", { name: "原始请求/响应（排障）" }).click();
  await expect(page).toHaveURL(/\/requests\/mock-request-long-content$/);

  await page
    .getByRole("button", { name: /展开系统指令.*17730 字符/ })
    .click();
  await expect(page).toHaveURL(/\/raw\/instructions$/);
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-SYSTEM-INSTRUCTIONS",
  );
  await page.getByRole("button", { name: /返回本次请求/ }).click();
  await expect(page).toHaveURL(/\/requests\/mock-request-long-content$/);
  await page.getByRole("button", { name: "查看并复制" }).first().click();
  await expect(page).toHaveURL(/\/raw\/request$/);
  await expect(page.locator("#subDetail")).toContainText(
    "END-OF-COMPLETE-USER-COMMAND",
  );
});

test("session export exposes a reusable server-streamed JSONL download", async ({
  page,
}) => {
  await page.locator(".session-row").first().click();
  const sessionDownload = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载当前 Session" }).click();
  const downloaded = await sessionDownload;
  expect(downloaded.suggestedFilename()).toBe(
    "mock-session-long-content.archive.jsonl",
  );
  const downloadedPath = await downloaded.path();
  expect(downloadedPath).toBeTruthy();
  expect(require("node:fs").statSync(downloadedPath).size).toBeGreaterThan(0);
  expect(require("node:fs").readFileSync(downloadedPath, "utf8")).toContain(
    "END-OF-LONG-TOOL-OUTPUT",
  );
  const link = page.locator("#downloadNotice a");
  await expect(link).toHaveAttribute(
    "href",
    "http://127.0.0.1:4173/archive-api/v1/exports/mock-ticket",
  );
  await expect(link).toHaveText("mock-session-long-content.archive.jsonl");

  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await page.locator(".archive-actions > summary").click();
  await page.locator("#allExportFormat").selectOption("sft");
  const allDownload = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载全库" }).click();
  expect((await allDownload).suggestedFilename()).toBe(
    "mock-session-long-content.archive.jsonl",
  );
  await expect(page.locator("#allDownloadNotice a")).toHaveText(
    "cpa-session-archive.all.sft.jsonl",
  );
});

test("escaped environment context is readable and process content loads lazily", async ({
  page,
}) => {
  await expect(page.locator(".session-row").first()).toContainText(
    'calibre 路径 "C:\\Books" 应显示为正常引号',
  );
  await page.locator(".session-row").first().click();
  await expect(page.locator(".turn-card").first()).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
  await page.locator(".turn-card").click();
  const step = page.locator(".process-step").first();
  await expect(step).toContainText("展开后加载完整过程正文");
  await step.click();
  await expect(step).not.toContainText("没有可直接阅读的文本内容");
  await step.getByRole("button", { name: "原始请求/响应（排障）" }).click();
  await expect(page.locator("#requestTitle")).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
  await expect(page.locator("#requestDetail")).toContainText(
    "<environment_context>mock cwd</environment_context>",
  );
});

test("facets, i18n, deep links, and back navigation remain interactive", async ({
  page,
}) => {
  const sidebar = page.locator("#filterSidebar");
  await expect(sidebar).not.toHaveAttribute("open", "");
  await sidebar.locator(":scope > summary").click();
  await expect(sidebar).toHaveAttribute("open", "");

  await page.locator("#facetSearch").fill("project");
  await expect(page.locator('[data-filter-name*="project"]').first()).toBeVisible();
  await page.locator("#facetSearch").fill("");
  await page.locator('[data-facet="project.name"]').selectOption(
    "mock-long-project",
  );
  await expect(page.locator("#activeCount")).toHaveText("0");
  await page.getByRole("button", { name: "应用筛选" }).click();
  await expect(page.locator("#activeCount")).toHaveText("1");
  await expect(page.locator(".session-row")).toHaveCount(1);
  await expect(page.locator("[data-remove-facet]")).toContainText("mock-long-project");
  await page.locator("[data-remove-facet]").click();
  await expect(page.locator("#activeCount")).toHaveText("0");

  await page.evaluate(() => {
    document.documentElement.lang = "en";
  });
  await expect(page.getByRole("heading", { name: "Sessions", exact: true })).toBeVisible();
  await page.evaluate(() => {
    document.documentElement.lang = "zh-CN";
  });
  await expect(page.getByRole("heading", { name: "会话", exact: true })).toBeVisible();

  await page.goto(
    "/#/sessions/mock-session-long-content/turns/mock-turn-long-content",
  );
  await expect(page.getByRole("button", { name: /返回当前会话/ })).toBeVisible();
  await page.getByRole("button", { name: /返回当前会话/ }).click();
  await expect(page).toHaveURL(/\/sessions\/mock-session-long-content$/);
  await page.getByRole("button", { name: /返回会话列表/ }).click();
  await expect(page).toHaveURL(/#\/sessions$/);
  await expect(page.locator("#listView")).toBeVisible();
});

test("session pagination and loading SQL remain visible and operable", async ({
  page,
}) => {
  let delayed = false;
  await page.route("**/cpa-session-archive/turns?**", async (route) => {
    if (!delayed) {
      delayed = true;
      await new Promise((resolve) => setTimeout(resolve, 600));
    }
    await route.continue();
  });
  await page.locator(".session-row").first().click();
  await expect(page.locator("#loading")).toHaveClass(/open/);
  await expect(page.locator("#loadingSQL")).toContainText(
    "SELECT request_id, key_id, summary",
  );
  await expect(page.locator(".turn-card")).toBeVisible();
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");

  await page.getByRole("button", { name: "末页" }).click();
  await expect(page.locator("#pageInfo")).toContainText("第 3 / 3 页");
  await page.getByRole("button", { name: "首页" }).click();
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");
  await page.locator("#pageJump").fill("2");
  await page.locator("#pageJump").press("Enter");
  await expect(page.locator("#pageInfo")).toContainText("第 2 / 3 页");
  await page.locator("#sessionOrder").selectOption("asc");
  await expect(page.locator("#pageInfo")).toContainText("第 1 / 3 页");
});

for (const view of ["session", "turn", "process", "request"]) {
  test(`assistant markdown is safe in the ${view} view`, async ({ page }) => {
    const dialogs = [];
    page.on("dialog", async dialog => { dialogs.push(dialog.message()); await dialog.dismiss(); });
    await page.locator(".session-row").first().click();
    if (view !== "session") await page.locator(".turn-card").first().click();
    if (view === "process" || view === "request") await page.locator(".process-step").first().click();
    if (view === "request") await page.getByRole("button", {name: "原始请求/响应（排障）"}).click();
    const container = page.locator(view === "session" ? "#detail" : view === "turn" ? "#turnPrimary" : view === "process" ? ".process-step-body" : "#requestDetail");
    await expect(container.locator("strong").first()).toHaveText("bold");
    await expect(container).toContainText("<img src=x onerror=alert(1)>");
    await expect(container.locator("img")).toHaveCount(0);
    await expect(container.locator("script")).toHaveCount(0);
    expect(dialogs).toEqual([]);
  });
}

test("search filters only the loaded sessions without a network query", async ({page}) => {
  await expect(page.locator(".session-row")).toHaveCount(2);
  const requests = [];
  page.on("request", request => requests.push(request.url()));
  await page.locator("#sessionSearch").fill("mock-long-project");
  await expect(page.locator(".session-row")).toHaveCount(1);
  await expect(page.locator("#resultCount")).toContainText("1 / 2");
  await page.locator("#sessionSearch").fill("no matching session");
  await expect(page.locator(".session-row")).toHaveCount(0);
  await page.locator("#sessionSearch").fill("");
  await expect(page.locator(".session-row")).toHaveCount(2);
  expect(requests.filter(url => url.includes("/sessions?"))).toEqual([]);
});

for (const width of [390, 1280]) {
test(`sessions and session detail fit a ${width}px viewport`, async ({page}) => {
  await page.setViewportSize({width, height: 844});
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.locator("#filterSidebar > summary").click();
  await expect(page.locator(".filter-panel")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.keyboard.press("Escape");
  await page.locator(".session-row").first().click();
  await expect(page.locator(".turn-card")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});
}

test("markdown preserves prose structure, safe links and literal code after expansion", async ({page}) => {
  await page.locator(".session-row").first().click();
  await page.locator(".turn-card").click();
  const answer = page.locator("#turnPrimary .turn-final");
  await expect(answer.locator("h2")).toHaveText("Result");
  await expect(answer.locator("em")).toHaveText("italic");
  await expect(answer.locator("ul li")).toHaveText(["first item", "second item"]);
  await expect(answer.locator("ol li")).toHaveText(["ordered item"]);
  await expect(answer.locator("a")).toHaveCount(1);
  await expect(answer.locator("a")).toHaveAttribute("href", "https://example.com/?q=a%26b");
  await expect(answer).toContainText("literal a%26b");
  await expect(answer.locator("a")).toHaveAttribute("rel", "noopener noreferrer");
  await expect(answer.locator("pre code")).toContainText('<script>alert(1)</script>');
  await expect(answer.locator("script")).toHaveCount(0);
  await answer.getByRole("button", {name:"展开完整内容"}).click();
  await expect(answer.locator(".full-content strong")).toHaveText("bold");
  await expect(answer.locator(".full-content")).toContainText("END-OF-COMPLETE-ASSISTANT-ANSWER");
});

test("unapplied filter choices never become chips or queries", async ({ page }) => {
  const queries = [];
  page.on("request", request => { if (request.url().includes("/sessions?")) queries.push(request.url()); });
  const panel = page.locator("#filterSidebar");
  const select = page.locator('[data-facet="project.name"]');
  const value = "mock-long-project";
  await panel.locator(":scope > summary").click();
  await page.locator("#facetSearch").fill("project");
  await select.selectOption(value);
  await page.locator("#limit").fill("1");
  await page.keyboard.press("Escape");
  await expect(page.locator("#limit")).toHaveValue("100");
  await expect(page.locator("#activeFilters .chip")).toHaveCount(0);
  await expect(select).toHaveValue("");
  await page.locator(".session-row").first().click();
  await page.goBack();
  await expect(page.locator(".session-row").first()).toBeVisible();
  const facet = await select.getAttribute("data-facet");
  expect(queries.some(url => new URL(url).searchParams.has(facet))).toBe(false);
  expect(queries.some(url => new URL(url).searchParams.get("limit") === "1")).toBe(false);
  await panel.locator(":scope > summary").click();
  await page.locator("#facetSearch").fill("project");
  await select.selectOption(value);
  await page.locator(".filter-panel button.primary").click();
  await expect(page.locator("#activeFilters .chip")).toHaveCount(1);
  await page.locator("#activeFilters .chip").click();
  await expect(page.locator("#activeFilters .chip")).toHaveCount(0);
  await expect(select).toHaveValue("");
});

test("markdown link boundaries and inline handler ids survive hostile input", async ({ page }) => {
  const result = await page.evaluate(() => {
    const host = document.createElement("div");
    host.innerHTML = renderMarkdown("([docs](https://example.com/x)) and **[bold](https://example.com/b)** and [wiki](https://example.com/a(b))");
    const id = "turn's-(id)*!";
    return {
      hrefs: [...host.querySelectorAll("a")].map(a => a.getAttribute("href")),
      boldLink: !!host.querySelector("strong a"),
      encoded: jsId(id),
      roundTrip: decodeURIComponent(jsId(id)) === id,
    };
  });
  expect(result.hrefs).toEqual(["https://example.com/x", "https://example.com/b", "https://example.com/a(b)"]);
  expect(result.boldLink).toBe(true);
  expect(result.encoded).not.toMatch(/['()*!]/);
  expect(result.roundTrip).toBe(true);
});

test("the row limit only changes on a valid applied value", async ({ page }) => {
  const summary = page.locator("#filterSidebar > summary");
  await summary.click();
  await page.locator("#limit").fill("1");
  await summary.click();
  await summary.click();
  await expect(page.locator("#limit")).toHaveValue("100");
  const applied = page.waitForRequest(request => request.url().includes("/sessions?"));
  await page.locator("#limit").fill("1e2");
  await page.locator(".filter-panel button.primary").click();
  expect(new URL((await applied).url()).searchParams.get("limit")).toBe("100");
});

test("a late session page cannot wipe the open turn's expandable text", async ({ page }) => {
  await page.route(/\/turns\?.*order=/, async route => {
    await new Promise(resolve => setTimeout(resolve, 1500));
    await route.continue();
  });
  await page.goto("/#/sessions/mock-session-long-content");
  await page.goto("/#/sessions/mock-session-long-content/turns/mock-turn-long-content");
  const answer = page.locator("#turnPrimary .turn-final");
  await expect(answer).toBeVisible();
  await page.waitForTimeout(2000);
  await answer.getByRole("button", { name: "展开完整内容" }).click();
  await expect(answer).toContainText("END-OF-COMPLETE-ASSISTANT-ANSWER");
});
