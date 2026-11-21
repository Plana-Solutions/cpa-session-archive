import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import uuid
import xml.etree.ElementTree as ET

DOC = (Path(__file__).resolve().parents[1] / "docs/clickhouse-import.md").read_text()


class ImportProcedure(unittest.TestCase):
    def test_xml_grants_have_no_grantee(self):
        root = ET.fromstring(DOC.split("```xml\n")[1].split("\n```")[0])
        grants = root.findall("./query")
        self.assertEqual(len(grants), 3)
        self.assertTrue(all(" TO " not in g.text for g in grants))

    @unittest.skipUnless(os.getenv("IMPORT_PROCEDURE_DOCKER") == "1", "isolated Docker rehearsal")
    def test_25_8_grants_and_rollback(self):
        name = "imp11-regression-" + uuid.uuid4().hex[:8]

        def docker(*args):
            return subprocess.check_output(["docker", *args], stderr=subprocess.DEVNULL).decode()

        def query(sql, user="default"):
            args = ["exec", name, "clickhouse-client", "--user", user]
            if user == "cliproxy":
                args += ["--password", "imp11-test"]
            return docker(*args, "--query", sql).strip()

        with tempfile.TemporaryDirectory(prefix="imp11-") as directory:
            xml = Path(directory) / "user.xml"
            root = ET.fromstring("""<clickhouse>
              <profiles><writer><max_memory_usage>1073741824</max_memory_usage></writer></profiles>
              <users><cliproxy><password>imp11-test</password>
              <networks><ip>::/0</ip></networks><profile>writer</profile><quota>default</quota>
              <grants><query>GRANT ALL ON cliproxy.*</query></grants></cliproxy></users>
              </clickhouse>""")
            user = root.find("./users/cliproxy")
            user.remove(user.find("grants"))
            user.append(ET.fromstring(DOC.split("```xml\n")[1].split("\n```")[0]))
            xml.write_text(ET.tostring(root, encoding="unicode"))
            docker("run", "-d", "--name", name, "--network", "none",
                   "--tmpfs", "/var/lib/clickhouse", "--tmpfs", "/var/log/clickhouse-server",
                   "-v", str(xml) + ":/etc/clickhouse-server/users.d/imp11.xml:ro",
                   "clickhouse/clickhouse-server:25.8.33.6")
            try:
                import time
                for attempt in range(60):
                    try:
                        self.assertEqual(query("SELECT version()"), "25.8.33.6")
                        break
                    except subprocess.CalledProcessError:
                        if attempt == 59:
                            raise
                        time.sleep(1)
                self.assertEqual(query("CHECK GRANT INSERT ON cliproxy.request_facts", "cliproxy"), "1")
                self.assertEqual(query("SELECT value FROM system.settings WHERE name='max_memory_usage'", "cliproxy"), "1073741824")
                query("CREATE DATABASE cliproxy")
                schemas = {
                    "requests": "request_id String,request_headers Map(String,String)",
                    "usage": "request_id String,source String",
                    "body_blobs": "request_id String",
                    "external_messages": "request_id String,import_marker String",
                    "request_facts": "request_id String",
                    "turn_search": "request_id String",
                }
                for table, columns in schemas.items():
                    query(f"CREATE TABLE cliproxy.{table} ({columns}) ENGINE=MergeTree ORDER BY request_id")
                query("SELECT sum(unreserved_space) FROM system.disks", "cliproxy")
                query("SELECT table,partition,count(),sum(bytes_on_disk) FROM system.parts "
                      "WHERE active AND database='cliproxy' GROUP BY table,partition", "cliproxy")

                def insert():
                    query("INSERT INTO cliproxy.requests VALUES ('r',map('x-plana-import','archive-v1'))", "cliproxy")
                    query("INSERT INTO cliproxy.usage VALUES ('archive-v1:r','archive-v1')", "cliproxy")
                    query("INSERT INTO cliproxy.external_messages VALUES ('r','archive-v1')", "cliproxy")
                    for table in ["request_facts", "turn_search"]:
                        query(f"INSERT INTO cliproxy.{table} VALUES ('r')", "cliproxy")

                query("INSERT INTO cliproxy.body_blobs VALUES ('shared')", "cliproxy")
                for table in ["requests", "usage", "body_blobs", "external_messages"]:
                    query(f"SELECT * FROM cliproxy.{table} LIMIT 0", "cliproxy")
                insert()
                sql = "CREATE TABLE cliproxy.archive_v1_rollback_ids" + DOC.split(
                    "CREATE TABLE cliproxy.archive_v1_rollback_ids")[1].split("```")[0]
                for statement in sql.split(";"):
                    if statement.strip():
                        query(statement)
                for table in ["requests", "usage", "request_facts", "turn_search", "external_messages"]:
                    self.assertEqual(query(f"SELECT count() FROM cliproxy.{table}"), "0")
                self.assertEqual(query("SELECT count() FROM cliproxy.body_blobs"), "1")
                insert()
                for table in ["requests", "usage", "request_facts", "turn_search", "body_blobs"]:
                    self.assertEqual(query(f"SELECT count()=uniqExact(request_id) FROM cliproxy.{table}"), "1")
            finally:
                docker("rm", "-f", "-v", name)


if __name__ == "__main__":
    unittest.main()
