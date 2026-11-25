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

test('remains cleanly dark-styled when CPAMP injects its unlayered light host stylesheet', async ({
  page,
  baseURL,
}) => {
  await page.addInitScript((base) => {
    localStorage.setItem(
      'cli-proxy-auth',
      JSON.stringify({ state: { apiBase: base, managementKey: 'mock-management-key' } })
    );
  }, baseURL);

  await page.goto('/#/');
  await expect(page.getByRole('heading', { name: 'Fleet Insights' })).toBeVisible();

  // Mirror CPAMP management.html's zvt()/Ivt('white') host injection inside the plugin iframe:
  // attributes on <html> + an unlayered <style id="cpamp-plugin-host-style"> inserted BEFORE our <style>.
  await page.evaluate(() => {
    const html = document.documentElement;
    html.setAttribute('data-cpamp-plugin-host', 'true');
    html.setAttribute('data-theme', 'white');
    html.classList.add('cpamp-plugin-host', 'theme-light');

    const O9 = ":where(html[data-cpamp-plugin-host='true'])";
    const style = document.createElement('style');
    style.id = 'cpamp-plugin-host-style';
    style.setAttribute('data-cpamp-plugin-host', 'true');
    style.textContent = `
      ${O9} {
        --bg-primary: #ffffff;
        --bg-tertiary: #e4e4e7;
        --bg-hover: #dbeafe;
        --app-surface-muted: #f6faff;
        --text-primary: #1f2937;
        --text-secondary: #4b5563;
        --border-color: #d1d5db;
        --border-hover: rgba(64, 158, 255, 0.28);
        --primary-color: #409eff;
        --primary-hover: #79bbff;
        --primary-active: #337ecc;
        --primary-contrast: #ffffff;
        --focus-inset: rgba(64, 158, 255, 0.22);
        color-scheme: light;
        background: var(--bg-primary);
        color: var(--text-primary);
      }
      ${O9} :where(body) {
        background: var(--bg-primary);
        color: var(--text-primary);
        font-size: 14px;
      }
      ${O9} :where(a) { color: var(--primary-color); }
      ${O9} :where(a:hover) { color: var(--primary-active); }
      ${O9} :where(h1, h2, h3, h4, h5, h6) { color: var(--text-primary); letter-spacing: 0; }
      ${O9} :where(button, input[type='button'], input[type='reset'], input[type='submit'], [role='button']) {
        min-height: 34px;
        border: 1px solid var(--border-color);
        border-radius: 12px;
        background: var(--app-surface-muted, var(--bg-tertiary));
        color: var(--text-primary);
        font-weight: 600;
        line-height: 1.2;
      }
      ${O9} :where(button:hover:not(:disabled), [role='button']:hover:not([aria-disabled='true'])) {
        border-color: var(--border-hover);
        background: var(--bg-hover);
        color: var(--primary-color);
      }
      ${O9} :where(button[type='submit']) {
        border-color: var(--primary-color);
        background: var(--primary-color);
        color: var(--primary-contrast);
      }
      ${O9} :where(input:not([type='checkbox']):not([type='radio']):not([type='range']), select, textarea) {
        min-height: 34px;
        border: 1px solid var(--border-color);
        border-radius: 8px;
        background: var(--bg-primary);
        color: var(--text-primary);
      }
      ${O9} :where(input:focus, select:focus, textarea:focus) {
        border-color: var(--primary-color);
        box-shadow: inset 0 0 0 1px var(--focus-inset);
      }
      ${O9} :where(thead, th) {
        background: #f3f4f6;
        color: var(--text-secondary);
      }
      ${O9} :where(pre, code, kbd, samp) {
        border-color: var(--border-color);
        background: #f3f4f6;
        color: var(--text-primary);
      }
    `;
    const firstStyle = document.head.querySelector('style, link[rel="stylesheet"]');
    document.head.insertBefore(style, firstStyle);
  });

  // Verify body, heading, model row button, active range button, nav button, and Standalone link keep Fleethouse dark theme
  const styles = await page.evaluate(() => {
    const h1 = document.querySelector('h1');
    const modelBtn = document.querySelector('[data-metric="requests"] [data-model="gpt-6.1-sol"]');
    const brandBtn = document.querySelector('header button');
    const standaloneLink = document.querySelector('header a');
    const range30d = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === '30d');
    const refreshBtn = document.querySelector('button[title="Refresh"]');
    return {
      bodyBg: getComputedStyle(document.body).backgroundColor,
      h1Color: h1 ? getComputedStyle(h1).color : '',
      modelBg: modelBtn ? getComputedStyle(modelBtn).backgroundColor : '',
      modelMinHeight: modelBtn ? getComputedStyle(modelBtn).minHeight : '',
      modelBorderWidth: modelBtn ? getComputedStyle(modelBtn).borderTopWidth : '',
      brandBg: brandBtn ? getComputedStyle(brandBtn).backgroundColor : '',
      linkColor: standaloneLink ? getComputedStyle(standaloneLink).color : '',
      range30dBg: range30d ? getComputedStyle(range30d).backgroundColor : '',
      range30dColor: range30d ? getComputedStyle(range30d).color : '',
      range30dRadius: range30d ? getComputedStyle(range30d).borderTopLeftRadius : '',
      refreshBorderWidth: refreshBtn ? getComputedStyle(refreshBtn).borderTopWidth : '',
    };
  });

  expect(styles.bodyBg).toBe('rgb(0, 0, 0)');
  expect(styles.h1Color).toBe('rgb(255, 255, 255)');
  expect(styles.modelBg).toBe('rgba(0, 0, 0, 0)');
  expect(styles.modelMinHeight).toBe('0px');
  expect(styles.modelBorderWidth).toBe('0px');
  expect(styles.brandBg).toBe('rgba(0, 0, 0, 0)');
  // Must NOT be CPAMP's #409eff (rgb(64, 158, 255))
  expect(styles.linkColor).not.toBe('rgb(64, 158, 255)');
  // Active 30d button keeps dark Tailwind styling (not CPAMP's light-grey 12px radius button)
  expect(styles.range30dColor).toBe('rgb(255, 255, 255)');
  expect(styles.range30dRadius).not.toBe('12px');
  expect(styles.range30dBg).not.toBe('rgb(246, 250, 255)');
  expect(styles.refreshBorderWidth).toBe('1px');

  // Hovering the brand button must not turn its text CPAMP blue or background light blue
  const brandButton = page.locator('header button').first();
  await brandButton.hover();
  const hoverStyles = await brandButton.evaluate((el) => ({
    bg: getComputedStyle(el).backgroundColor,
    color: getComputedStyle(el).color,
  }));
  expect(hoverStyles.bg).not.toBe('rgb(219, 234, 254)');
  expect(hoverStyles.color).not.toBe('rgb(64, 158, 255)');

  // Table headers in Insights must also stay dark (not CPAMP's light-grey thead/th)
  const thBg = await page.locator('thead th').first().evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(thBg).toBe('rgba(0, 0, 0, 0)');

  // Sessions search input and Session Detail <pre> must also stay dark under CPAMP host CSS
  await page.getByRole('navigation').getByRole('button', { name: 'Sessions' }).click();
  await expect(page.getByRole('heading', { name: 'Sessions' })).toBeVisible();
  const searchInput = page.getByPlaceholder(/Search/);
  await searchInput.focus();
  const inputStyles = await searchInput.evaluate((el) => ({
    bg: getComputedStyle(el).backgroundColor,
    boxShadow: getComputedStyle(el).boxShadow,
  }));
  expect(inputStyles.bg).not.toBe('rgb(255, 255, 255)');
  expect(inputStyles.boxShadow).toBe('none');

  await page.getByRole('button', { name: /mock-long-project/ }).first().click();
  await expect(page.getByText(/END-OF-COMPLETE-ASSISTANT-ANSWER/)).toBeVisible();
  await page.getByRole('button', { name: /Process & tools/ }).click();
  await page.getByRole('button', { name: 'Raw JSON' }).click();
  const preBg = await page.locator('pre').first().evaluate((el) => getComputedStyle(el).backgroundColor);
  expect(preBg).not.toBe('rgb(243, 244, 246)');
});
