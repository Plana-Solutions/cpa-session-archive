"""Read-only sample verification. Print counts and sizes, never row text or secrets."""
import base64
import glob
import json
import os
import re
import sqlite3
import urllib.parse
import urllib.request


endpoint = os.environ["CLICKHOUSE_URL"].rstrip("/")
parsed = urllib.parse.urlsplit(endpoint)
if parsed.hostname not in {"127.0.0.1", "localhost", "::1"} or parsed.username or parsed.query:
    raise SystemExit("verification requires the isolated loopback sample URL")
import yaml

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
        endpoint + "/?" + urllib.parse.urlencode({"query": query, "output_format_json_quote_64bit_integers": 0, "output_format_json_validate_utf8": 1}),
        headers={"X-ClickHouse-User": user, "X-ClickHouse-Key": password},
        data=b"",
    )
    with urllib.request.urlopen(req, timeout=300) as response:
        for line in response:
            if line.strip():
                yield json.loads(line)


if list(rows("SELECT hostName() AS host FORMAT JSONEachRow")) != [{"host": "cpa-archive-import-sample"}]:
    raise SystemExit("verification refuses a server without the throwaway sample hostname")

state = json.load(open(os.environ["IMPORT_STATE"]))
db = sqlite3.connect("file:" + urllib.parse.quote(archive) + "?mode=ro", uri=True)
records = list(db.execute("SELECT request_id,session_id,facets_json FROM records WHERE id>? AND id<=? AND source_format!='external-native-event' AND (COALESCE(model,'')<>'' OR COALESCE(requested_model,'')<>'') ORDER BY id", (state["FirstID"], state["UpperID"])))
assert 0 < len(records) <= 10000, "verification requires a bounded sample"
assert not state["Quarantine"], "sample has quarantined rows"
ids = [r[0] for r in records]
native = [r[0] for r in db.execute("SELECT id FROM external_sessions ORDER BY id LIMIT ?", (int(os.environ.get("EXTERNAL_SESSIONS", "50")),))]



def identities_match(expected, derived):
    return set(expected) == set(derived) and all(values == {key} for key, values in expected.items()) and all(values == {key} for key, values in derived.items())


def filtered(table, field, values):
    columns = [r["name"] for r in rows("SELECT name FROM system.columns WHERE database='cliproxy' AND table='" + table + "' ORDER BY position FORMAT JSONEachRow")]
    assert columns and all(re.fullmatch(r"[a-z_][a-z0-9_]*", c) for c in columns)
    suffix = " FINAL" if table == "external_messages" else ""
    projection = {"body_sha256": "hex(body_sha256) AS body_sha256", "body_refs": "arrayMap(x -> hex(x),body_refs) AS body_refs", "body_residual": "base64Encode(body_residual) AS body_residual"} if table == "requests" else {}
    selection = [projection.get(c,c) for c in columns]
    for offset in range(0, len(values), 16):
        quoted = ",".join("'" + v.replace("\\", "\\\\").replace("'", "\\'") + "'" for v in values[offset:offset + 16])
        yield from rows("SELECT " + ",".join(selection) + " FROM cliproxy." + table + suffix + " WHERE " + field + " IN (" + quoted + ") FORMAT JSONEachRow")


expected_rows = {r["request_id"]: r for r in map(json.loads, open(os.environ["EXPECTED_FILE"]))}
assert set(expected_rows) == set(ids), "byte verification did not cover the complete sample"
assert all(r["byte_exact"] for r in expected_rows.values())
live = {r["request_id"]: r for r in map(json.loads, open(os.environ["LIVE_FACTS"]))} if os.environ.get("LIVE_FACTS") else {}
body_ids = set()
facts = {}
usage_ids = set()
request_ids = set()
missing_body = leaks = scanned = no_model = count_tokens = bad_count_tokens = codex_rows = bad_codex = 0
count_tokens_outside_claude = 0
counts = {}
clients = {}
statuses = {}
for table in ["requests", "usage", "external_messages", "request_facts"]:
    count = 0
    values = native if table == "external_messages" else ["archive-v1:" + v for v in ids] if table == "usage" else ids
    field = "session_id" if table == "external_messages" else "request_id"
    for row in filtered(table, field, values):
        if table == "requests":
            assert row["request_id"] not in request_ids, "duplicate raw request"
            request_ids.add(row["request_id"])
            if row["request_body"].strip() or row.get("body_len", 0):
                body_ids.add(row["request_id"])
            else:
                missing_body += 1
            no_model += not row["model"] and not row["requested_model"]
            residual = base64.b64decode(row.get("body_residual", "")).decode("utf-8", errors="replace")
            leaks += any(v in residual for v in variants)
        if table == "usage":
            assert row["request_id"] not in usage_ids, "duplicate usage request"
            usage_ids.add(row["request_id"])
        if table == "request_facts":
            assert row["request_id"] not in facts, "duplicate derived request"
            facts[row["request_id"]] = row["conversation_id"]
            clients[row["client"]] = clients.get(row["client"], 0) + 1
            statuses[row["parse_status"]] = statuses.get(row["parse_status"], 0) + 1
            if row["kind"] == "count_tokens":
                count_tokens += 1
                outside = not row["conversation_id"].startswith("claude:")
                count_tokens_outside_claude += outside
                bad_count_tokens += outside and expected_rows[row["request_id"]]["conversation_id"].startswith("claude:")
            if row["client"] == "codex_cli_rs" or row["conversation_source"] == "codex-header":
                codex_rows += 1
                bad_codex += not row["conversation_id"].startswith("codex:")
        text = json.dumps(row, ensure_ascii=False)
        decoded = "\n".join(str(v) for v in row.values())
        if any(v in text or v in decoded for v in variants):
            leaks += 1
        scanned += 1
        count += 1
    counts[table] = count
