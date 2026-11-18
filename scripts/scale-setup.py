import argparse
import shutil
from pathlib import Path
import urllib.error
import urllib.parse
import urllib.request

parser = argparse.ArgumentParser()
parser.add_argument('--url', required=True)
args = parser.parse_args()
endpoint = urllib.parse.urlparse(args.url)
if endpoint.hostname not in ('127.0.0.1', 'localhost', '::1'):
    parser.error('synthetic writes require a local ClickHouse')
url = args.url.rstrip('/') + '/?max_threads=1&max_block_size=1000&max_insert_block_size=1000&min_insert_block_size_rows=1000&min_insert_block_size_bytes=0'


def execute(sql):
    if shutil.disk_usage("/System/Volumes/Data").free < 3 * 1024**3:
        raise RuntimeError("stopping below 3 GiB free")
    try:
        return urllib.request.urlopen(url, sql.encode(), timeout=60).read().decode()
    except urllib.error.HTTPError as error:
        raise RuntimeError(error.read().decode()) from error


if execute("SELECT count() FROM system.tables WHERE database='cliproxy'").strip() != '0':
    parser.error('use a disposable server without existing cliproxy tables')
root = Path(__file__).resolve().parents[1]
for file in ('schema.sql', 'derived.sql'):
    schema = (root / 'internal/archive/testdata/clickhouse' / file).read_text()
    for statement in schema.replace('{db}', 'cliproxy').split('\n;\n'):
        execute(statement)
for statement in (root / 'scripts/scale-fixture.sql').read_text().split(';'):
    if not statement.strip():
        continue
    if 'numbers(300000)' in statement:
        for offset in range(0, 300000, 2000):
            execute(statement.replace('numbers(300000)', f'numbers({offset},2000)'))
    elif "INSERT INTO cliproxy.requests" in statement:
        for bucket in range(100):
            execute(statement + f" WHERE cityHash64(request_id)%100={bucket}")
    else:
        execute(statement)
assert execute('SELECT count() FROM cliproxy.request_facts').strip() == '300000'
assert execute('SELECT uniqExact(conversation_id) FROM cliproxy.request_facts').strip() == '25000'
print(execute('SELECT count(),avg(length(last_user_text)),avg(length(reply_text)),avg(length(msg_text)) FROM cliproxy.request_facts'))
