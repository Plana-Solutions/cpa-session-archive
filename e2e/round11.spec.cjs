const { test, expect } = require('@playwright/test');

test('automated turns are labelled separately from user prompts', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({state: {
    apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key',
  }})));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
  await page.evaluate(() => {
    document.querySelector('.sessions-panel').innerHTML = renderTurnSummary({
      turn_id: 'automated', automated: true, user_text: '', requests: 1,
      final_text: 'Client summary', last_at: new Date().toISOString(),
    }, 1);
  });
  const card = page.locator('.turn-card');
  await expect(card.getByRole('heading', {name: 'Automated step', exact: true})).toBeVisible();
  await expect(card).toContainText('Client notification or compaction');
  await expect(card).not.toContainText('CRITICAL: Respond with TEXT ONLY');
});
