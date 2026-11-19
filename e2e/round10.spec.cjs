const { test, expect } = require('@playwright/test');

test('proxy process previews expose request-specific full content', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({state: {
    apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key',
  }})));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
  await page.evaluate(() => {
    showSubDetail();
    currentRequestID = 'stale-request';
    document.querySelector('#subDetail').innerHTML = renderTurnProcessBody({
      request_id: 'proxy-long', entries: [
        {role: 'assistant', direction: 'output', text: 'answer preview', truncated: true},
        {role: 'reasoning', direction: 'output', text: 'reasoning preview', truncated: true},
      ],
    }, true);
  });
  const buttons = page.locator('#subDetail').getByRole('button', {name: '展开完整内容', exact: true});
  await expect(buttons).toHaveCount(2);
  for (const button of await buttons.all()) {
    await expect(button).toHaveAttribute('onclick', "event.stopPropagation();openRaw('response',decodeURIComponent('proxy-long'))");
  }
  await buttons.first().click();
  await expect(page).toHaveURL(/requests\/proxy-long\/raw\/response$/);
});