expected = {}
derived = {}
for rid, sid, facets in records:
    if rid not in body_ids:
        continue
    sid = expected_rows[rid]["conversation_id"]
    cid = facts.get(rid, "")
    expected.setdefault(sid, set()).add(cid)
    derived.setdefault(cid, set()).add(sid)
gate = identities_match(expected, derived)
shared = set(live) & set(facts)
live_mismatches = sum(live[rid]["conversation_id"] != facts[rid] for rid in shared)
expected_mismatches = sum(expected_rows[rid]["conversation_id"] != facts.get(rid) for rid in ids)
gate = gate and expected_mismatches == 0 and live_mismatches == 0
if os.environ.get("REQUIRE_LIVE_OVERLAP") == "1":
    assert shared, "sample has no live overlap"


# Blob bytes are scanned independently because request_body is empty for skeleton rows.
for row in rows("SELECT base64Encode(data) AS data FROM cliproxy.body_blobs FORMAT JSONEachRow"):
    raw = base64.b64decode(row["data"]).decode("utf-8", errors="replace")
    leaks += any(v in raw for v in variants)
    scanned += 1

expected_events = sum(db.execute("SELECT count(*) FROM external_events WHERE session_id=?", (sid,)).fetchone()[0] for sid in native)
print(json.dumps({"rows": counts, "raw_archive_body_session_ids": len({sid for rid, sid, _ in records if rid in body_ids}), "live_extractor_sessions_with_bodies": len(expected), "derived_conversations_with_bodies": len(derived), "one_to_one_gate": gate, "byte_exact": len(expected_rows), "live_overlap": len(shared), "live_identity_mismatches": live_mismatches, "released_extractor_mismatches": expected_mismatches, "missing_body_requests": missing_body, "no_model_rows": no_model, "count_tokens_rows": count_tokens, "count_tokens_outside_claude": count_tokens_outside_claude, "count_tokens_identity_mismatches": bad_count_tokens, "codex_rows": codex_rows, "codex_without_prefix": bad_codex, "clients": clients, "parse_status": statuses, "archive_native_sessions": len(native), "archive_native_events": expected_events, "secrets_known": len(secrets), "rows_scanned": scanned, "leak_rows": leaks}, sort_keys=True))
for query in [
    "SELECT countIf(total_tokens>0) AS rows_with_tokens,sum(input_tokens) AS sum_input_tokens,sum(output_tokens) AS sum_output_tokens,sum(total_tokens) AS sum_total_tokens FROM cliproxy.usage",
    "SELECT table,count() AS parts,sum(rows) AS rows,sum(data_compressed_bytes) AS compressed_bytes,sum(bytes_on_disk) AS disk_bytes,sum(data_uncompressed_bytes) AS uncompressed_bytes FROM system.parts WHERE active AND database='cliproxy' GROUP BY table ORDER BY table",
]:
    for row in rows(query + " FORMAT JSONEachRow"):
        print(json.dumps(row, sort_keys=True))
assert live_mismatches == 0, "imported conversation_id differs from production request_facts"
assert expected_mismatches == 0, "imported conversation_id differs from released live extractor"
assert leaks == 0, "secret leak detected (values suppressed)"
assert no_model == 0, "empty model rows imported"
assert set(facts) == set(ids), "deriver did not cover all requests"
assert usage_ids == {"archive-v1:" + rid for rid in ids}, "usage IDs do not reconcile"
assert request_ids == set(ids), "raw request IDs do not reconcile"
assert counts["requests"] == len(ids), "request count differs"
assert counts["external_messages"] == expected_events, "native events do not reconcile"
assert not bad_count_tokens and not bad_codex, "identity prefix parity failed"
assert gate, "archive sessions and derived conversations are not one-to-one"
