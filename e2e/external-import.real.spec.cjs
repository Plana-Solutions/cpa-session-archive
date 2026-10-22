const { test, expect } = require("@playwright/test");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const crypto = require("node:crypto");
const { execFileSync } = require("node:child_process");

const port = Number(process.env.E2E_REAL_PORT || 4174);
const apiBase = "/v0/management/plugins/cpa-session-archive";
const headers = { Authorization: "Bearer fixture-management-key" };
let state;

test.beforeAll(() => {
  state = JSON.parse(fs.readFileSync(path.join(os.tmpdir(), `cpa-real-e2e-${port}.json`), "utf8"));
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("cli-proxy-auth", JSON.stringify({ state: {
      apiBase: location.origin,
      managementKey: "fixture-management-key",
    } }));
  });
  await page.goto("/v0/resource/plugins/cpa-session-archive/index.html#/sessions");
  await page.evaluate(() => { document.documentElement.lang = "en"; });
  await expect(page.locator("#rows tr")).toHaveCount(2);
});

test("real imported sessions retain provenance, native events and exact originals", async ({ page, request }) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await expect(page.locator(".kind-badge.external-import")).toHaveCount(2);
  await page.locator('[data-facet="source.kind"]').selectOption("external-local-history");
  await page.locator('[data-facet="source.tool"]').selectOption("codex");
  await page.getByRole("button", { name: "Apply", exact: true }).click();
  await expect(page.locator("#rows tr")).toHaveCount(1);
  await expect(page.locator("#rows")).toContainText("Archive fixture request");
  await page.locator("#rows tr").click();
  const sources = page.locator("#externalSources");
  await expect(sources).toContainText("Original files (1)");
  await sources.locator("details.external-sources > summary").click();
  await sources.locator(".external-source > summary").click();
  const source = state.sources.find((item) => item.tool === "codex");
  await expect(sources).toContainText("clawdshome@m1");
  await expect(sources).toContainText(source.sha256);
  const downloaded = page.waitForEvent("download");
  await sources.locator("button[data-source-id]").click();
  const download = await downloaded;
  const bytes = fs.readFileSync(await download.path());
  expect(crypto.createHash("sha256").update(bytes).digest("hex")).toBe(source.sha256);
  expect(bytes.equals(fs.readFileSync(source.source))).toBe(true);
  expect(bytes.toString()).toContain('"encrypted_content":"fixture-opaque-exact-bytes"');

  await page.locator(".turn-card").first().click();
  await expect(page.locator("#turnPrimary")).toContainText("Archive fixture request");
  const steps = page.locator(".process-step");
  for (let index = 0; index < await steps.count(); index++) {
    await steps.nth(index).click();
  }
  await expect(page.locator(".native-event").first()).toBeVisible();
  await expect(page.locator("#turnProcess")).toContainText("fixture tool output");
  await expect(page.locator("#turnProcess")).toContainText("shell_command");
  await expect(page.locator(".native-event-meta").first()).toContainText("2026");

  const sessions = await (await request.get(apiBase + "/sessions", { headers })).json();
  expect(sessions).toHaveLength(2);
  expect(sessions.find((item) => item.source_tool === "codex")).toMatchObject({
    source_kind: "external-local-history", source_host: "m1", source_account: "clawdshome",
  });
  expect(errors).toEqual([]);
});

test("CLI replay against the running real archive leaves counts unchanged", async ({ request }) => {
  const before = await (await request.get(apiBase + "/stats", { headers })).json();
  for (const source of state.sources) {
    expect(source.first.receipt.source.sha256).toBe(source.sha256);
    expect(source.first.receipt.imported_events).toBeGreaterThan(0);
    expect(source.replay.receipt.replayed).toBe(true);
    expect(source.replay.receipt.imported_events).toBe(0);
    execFileSync(state.importer, [
      "--db", state.db, "--source", source.source,
      "--host", "m1", "--account", source.account, "--tool", source.tool,
    ], { encoding: "utf8" });
  }
  const after = await (await request.get(apiBase + "/stats", { headers })).json();
  expect(after.records).toBe(before.records);
  expect(after.sessions).toBe(before.sessions);
  expect(after.sessions).toBe(2);
});
