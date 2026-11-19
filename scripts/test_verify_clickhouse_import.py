import ast
import re
import pathlib
import os
import subprocess
import sys
import unittest


class IdentityGateTest(unittest.TestCase):
    def setUp(self):
        source = ast.parse(pathlib.Path(__file__).with_name("verify_clickhouse_import.py").read_text())
        fn = next(n for n in source.body if isinstance(n, ast.FunctionDef) and n.name == "identities_match")
        namespace = {}
        exec(compile(ast.Module(body=[fn], type_ignores=[]), "gate", "exec"), namespace)
        self.gate = namespace["identities_match"]

    def test_one_to_one(self):
        self.assertTrue(self.gate({"a": {"a"}, "b": {"b"}}, {"a": {"a"}, "b": {"b"}}))

    def test_same_counts_wrong_ids(self):
        self.assertFalse(self.gate({"a": {"b"}, "b": {"a"}}, {"a": {"b"}, "b": {"a"}}))

    def test_split_and_merge(self):
        self.assertFalse(self.gate({"a": {"a", "b"}}, {"a": {"a"}, "b": {"a"}}))
        self.assertFalse(self.gate({"a": {"a"}, "b": {"a"}}, {"a": {"a", "b"}}))

    def test_dedup_column_identifiers(self):
        source = ast.parse(pathlib.Path(__file__).with_name("verify_clickhouse_import.py").read_text())
        fn = next(n for n in source.body if isinstance(n, ast.FunctionDef) and n.name == "filtered")
        columns = ["request_id", "body_sha256", "body_ref_offsets"]
        namespace = {"re": re, "rows": lambda query: [{"name": c} for c in columns]}
        exec(compile(ast.Module(body=[fn], type_ignores=[]), "columns", "exec"), namespace)
        self.assertEqual(list(namespace["filtered"]("requests", "request_id", [])), [])
        columns.append("invalid;column")
        with self.assertRaises(AssertionError):
            list(namespace["filtered"]("requests", "request_id", []))

    def test_production_url_refused(self):
        env = dict(os.environ, CLICKHOUSE_URL="http://100.127.160.51:8123")
        result = subprocess.run([sys.executable, str(pathlib.Path(__file__).with_name("verify_clickhouse_import.py"))], env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("isolated loopback", result.stderr)


if __name__ == "__main__":
    unittest.main()
