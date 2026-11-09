"""Offline transfer regressions; run: python3 -m unittest discover -s scripts/tests -v."""
import contextlib
import importlib.util
import json
import os
import subprocess
import uuid
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("migration", Path(__file__).parents[1] / "migrate-postgres-to-sqlite.py")
migration = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(migration)

ID = "ee8a1224-746a-4531-a88b-7fe95e96d6d8"


class SQLiteMigrationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.root = Path(self.directory.name)
        self.template = self.root / "schema.sqlite"
        self.output = self.root / "imported.sqlite"
        self.snapshot = self.root / "snapshot.jsonl"
        with contextlib.closing(sqlite3.connect(self.template)) as db:
            db.executescript('''
              CREATE TABLE _fluent_migrations (id TEXT PRIMARY KEY, name TEXT, batch INTEGER);
              INSERT INTO _fluent_migrations VALUES ('local-migration-id', 'App.CreateAccounts', 1);
              CREATE TABLE accounts (id TEXT PRIMARY KEY, enabled INTEGER NOT NULL, created_at REAL,
                profile TEXT, secret BLOB, display TEXT);
              CREATE TABLE app_sessions (id TEXT PRIMARY KEY, account_id TEXT REFERENCES accounts(id), payload TEXT);
            ''')
        self.entries = [
            {"format": migration.FORMAT, "schema": "public"},
            {"table": "_fluent_migrations", "count": 1, "columns": [
                {"name": "id", "type": "uuid"}, {"name": "name", "type": "text"}, {"name": "batch", "type": "int8"}]},
            {"row": "_fluent_migrations", "values": {"id": ID, "name": "App.CreateAccounts", "batch": 2}},
            {"table": "accounts", "count": 1, "columns": [
                {"name": "id", "type": "uuid"}, {"name": "enabled", "type": "bool"},
                {"name": "created_at", "type": "timestamptz"}, {"name": "profile", "type": "jsonb"},
                {"name": "secret", "type": "bytea"}, {"name": "display", "type": "text"}]},
            {"row": "accounts", "values": {"id": ID, "enabled": True, "created_at": "2026-11-09T12:30:01.123456+00:00",
                "profile": {"tags": ["one", "two"]}, "secret": "\\x00ff4142", "display": "private\n‘quoted’ Ω"}},
            {"table": "app_sessions", "count": 1, "columns": [
                {"name": "id", "type": "uuid"}, {"name": "account_id", "type": "uuid"}, {"name": "payload", "type": "text"}]},
            {"row": "app_sessions", "values": {"id": ID, "account_id": ID, "payload": '{"private":"session"}'}},
            {"complete": True},
        ]

    def tearDown(self):
        self.directory.cleanup()

    def write_snapshot(self):
        self.snapshot.write_text("\n".join(json.dumps(e) for e in self.entries) + "\n")

    def transfer(self):
        self.write_snapshot()
        return migration.import_snapshot(self.snapshot, self.template, self.output)

    def test_round_trip_preserves_types_sessions_and_local_migration_history(self):
        report = self.transfer()
        self.assertEqual(report["tables"]["accounts"]["rows"], 1)
        self.assertEqual(len(report["tables"]["accounts"]["multiset_sha256"]), 64)
        self.assertEqual(self.output.stat().st_mode & 0o777, 0o600)
        with contextlib.closing(sqlite3.connect(self.output)) as db:
            row = db.execute("SELECT * FROM accounts").fetchone()
            self.assertEqual(row[0], ID.upper())
            self.assertEqual(row[1], 1)
            self.assertAlmostEqual(row[2], 1794227401.123456, places=5)
            self.assertEqual(json.loads(row[3]), {"tags": ["one", "two"]})
            self.assertEqual(row[4], bytes.fromhex("00ff4142"))
            self.assertEqual(row[5], "private\n‘quoted’ Ω")
            self.assertEqual(db.execute("SELECT id FROM _fluent_migrations").fetchone()[0], "local-migration-id")
            self.assertEqual(db.execute("SELECT account_id FROM app_sessions").fetchone()[0], ID.upper())
        with contextlib.closing(sqlite3.connect(self.template)) as db:
            self.assertEqual(db.execute("SELECT count(*) FROM accounts").fetchone()[0], 0)

    def test_output_is_not_replaced(self):
        self.output.write_text("keep me")
        with self.assertRaisesRegex(ValueError, "already exists"):
            self.transfer()
        self.assertEqual(self.output.read_text(), "keep me")

    def test_missing_completion_marker_does_not_publish_partial_import(self):
        self.entries.pop()
        with self.assertRaisesRegex(ValueError, "incomplete"):
            self.transfer()
        self.assertFalse(self.output.exists())
        self.assertEqual(list(self.root.glob(".sqlite-import-*")), [])

    def test_different_migration_revision_is_rejected(self):
        self.entries[2]["values"]["name"] = "App.UnknownMigration"
        with self.assertRaisesRegex(ValueError, "migration histories differ"):
            self.transfer()
        self.assertFalse(self.output.exists())

    def test_foreign_key_violation_is_rejected(self):
        self.entries[6]["values"]["account_id"] = "00000000-0000-0000-0000-000000000000"
        with self.assertRaisesRegex(ValueError, "foreign key"):
            self.transfer()
        self.assertFalse(self.output.exists())

    def test_unknown_table_and_missing_columns_fail_closed(self):
        self.entries[3]["table"] = "unknown_table"
        with self.assertRaisesRegex(ValueError, "unexpected source table"):
            self.transfer()
        self.entries[3]["table"] = "accounts"
        self.entries[3]["columns"].pop()
        with self.assertRaisesRegex(ValueError, "column mismatch"):
            self.transfer()

    def test_nonempty_template_is_rejected(self):
        with contextlib.closing(sqlite3.connect(self.template)) as db:
            db.execute("INSERT INTO accounts (id, enabled) VALUES ('existing', 1)")
            db.commit()
        with self.assertRaisesRegex(ValueError, "contains application data"):
            self.transfer()

    def test_duplicate_rows_cannot_silently_disappear(self):
        self.entries.insert(5, self.entries[4])
        with self.assertRaises(sqlite3.IntegrityError):
            self.transfer()
        self.assertFalse(self.output.exists())

    def test_row_and_table_omissions_are_rejected(self):
        del self.entries[5:7]
        with self.assertRaisesRegex(ValueError, "table inventory mismatch"):
            self.transfer()

    def test_unsupported_type_cannot_be_lossily_converted(self):
        self.entries[3]["columns"][1]["type"] = "numeric"
        with self.assertRaisesRegex(ValueError, "unsupported Postgres column type"):
            self.transfer()

    def test_omitted_row_fails_source_count_verification(self):
        del self.entries[6]
        with self.assertRaisesRegex(ValueError, "source table inventory"):
            self.transfer()
        self.assertFalse(self.output.exists())

    def test_no_records_after_completion(self):
        self.entries.append({"complete": True})
        with self.assertRaisesRegex(ValueError, "after snapshot completion"):
            self.transfer()

    def test_export_failure_removes_snapshot(self):
        class Failed:
            returncode = 1
        with patch.object(migration.subprocess, "run", return_value=Failed()) as run:
            with self.assertRaisesRegex(ValueError, "Postgres export failed"):
                migration.export_snapshot(self.snapshot, "public")
        self.assertFalse(self.snapshot.exists())
        args, kwargs = run.call_args
        self.assertEqual(args[0], ["psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1"])
        self.assertIn("REPEATABLE READ READ ONLY", kwargs["input"])
        self.assertIn("SET LOCAL row_security = off", kwargs["input"])
        self.assertIn("SET LOCAL bytea_output = 'hex'", kwargs["input"])
        self.assertEqual(kwargs["env"]["PGCLIENTENCODING"], "UTF8")
        self.assertNotIn("PGPASSWORD", " ".join(args[0]))

    def test_existing_export_is_not_overwritten(self):
        self.snapshot.write_text("keep me")
        with self.assertRaises(FileExistsError):
            migration.export_snapshot(self.snapshot, "public")
        self.assertEqual(self.snapshot.read_text(), "keep me")


    @unittest.skipUnless(os.environ.get("SQLITE_MIGRATION_TEST_PG") == "1", "requires an isolated PG fixture via PG* env")
    def test_real_postgres_export_and_import(self):
        schema = "migration_test_" + uuid.uuid4().hex
        ddl = f"""
        CREATE SCHEMA {schema};
        CREATE TABLE {schema}._fluent_migrations (id UUID PRIMARY KEY, name TEXT, batch BIGINT);
        INSERT INTO {schema}._fluent_migrations VALUES ('{ID}', 'App.CreateAccounts', 1);
        CREATE TABLE {schema}.accounts (id UUID PRIMARY KEY, enabled BOOLEAN NOT NULL, created_at TIMESTAMPTZ,
            profile JSONB, secret BYTEA, display TEXT);
        CREATE TABLE {schema}.app_sessions (id UUID PRIMARY KEY, account_id UUID REFERENCES {schema}.accounts(id), payload TEXT);
        CREATE TABLE {schema}.empty_records (id UUID PRIMARY KEY, note TEXT);
        INSERT INTO {schema}.accounts VALUES ('{ID}', true, '2026-11-09T12:30:01.123456Z',
            '{{"tags":["one","two"]}}', decode('00ff4142','hex'), E'private\\n‘quoted’ Ω');
        INSERT INTO {schema}.app_sessions VALUES ('{ID}', '{ID}', '{{"private":"session"}}');
        """
        subprocess.run(["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1"], input=ddl, text=True, check=True, capture_output=True)
        try:
            with contextlib.closing(sqlite3.connect(self.template)) as db:
                db.execute("CREATE TABLE empty_records (id TEXT PRIMARY KEY, note TEXT)")
            migration.export_snapshot(self.snapshot, schema)
            # Exercise actual psql output, including empty-table metadata. psql 14
            # drops the first result if one gexec cell contains multiple SELECTs.
            metadata = {}
            for entry in map(json.loads, self.snapshot.read_text().splitlines()):
                if "table" in entry:
                    metadata[entry["table"]] = entry
                elif "row" in entry:
                    self.assertIn(entry["row"], metadata, "table metadata must precede its rows")
            self.assertEqual(set(metadata), {"_fluent_migrations", "accounts", "app_sessions", "empty_records"})
            self.assertEqual(metadata["empty_records"]["count"], 0)
            self.assertEqual([column["name"] for column in metadata["empty_records"]["columns"]], ["id", "note"])
            report = migration.import_snapshot(self.snapshot, self.template, self.output)
            self.assertEqual(report["tables"]["accounts"]["rows"], 1)
            self.assertEqual(report["tables"]["empty_records"]["rows"], 0)
            with contextlib.closing(sqlite3.connect(self.output)) as db:
                self.assertEqual(db.execute("SELECT display FROM accounts").fetchone()[0], "private\n‘quoted’ Ω")
                self.assertEqual(db.execute("SELECT secret FROM accounts").fetchone()[0], bytes.fromhex("00ff4142"))
                self.assertEqual(db.execute("PRAGMA foreign_key_check").fetchall(), [])
            # A restricted export must fail instead of producing a consistent but partial copy.
            reader = "migration_reader_" + uuid.uuid4().hex
            policies = f"""
                CREATE ROLE {reader};
                GRANT USAGE ON SCHEMA {schema} TO {reader};
                GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO {reader};
                ALTER TABLE {schema}.accounts ENABLE ROW LEVEL SECURITY;
                CREATE POLICY hidden ON {schema}.accounts USING (false);
            """
            subprocess.run(["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1"], input=policies, text=True, check=True, capture_output=True)
            restricted = self.root / "restricted.jsonl"
            try:
                with patch.dict(os.environ, {"PGUSER": reader}):
                    with self.assertRaisesRegex(ValueError, "Postgres export failed"):
                        migration.export_snapshot(restricted, schema)
                self.assertFalse(restricted.exists())
            finally:
                subprocess.run(["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1"], input=f"DROP OWNED BY {reader}; DROP ROLE {reader};", text=True, check=True, capture_output=True)
        finally:
            subprocess.run(["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1"], input=f"DROP SCHEMA {schema} CASCADE;", text=True, check=True, capture_output=True)

    def test_timestamp_offsets_and_invalid_scalars(self):
        self.assertEqual(migration.convert("1970-01-01T01:00:00+01:00", "timestamptz"), 0.0)
        self.assertEqual(migration.convert("1970-01-01T00:00:00", "timestamp"), 0.0)
        for value, kind in [(1, "bool"), (1 << 63, "int8"), (float("nan"), "float8"), ({}, "text")]:
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                migration.convert(value, kind)


if __name__ == "__main__":
    unittest.main()
