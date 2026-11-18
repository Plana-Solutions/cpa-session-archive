"""Read-only sample verification. Print counts and sizes, never row text or secrets."""
import base64
import glob
import json
import os
import re
import sqlite3
import urllib.parse
import urllib.request

import yaml

endpoint = os.environ["CLICKHOUSE_URL"]
user = os.environ.get("CLICKHOUSE_USER", "default")
password = open(os.environ["CLICKHOUSE_PASSWORD_FILE"]).read().rstrip("\r\n")
config = os.environ.get("CPA_CONFIG", "/data/compose/3/config.yaml")
auth = os.environ.get("CPA_AUTH_DIR", "/var/lib/docker/volumes/cli-proxy-api_cpa-auth/_data")
archive = os.environ["ARCHIVE_SQLITE"]
secrets = set()


def collect(node, under=False):
    if isinstance(node, dict):
        for key, value in node.items():
            collect(value, under or bool(re.search(r"(?i)token|secret|password|passwd|api[-_]?key|cookie|credential|authorization|private[-_]?key", key)))
    elif isinstance(node, list):
        for value in node:
            collect(value, under)
    elif isinstance(node, str) and under and len(node.strip()) >= 12:
        secrets.add(node.strip())


collect(yaml.safe_load(open(config)))
for path in glob.glob(auth + "/*.json"):
    collect(json.load(open(path)))
variants = set()
for secret in secrets:
    for value in [secret, json.dumps(secret)[1:-1], base64.b64encode(secret.encode()).decode(), base64.urlsafe_b64encode(secret.encode()).decode().rstrip("=")]:
        variants.add(value)
        if len(value) >= 24:
            variants.add(value[:24])


def rows(query):
    req = urllib.request.Request(
        endpoint + "/?" + urllib.parse.urlencode({"query": query, "output_format_json_quote_64bit_integers": 0}),
        headers={"X-ClickHouse-User": user, "X-ClickHouse-Key": password},
        data=b"",
    )
    with urllib.request.urlopen(req, timeout=300) as response:
        for line in response:
            if line.strip():
                yield json.loads(line)


db = sqlite3.connect("file:" + urllib.parse.quote(archive) + "?mode=ro", uri=True)
ids = []
native = set()
body_ids = set()
conversations = set()
missing_body = 0
leaks = 0
scanned = 0
counts = {}
for table in ["requests", "usage", "external_messages", "request_facts"]:
    count = 0
    suffix = " FINAL" if table == "external_messages" else ""
    for row in rows("SELECT * FROM cliproxy." + table + suffix + " FORMAT JSONEachRow"):
        if table == "requests":
            ids.append(row["request_id"])
            if row["request_body"].strip():
                body_ids.add(row["request_id"])
            else:
                missing_body += 1
        if table == "request_facts" and row["conversation_id"]:
            conversations.add(row["conversation_id"])
        if table == "external_messages":
            native.add(row["session_id"])
        text = json.dumps(row, ensure_ascii=False)
        decoded = "\n".join(str(v) for v in row.values())
        if any(v in text or v in decoded for v in variants):
            leaks += 1
        scanned += 1
        count += 1
    counts[table] = count
sessions = set()
body_sessions = set()
for offset in range(0, len(ids), 100):
    page = ids[offset:offset + 100]
    for rid, sid in db.execute("SELECT request_id,session_id FROM records WHERE request_id IN (" + ",".join("?" for _ in page) + ")", page):
        sessions.add(sid)
        if rid in body_ids:
            body_sessions.add(sid)
expected_events = sum(db.execute("SELECT count(*) FROM external_events WHERE session_id=?", (sid,)).fetchone()[0] for sid in native)
print(json.dumps({"rows": counts, "archive_proxy_sessions": len(sessions), "archive_proxy_sessions_with_bodies": len(body_sessions), "derived_proxy_conversations": len(conversations), "missing_body_requests": missing_body, "archive_native_sessions": len(native), "archive_native_events": expected_events, "secrets_known": len(secrets), "rows_scanned": scanned, "leak_rows": leaks}, sort_keys=True))
for query in [
    "SELECT parse_status,count() AS n FROM cliproxy.request_facts GROUP BY parse_status ORDER BY parse_status",
    "SELECT uniqExactIf(conversation_id,conversation_id!='') AS conversations,countIf(conversation_id='') AS without_conversation FROM cliproxy.request_facts",
    "SELECT countIf(total_tokens>0) AS rows_with_tokens,sum(input_tokens) AS sum_input_tokens,sum(output_tokens) AS sum_output_tokens,sum(total_tokens) AS sum_total_tokens FROM cliproxy.usage",
    "SELECT table,sum(rows) AS rows,sum(data_compressed_bytes) AS compressed_bytes,sum(bytes_on_disk) AS disk_bytes,sum(data_uncompressed_bytes) AS uncompressed_bytes FROM system.parts WHERE active AND database='cliproxy' GROUP BY table ORDER BY table",
]:
    for row in rows(query + " FORMAT JSONEachRow"):
        print(json.dumps(row, sort_keys=True))
assert leaks == 0, "secret leak detected (values suppressed)"
assert counts["requests"] == counts["request_facts"], "deriver did not cover all requests"
assert counts["external_messages"] == expected_events, "native events do not reconcile"

assert abs(len(conversations) - len(sessions)) <= max(1, len(sessions) * 0.03), "conversation counts differ by more than 3 percent"
