const { test, expect } = require('@playwright/test');

function encodeV2(value, host) {
  const data = Buffer.from(value, 'utf8');
  const key = Buffer.from(`cli-proxy-api-webui::secure-storage|v2|${host}`, 'utf8');
  const out = Buffer.alloc(data.length);
  for (let i = 0; i < data.length; i++) out[i] = data[i] ^ key[i % key.length];
  return 'enc::v2::' + out.toString('base64');
}

test('auto-authenticates with CPAMP enc::v2:: cli-proxy-auth and renders Insights without extra login', async ({
  page,
  baseURL,
}) => {
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (msg) => {
    if (msg.type() === 'error') errors.push(msg.text());
  });

  const host = new URL(baseURL).host;
  const payload = JSON.stringify({
    state: { apiBase: baseURL, managementKey: 'mock-management-key', rememberPassword: true },
  });
  const encoded = encodeV2(payload, host);

  await page.addInitScript((val) => {
    localStorage.setItem('cli-proxy-auth', val);
  }, encoded);

  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Fleet Insights' })).toBeVisible();
  await expect(page.getByText('Connect to CPAMP')).toHaveCount(0);
  await expect(page.locator('[data-metric]')).toHaveCount(6);
  await expect(page.locator('[data-metric="requests"] [data-model="claude-opus-5-5"]')).toBeVisible();

  // Clicking a model drills down into Sessions pre-filtered by that model
  await page.locator('[data-metric="requests"] [data-model="gpt-6.1-sol"]').click();
  await expect(page).toHaveURL(/#\/sessions$/);
  await expect(page.getByRole('heading', { name: 'Sessions' })).toBeVisible();
  await expect(page.getByText('gpt-6.1-sol').first()).toBeVisible();

  expect(errors).toEqual([]);
});

test('auto-authenticates with legacy enc::v1:: cli-proxy-auth', async ({ page, baseURL }) => {
  const host = new URL(baseURL).host;
  await page.addInitScript(
    ({ base, hostName }) => {
      const raw = JSON.stringify({ state: { apiBase: base, managementKey: 'mock-management-key' } });
      const data = new TextEncoder().encode(raw);
      const key = new TextEncoder().encode(
        `cli-proxy-api-webui::secure-storage|${hostName}|${navigator.userAgent}`
      );
      const out = new Uint8Array(data.length);
      for (let i = 0; i < data.length; i++) out[i] = data[i] ^ key[i % key.length];
      let bin = '';
      for (let i = 0; i < out.length; i++) bin += String.fromCharCode(out[i]);
      localStorage.setItem('cli-proxy-auth', 'enc::v1::' + btoa(bin));
    },
    { base: baseURL, hostName: host }
  );

  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Fleet Insights' })).toBeVisible();
});

test('manual login works when no CPAMP auth is present', async ({ page, baseURL }) => {
  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Connect to CPAMP' })).toBeVisible();
  await page.getByPlaceholder('https://cpamp.planasolutions.ai').fill(baseURL);
  await page.getByPlaceholder('Enter admin key').fill('mock-management-key');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Fleet Insights' })).toBeVisible();
});

test('sessions browser, native imports, conversation reader, process steps, decoded raw JSON, and mobile fit', async ({
  page,
  baseURL,
}) => {
  const errors = [];
  const dialogs = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('dialog', (d) => {
    dialogs.push(d.message());
    d.dismiss();
  });

  await page.addInitScript((base) => {
    localStorage.setItem(
      'cli-proxy-auth',
      JSON.stringify({ state: { apiBase: base, managementKey: 'mock-management-key' } })
    );
  }, baseURL);

  await page.goto('/#/sessions');
  const proxySessionBtn = page.getByRole('button', { name: /mock-long-project/ }).first();
  const nativeSessionBtn = page.getByRole('button', { name: /Imported native transcript/ });

  await expect(proxySessionBtn).toBeVisible();
  await expect(nativeSessionBtn).toBeVisible();
  await expect(page.getByText('Native', { exact: true })).toBeVisible();
  await expect(page.getByText('长文本测试 Key')).toBeVisible();

  // Open proxy session and verify prompt expansion, process steps, tool output, and decoded raw JSON
  await proxySessionBtn.click();
  await expect(page).toHaveURL(/#\/sessions\/mock-session-long-content$/);
  await expect(page.getByText(/END-OF-COMPLETE-ASSISTANT-ANSWER/)).toBeVisible();
  expect(dialogs).toEqual([]);

  await page.getByRole('button', { name: 'Show full prompt' }).click();
  await expect(page.getByText(/END-OF-COMPLETE-USER-COMMAND/)).toBeVisible();

  await page.getByRole('button', { name: /Process & tools/ }).click();
  await expect(page.getByRole('button', { name: 'Load full step messages' }).first()).toBeVisible();
  await page.getByRole('button', { name: 'Load full step messages' }).first().click();

  await expect(page.getByRole('button', { name: 'Load tool output' }).first()).toBeVisible();
  await page.getByRole('button', { name: 'Load tool output' }).first().click();
  await expect(page.getByText(/END-OF-LONG-TOOL-OUTPUT/)).toBeVisible();

  await page.getByRole('button', { name: 'Raw JSON' }).click();
  await expect(page.getByText(/"gpt-5\.6-sol"/)).toBeVisible();
  await page.getByRole('button', { name: 'Request', exact: true }).click();
  await expect(page.getByText(/"shell_command"/)).toBeVisible();
  await page.getByRole('button', { name: 'Close' }).click();

  // Back to sessions and open native imported session
  await page.getByRole('button', { name: 'Back to Sessions' }).click();
  await nativeSessionBtn.click();
  await expect(page.getByText('Native prompt')).toBeVisible();
  await expect(page.getByText('Native answer')).toBeVisible();

  // Mobile layout check
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/#/insights');
  await expect(page.getByRole('heading', { name: 'Fleet Insights' })).toBeVisible();
  const fits = await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth);
  expect(fits).toBe(true);
  expect(errors).toEqual([]);
});
