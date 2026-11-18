const { test, expect } = require("@playwright/test");

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("cli-proxy-auth", JSON.stringify({
      state: { apiBase: "http://127.0.0.1:4173", managementKey: "mock-management-key" },
    }));
  });
  await page.goto("/#/");
});

test("insights is the default route and window changes refetch the fleet", async ({ page }) => {
  await expect(page.locator("#insightsView")).toBeVisible();
  await expect(page.locator("#listView")).toBeHidden();
  await expect(page.locator("#insightsTab")).toHaveAttribute("aria-current", "page");
  await expect(page.locator("#insightsView .kpi-number")).toHaveText(["33,020", "1,099", "4,384", "6"]);
  await expect(page.locator(".metric-chart")).toHaveCount(6);
  const claude = page.locator('[data-metric="requests"] [data-model="claude-opus-5-5"]');
  await expect(claude).toHaveClass(/provider-anthropic/);
  await expect(claude.locator(".model-bar")).toHaveCSS("background-color", "rgb(217, 119, 87)");
  await expect(page.locator(".daily-chart rect")).toHaveCount(120);
  await expect(page.locator('[data-metric="requests"] [data-model="claude-sonnet-5-5"]')).toBeVisible();
  await expect(page.locator('[data-metric="steps_per_turn"] [data-model="claude-sonnet-5-5"]')).toHaveCount(0);
  const refetch = page.waitForRequest(request => {
    const url = new URL(request.url());
    return url.pathname.endsWith("/insights") && url.searchParams.get("days") === "7";
  });
  await page.locator("#insightsDays").selectOption("7");
  await refetch;
  await expect(page.locator(".daily-chart rect")).toHaveCount(28);
  await expect(page.locator("#insightsView .kpi-number").first()).toHaveText("7,703");
  await page.evaluate(() => { document.documentElement.lang = "en"; });
  await expect(page.getByRole("heading", { name: "How the fleet behaves" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "How each model behaves" })).toBeVisible();
  await page.locator('[data-metric="requests"] [data-model="gpt-6.1-sol"]').click();
  await expect(page).toHaveURL(/#\/sessions$/);
  await expect(page.locator('[data-facet="model.resolved"]')).toHaveValue("gpt-6.1-sol");
  await expect(page.locator("#listView")).toBeVisible();
});

test("mobile insights stays within the viewport", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload();
  await expect(page.locator(".metric-chart")).toHaveCount(6);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect(page.locator("#insightsView")).toHaveCSS("background-color", "rgba(0, 0, 0, 0)");
  await expect(page.locator("body")).toHaveCSS("background-color", "rgb(0, 0, 0)");
  await page.locator("#sessionsTab").click();
  await expect(page.locator(".key-pill").first()).toBeVisible();
  const filters = page.locator("#filterSidebar");
  await expect(filters).not.toHaveAttribute("open", "");
  await filters.locator(":scope > summary").click();
  await expect(filters).toHaveAttribute("open", "");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("leaving a slow insights load does not yank the sessions view", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 500 });
  await expect(page.locator(".metric-chart")).toHaveCount(6);
  await page.locator("#sessionsTab").click();
  await expect(page.locator(".key-pill").first()).toBeVisible();
  await page.route("**/plugins/cpa-session-archive/insights**", async route => {
    await new Promise(resolve => setTimeout(resolve, 500));
    await route.continue();
  });
  await page.evaluate(() => { document.getElementById("insightsDays").value = "90"; });
  await page.locator("#insightsTab").click();
  await expect(page.locator(".insights-status")).toBeVisible();
  await page.locator("#sessionsTab").click();
  await expect(page.locator(".key-pill").first()).toBeVisible();
  await page.locator("#filterSidebar > summary").click();
  await page.evaluate(() => window.scrollTo(0, 300));
  await page.waitForTimeout(2600);
  expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
  await expect(page.locator("#insightsView")).toBeHidden();
});

test("leaving a cold initial insights load does not yank the sessions view", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 500 });
  await page.route("**/plugins/cpa-session-archive/insights**", async route => {
    await new Promise(resolve => setTimeout(resolve, 500));
    await route.continue();
  });
  await page.reload();
  await expect(page.locator(".insights-status")).toBeVisible();
  await page.locator("#sessionsTab").click();
  await expect(page.locator(".key-pill").first()).toBeVisible();
  await page.locator("#filterSidebar > summary").click();
  await page.evaluate(() => window.scrollTo(0, 300));
  await page.waitForTimeout(2600);
  expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0);
});

test("custom UTC dates refetch insights and fit on phones", async ({ page }) => {
  await page.setViewportSize({width: 390, height: 844});
  await page.locator("#insightsDays").selectOption("custom");
  await page.locator("#insightsFrom").fill("2026-11-01");
  await page.locator("#insightsTo").fill("2026-11-03");
  const request = page.waitForRequest(r => r.url().includes("from=2026-11-01") && r.url().includes("to=2026-11-03"));
  await page.locator("#insightsRange button").click();
  await request;
  await expect(page.locator(".daily-chart rect")).toHaveCount(12);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
});

test("imported sessions filter and open alongside proxy conversations", async ({page}) => {
  await page.locator("#sessionsTab").click();
  await page.locator("#filterSidebar > summary").click();
  await page.locator(".facet-group").filter({has: page.locator('[data-facet="client"]')}).locator(":scope > summary").click();
  await page.locator('[data-facet="client"]').selectOption("claude-import");
  await page.locator(".filter-panel .primary").click();
  await expect(page.locator(".session-row")).toHaveCount(1);
  await expect(page.locator(".session-row")).toContainText("Imported native transcript");
  await expect(page.locator(".external-import")).toBeVisible();
  await page.locator(".session-row").click();
  await expect(page.locator(".turn-card")).toContainText("Native prompt");
  await page.locator(".turn-card").click();
  await expect(page.locator("#turnPrimary")).toContainText("Native answer");
});
