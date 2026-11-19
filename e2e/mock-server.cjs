const {spawn} = require('node:child_process');
const fs = require('node:fs');
const candidate = '/private/tmp/cpa-archive-clickhouse-test/clickhouse';
const child = spawn('go', ['test', './internal/archive', '-run', '^TestClickHouseBrowserServer$', '-timeout', '0', '-v'], {
  stdio: 'inherit', env: {...process.env, ARCHIVE_BROWSER_SERVER: '1',
    CLICKHOUSE_BINARY: process.env.CLICKHOUSE_BINARY || (fs.existsSync(candidate) ? candidate : 'clickhouse')},
});
for(const signal of ['SIGINT','SIGTERM']) process.on(signal,()=>child.kill(signal));
child.on('exit',code=>process.exit(code || 0));
