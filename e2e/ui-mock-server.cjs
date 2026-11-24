const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

const htmlPath = path.resolve(__dirname, '../internal/archive/web/index.html');
const PORT = Number(process.env.MOCK_UI_PORT || 4174);

const fixture = JSON.parse(
  execFileSync(process.execPath, [path.resolve(__dirname, 'fixture-source.cjs')], {
    encoding: 'utf8',
  })
);

const now = new Date();
const hourAgo = new Date(now.getTime() - 3600 * 1000).toISOString();
const twoHoursAgo = new Date(now.getTime() - 7200 * 1000).toISOString();
const fullUser = `${fixture.escapedSummary} mock-long-project ${fixture.escapedEnvironment} ${fixture.longUser}`;
const b64 = (obj) => Buffer.from(JSON.stringify(obj), 'utf8').toString('base64');

function buildDaily(days) {
  const out = [];
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(now.getTime() - i * 86400 * 1000).toISOString().slice(0, 10);
    out.push({
      day: d,
      requests: {
        anthropic: i === 0 ? 40 : 12,
        openai: i === 0 ? 61 : 18,
        google: i === 0 ? 20 : 5,
        other: i === 0 ? 20 : 2,
      },
    });
  }
  return out;
}

const sessionsData = [
  {
    session_id: 'mock-session-long-content',
    summary: fullUser.slice(0, 200),
    project: 'mock-long-project',
    key_id: 'mock-key-hash',
    model: 'gpt-6.1-sol',
    client: 'codex',
    requests: 41,
    first_at: hourAgo,
    last_at: hourAgo,
    input_tokens: 410,
    output_tokens: 82,
    total_tokens: 492,
  },
  {
    session_id: 'external:imported-session',
    summary: 'Imported native transcript',
    project: 'mock-long-project',
    key_id: '',
    model: '',
    client: 'claude-import',
    requests: 2,
    first_at: twoHoursAgo,
    last_at: twoHoursAgo,
    input_tokens: 0,
    output_tokens: 0,
    total_tokens: 0,
    source_kind: 'external-local-history',
    source_tool: 'claude-import',
  },
  {
    system: true,
    session_id: 'codex:system-title',
    summary: 'Generate a title that will help the user recognize this T3 Code thread weeks later.',
    project: '',
    key_id: '',
    model: 'gpt-6-astra',
    client: 'codex',
    requests: 1,
    first_at: twoHoursAgo,
    last_at: twoHoursAgo,
    input_tokens: 20,
    output_tokens: 10,
    total_tokens: 30,
  },
];

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://127.0.0.1:${PORT}`);
  const p = url.pathname;

  if (p === '/healthz') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify({ ok: true }));
  }

  if (
    p === '/' ||
    p === '/archive' ||
    p === '/management.html' ||
    p === '/v0/resource/plugins/cpa-session-archive/index.html'
  ) {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    return res.end(fs.readFileSync(htmlPath));
  }

  if (p.startsWith('/v0/management/')) {
    const auth = req.headers.authorization || '';
    if (auth !== 'Bearer mock-management-key') {
      res.writeHead(401, { 'Content-Type': 'application/json' });
      return res.end(JSON.stringify({ error: 'unauthorized' }));
    }
  }

  const json = (obj) => {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(obj));
  };

  if (p === '/v0/management/api-key-aliases') {
    return json({ items: [{ apiKeyHash: 'sha256:mock-key-hash', alias: '长文本测试 Key' }] });
  }
  if (p === '/v0/management/plugins/cpa-key-policy/identities') {
    return json({ identities: [] });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/stats') {
    return json({ records: 143, sessions: 2 });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/facets') {
    return json([
      { name: 'model.resolved', value: 'gpt-6.1-sol', sessions: 1, records: 41 },
      { name: 'model.resolved', value: 'claude-opus-5-5', sessions: 1, records: 20 },
      { name: 'client', value: 'codex', sessions: 1, records: 41 },
      { name: 'client', value: 'claude-import', sessions: 1, records: 2 },
      { name: 'outcome', value: 'succeeded', sessions: 1, records: 41 },
      { name: 'outcome', value: 'imported', sessions: 1, records: 2 },
      { name: 'key.id', value: 'mock-key-hash', sessions: 1, records: 41 },
    ]);
  }
  if (p === '/v0/management/plugins/cpa-session-archive/insights') {
    const days = Number(url.searchParams.get('days') || 30);
    return json({
      window_days: days,
      from: new Date(now.getTime() - days * 86400 * 1000).toISOString(),
      to: now.toISOString(),
      generated_at: now.toISOString(),
      totals: { requests: 141, sessions: 1, turns: 1, models: 6 },
      models: [
        { model: 'gpt-6.1-sol', provider: 'openai', requests: 41, sessions: 1, turns: 1, steps_per_turn: 41, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
        { model: 'claude-opus-5-5', provider: 'anthropic', requests: 20, sessions: 0, turns: 0, steps_per_turn: 0, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
        { model: 'claude-sonnet-5-5', provider: 'anthropic', requests: 20, sessions: 0, turns: 0, steps_per_turn: 0, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
        { model: 'gpt-6-astra', provider: 'openai', requests: 20, sessions: 0, turns: 0, steps_per_turn: 0, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
        { model: 'gemini-3-pro', provider: 'google', requests: 20, sessions: 0, turns: 0, steps_per_turn: 0, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
        { model: 'kimi-k3', provider: 'other', requests: 20, sessions: 0, turns: 0, steps_per_turn: 0, avg_latency_seconds: 2.5, failure_rate: 0.0, cancel_rate: 0.0 },
      ],
      daily: buildDaily(days),
    });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/sessions') {
    let rows = sessionsData.slice();
    if (url.searchParams.get('show_system') !== '1') {
      rows = rows.filter((r) => !r.system);
    }
    const model = url.searchParams.get('model.resolved');
    if (model) rows = rows.filter((r) => r.model === model);
    const client = url.searchParams.get('client');
    if (client) rows = rows.filter((r) => r.client === client);
    const q = (url.searchParams.get('q') || '').toLowerCase();
    if (q) {
      rows = rows.filter(
        (r) =>
          r.summary.toLowerCase().includes(q) ||
          r.session_id.toLowerCase().includes(q) ||
          r.project.toLowerCase().includes(q)
      );
    }
    return json(rows);
  }
  if (p === '/v0/management/plugins/cpa-session-archive/cancel-search') {
    return json({ status: 'ok' });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/turn-text') {
    return json({ status: 'ok', text: fullUser });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/turns') {
    const sid = url.searchParams.get('session_id');
    const tid = url.searchParams.get('turn_id');
    if (sid === 'external:imported-session') {
      const extTurn = {
        turn_id: '1',
        session_id: sid,
        user_text: 'Native prompt',
        final_text: 'Native answer',
        requests: 2,
        first_at: twoHoursAgo,
        last_at: twoHoursAgo,
        key_id: '',
        model: '',
        outcome: 'imported',
        status_code: 200,
        duration_ms: 0,
        input_tokens: 0,
        output_tokens: 0,
        reasoning_tokens: 0,
        total_tokens: 0,
      };
      if (tid) {
        return json({ turn: extTurn, records: [], total: 2, limit: 100, offset: 0 });
      }
      return json({ turns: [extTurn], total: 1, limit: 25, offset: 0 });
    }
    const turn = {
      turn_id: 'mock-turn-long-content',
      session_id: sid,
      user_text: fullUser.slice(0, 1600),
      final_text: fixture.longAssistant,
      final_request: 'mock-request-long-content',
      requests: 41,
      first_at: hourAgo,
      last_at: hourAgo,
      key_id: 'mock-key-hash',
      model: 'gpt-6.1-sol',
      outcome: 'succeeded',
      status_code: 200,
      tool_names: ['shell_command'],
      duration_ms: 2500,
      input_tokens: 410,
      output_tokens: 82,
      reasoning_tokens: 0,
      total_tokens: 492,
    };
    if (tid) {
      return json({
        turn,
        records: [
          {
            request_id: 'mock-request-long-content',
            session_id: sid,
            model: 'gpt-6.1-sol',
            outcome: 'succeeded',
            status_code: 200,
            started_at: hourAgo,
            completed_at: hourAgo,
            entries: [
              { role: 'assistant', label: 'reasoning', text: fixture.longAssistant },
              { role: 'tool_call', label: 'shell_command' },
            ],
          },
        ],
        total: 41,
        limit: 100,
        offset: 0,
      });
    }
    return json({ turns: [turn], total: 1, limit: 25, offset: 0 });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/request-view') {
    return json({
      request_id: url.searchParams.get('id'),
      session_id: 'mock-session-long-content',
      model: 'gpt-6.1-sol',
      outcome: 'succeeded',
      entries: [
        { role: 'system', direction: 'input', text: fixture.longSystem.slice(0, 4000), truncated: true },
        { role: 'user', direction: 'input', text: fullUser.slice(0, 4000), truncated: true },
        {
          role: 'tool_call',
          label: 'shell_command',
          call_id: 'call-long-output',
          direction: 'output',
          text: '{"command":"printf fixture"}',
        },
        {
          role: 'tool_result',
          label: 'shell_command',
          call_id: 'call-long-output',
          direction: 'output',
          text: fixture.longToolOutput.slice(0, 4000),
          truncated: true,
        },
      ],
    });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/request-context') {
    return json([
      {
        request_id: url.searchParams.get('id'),
        session_id: 'mock-session-long-content',
        response: b64({
          output: [
            {
              type: 'function_call_output',
              call_id: url.searchParams.get('call_id') || 'call-long-output',
              output: fixture.longToolOutput,
            },
          ],
        }),
      },
    ]);
  }
  if (p === '/v0/management/plugins/cpa-session-archive/requests') {
    return json({
      request_id: url.searchParams.get('id'),
      session_id: 'mock-session-long-content',
      model: 'gpt-6.1-sol',
      requested_model: 'gpt-5.6-sol',
      outcome: 'succeeded',
      status_code: 200,
      original_request: b64(fixture.originalRequest),
      response: b64(fixture.originalResponse),
    });
  }
  if (p === '/v0/management/plugins/cpa-session-archive/export') {
    return json({
      filename: 'session.archive.jsonl',
      content: '{"request_id":"mock-request-long-content"}\n',
      done: true,
      next_offset: 1,
      next_session_offset: 0,
      next_byte_offset: 0,
      from: twoHoursAgo,
      to: now.toISOString(),
    });
  }

  res.writeHead(404, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify({ error: 'not found' }));
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`UI mock server listening on http://127.0.0.1:${PORT}`);
});
