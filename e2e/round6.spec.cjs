const { test, expect } = require('@playwright/test');

test('system sessions are hidden by default and can be shown', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({state: {
    apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key',
  }})));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
  const toggle = page.locator('#showSystemSessions');
  await expect(toggle).not.toBeChecked();
  await expect(page.locator('.session-row').filter({hasText: '系统任务'})).toHaveCount(0);
  const response = page.waitForResponse(r => r.url().includes('/sessions?') && r.url().includes('show_system=1'));
  await toggle.check();
  expect((await response).status()).toBe(200);
  await expect(page.locator('.session-row').filter({hasText: '系统任务'})).toHaveCount(1);
  await toggle.uncheck();
  await expect(page.locator('.session-row').filter({hasText: '系统任务'})).toHaveCount(0);
});

test('export download reports skipped incomplete records', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({state: {
    apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key',
  }})));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
  await page.route('**/export?*', route => route.fulfill({json: {
    content: '', skipped_incomplete: 1, done: true, filename: 'test.jsonl', content_type: 'application/x-ndjson',
  }}));
  page.once('dialog', dialog => dialog.accept());
  await page.evaluate(() => { document.documentElement.lang='en'; applyI18n(); return downloadExport('all'); });
  await expect(page.locator('#allDownloadNotice')).toContainText('Skipped 1 incomplete records');
});
