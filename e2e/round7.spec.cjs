const { test, expect } = require('@playwright/test');

async function open(page) {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({state: {
    apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key',
  }})));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
  await page.evaluate(() => {document.documentElement.lang='en'; applyI18n();});
}

test('search shows seven days and allows widening to thirty', async ({page}) => {
  await open(page);
  await expect(page.locator('#sessionSearchDays')).toHaveValue('7');
  const first = page.waitForRequest(r => r.url().includes('/sessions?') && new URL(r.url()).searchParams.get('q') === 'archive');
  await page.locator('#sessionSearch').fill('archive');
  expect(new URL((await first).url()).searchParams.get('days')).toBe('7');
  const wider = page.waitForRequest(r => r.url().includes('/sessions?') && new URL(r.url()).searchParams.get('days') === '30');
  await page.locator('#sessionSearchDays').selectOption('30');
  await wider;
});

test('export warns before reads and offers per-session export', async ({page}) => {
  await open(page);
  let exports = 0;
  await page.route('**/export?*', route => {exports++; return route.fulfill({json:{content:'',done:true}});});
  const dialog = new Promise(resolve => page.once('dialog', async dialog => {
    expect(dialog.message()).toContain('64 MiB');
    expect(dialog.message()).toContain('Download this session');
    await dialog.dismiss(); resolve();
  }));
  await page.evaluate(() => downloadExport('all'));
  await dialog;
  expect(exports).toBe(0);
  await page.locator('.archive-actions summary').click();
  await expect(page.locator('.archive-actions .export-controls')).toContainText('download individual sessions');
});
