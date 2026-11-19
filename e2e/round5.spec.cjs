const { test, expect } = require('@playwright/test');

async function open(page) {
  await page.addInitScript(() => localStorage.setItem('cli-proxy-auth', JSON.stringify({
    state: { apiBase: 'http://127.0.0.1:4173', managementKey: 'mock-management-key' },
  })));
  await page.goto('/#/sessions');
  await expect(page.locator('.session-row').first()).toBeVisible();
}

test('HTTP without randomUUID still initializes', async ({ page }) => {
  await page.addInitScript(() => Object.defineProperty(crypto, 'randomUUID', { value: undefined }));
  await open(page);
});

test('context result retains original tool name and arguments', async ({ page }) => {
  await open(page);
  await page.evaluate(() => {
    ensureRequestRecord = async () => ({
      original_request: btoa(JSON.stringify({ input: [{
        type: 'function_call', call_id: 'context-call', name: 'original_shell',
        arguments: '{"command":"original-input"}',
      }] })),
      response: btoa('{}'),
    });
    api = async () => [{ response: btoa(JSON.stringify({ output: [{
      type: 'function_call_output', call_id: 'context-call', output: 'context-result',
    }] })) }];
    return showToolDetail('session', 'request', 'context-call');
  });
  await expect(page.locator('#subDetailTitle')).toHaveText('original_shell');
  await expect(page.locator('#subDetail')).toContainText('original-input');
  await expect(page.locator('#subDetail')).toContainText('context-result');
});

test('truncated previews offer full diagnostics', async ({ page }) => {
  await open(page);
  await page.evaluate(() => {
    showSubDetail();
    document.querySelector('#subDetail').innerHTML = renderTimelineEntry({
      role: 'tool_result', direction: 'input', text: 'shortened', truncated: true,
    });
  });
  await expect(page.locator('#subDetail button')).toBeVisible();
  await expect(page.locator('#subDetail button')).toHaveAttribute('onclick', "event.stopPropagation();openRaw('request')");
});

test('truncated native process events offer full stored diagnostics', async ({ page }) => {
  await open(page);
  await page.evaluate(() => {
    showSubDetail();
    currentRequestID = 'stale-previous-request';
    document.querySelector('#subDetail').innerHTML = renderTurnProcessBody({
      request_id: 'external:large-native:1', source_format: 'external-native-event', entries: [{
        role: 'tool_call', label: 'shell', call_id: 'native-call', direction: 'input', text: 'short native preview', truncated: true,
      }],
    }, true);
  });
  await expect(page.locator('#subDetail button')).toBeVisible();
  await expect(page.locator('#subDetail button')).toHaveAttribute('onclick', "event.stopPropagation();openRaw('request',decodeURIComponent('external%3Alarge-native%3A1'))");
  await expect(page.locator('#subDetail')).toContainText('shell · native-call');
  await expect(page.locator('#subDetail button')).toHaveCount(1);
});

for (const surface of ['summary', 'detail']) {
  test('bounded final answers open full diagnostics from ' + surface, async ({ page }) => {
    await open(page);
    await page.evaluate(async (surface) => {
      const turn = { turn_id: 'long-turn', user_text: 'question', final_text: 'x'.repeat(12000),
        final_truncated: true, final_request: 'source-response', requests: 1, last_at: '2026-11-18T12:00:00Z' };
      const originalApi = api;
      api = async (path, ...args) => {
        if (path.startsWith('/turns?')) return { turn, records: [], total: 0, limit: 10, offset: 0 };
        if (path.startsWith('/requests?')) return { request_id: 'source-response', original_request: btoa('{}'),
          response: btoa(JSON.stringify({ output: [{ role: 'assistant', content: 'x'.repeat(24000) + 'REAL-ANSWER-END' }] })) };
        return originalApi(path, ...args);
      };
      detailSessionID = 'final-session';
      currentTurnID = '';
      currentRequestID = 'unrelated-request';
      if (surface === 'summary') {
        showSubDetail();
        document.querySelector('#subDetail').innerHTML = renderTurnSummary(turn, 1);
      } else {
        await showTurn('final-session', 'long-turn');
      }
    }, surface);
    const area = page.locator(surface === 'summary' ? '#subDetail .turn-final' : '#turnPrimary .turn-final');
    await area.getByRole('button', { name: '展开完整内容', exact: true }).click();
    await expect(page).toHaveURL(/requests\/source-response\/raw\/response$/);
  });
}
