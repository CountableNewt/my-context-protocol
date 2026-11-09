#!/usr/bin/env python3
"""Offline, fail-closed transfer into a SQLite schema migrated by the matching Gateway.

No credentials are command arguments: export uses libpq's PG* environment variables.
Snapshots contain secrets and are created with mode 0600. No live writes/cutover occur.
"""
from __future__ import annotations

import argparse
from collections import Counter
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import uuid

FORMAT = "mycontext-postgres-snapshot-v1"
MIGRATIONS = "_fluent_migrations"
MODULUS = 1 << 256


def quoted(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def literal(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def json_line(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def export_sql(schema: str) -> str:
    # All generated queries execute in this same read-only repeatable-read snapshot.
    # Each gexec result cell must contain one statement: older psql clients only
    # print the last result of a multi-statement cell, silently omitting metadata.
    return f"""\\set ON_ERROR_STOP on
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'UTC';
SET LOCAL row_security = off;
SET LOCAL bytea_output = 'hex';
SET LOCAL extra_float_digits = 3;
SELECT json_build_object('format', '{FORMAT}', 'schema', {literal(schema)});
SELECT format(
  'SELECT json_build_object(''table'', %L, ''count'', (SELECT count(*) FROM %I.%I), ''columns'', (SELECT json_agg(json_build_object(''name'', column_name, ''type'', udt_name) ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema = %L AND table_name = %L))',
  table_name, table_schema, table_name, table_schema, table_name) AS metadata_sql,
  format(
    'SELECT json_build_object(''row'', %L, ''values'', row_to_json(t)) FROM %I.%I t',
    table_name, table_schema, table_name) AS rows_sql
FROM information_schema.tables
WHERE table_schema = {literal(schema)} AND table_type = 'BASE TABLE'
ORDER BY table_name
\\gexec
SELECT json_build_object('complete', true);
COMMIT;
"""


def export_snapshot(output: Path, schema: str) -> None:
    fd = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as target:
            # Keep stderr private; database errors can contain credentials/data.
            with tempfile.TemporaryFile(mode="w+") as errors:
                result = subprocess.run(
                    ["psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1"],
                    input=export_sql(schema), text=True, stdout=target, stderr=errors,
                    env={**os.environ, "PGOPTIONS": "-c default_transaction_read_only=on", "PGCLIENTENCODING": "UTF8"},
                    check=False,
                )
                if result.returncode:
                    raise ValueError("Postgres export failed; verify libpq PG* connection settings and read permissions")
            target.flush()
            os.fsync(target.fileno())
    except BaseException:
        output.unlink(missing_ok=True)
        raise


def convert(value: object, pg_type: str) -> object:
    if value is None:
        return None
    if pg_type == "uuid":
        return str(uuid.UUID(str(value))).upper()
    if pg_type == "bool":
        if type(value) is not bool:
            raise ValueError("invalid boolean")
        return int(value)
    if pg_type in {"timestamp", "timestamptz"}:
        if not isinstance(value, str):
            raise ValueError("invalid timestamp")
        parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=dt.timezone.utc)
        # SQLiteKit stores Foundation Date as seconds since the Unix epoch.
        return parsed.timestamp()
    if pg_type in {"json", "jsonb"}:
        return json_line(value)
    if pg_type == "bytea":
        if not isinstance(value, str) or not value.startswith("\\x"):
            raise ValueError("invalid hex bytea")
        return bytes.fromhex(value[2:])
    if pg_type in {"int2", "int4", "int8"}:
        if type(value) is not int or not -(1 << 63) <= value < (1 << 63):
            raise ValueError("integer is not representable by SQLite")
        return value
    if pg_type in {"float4", "float8"}:
        if type(value) not in {int, float} or not math.isfinite(value):
            raise ValueError("non-finite or invalid float")
        return float(value)
    if pg_type in {"text", "varchar", "bpchar", "name"}:
        if not isinstance(value, str):
            raise ValueError("invalid text")
        return value
    raise ValueError(f"unsupported Postgres column type: {pg_type}")


def row_hash(values: list[object] | tuple[object, ...]) -> int:
    # Normalize SQLite's equivalent integer/real storage; preserve blobs explicitly.
    encoded = [{"blob": x.hex()} if isinstance(x, bytes) else x for x in values]
    return int.from_bytes(hashlib.sha256(json_line(encoded).encode()).digest(), "big")


def digest(count: int, total: int) -> str:
    return hashlib.sha256(f"{count}:{total % MODULUS:064x}".encode()).hexdigest()


def import_snapshot(snapshot: Path, template: Path, output: Path) -> dict:
    if output.exists():
        raise ValueError("output already exists; refusing to overwrite")
    if not template.is_file():
        raise ValueError("schema template does not exist")
    # Same filesystem as output permits atomic publication without replacing an existing file.
    fd, temporary = tempfile.mkstemp(prefix=".sqlite-import-", dir=output.parent)
    os.close(fd)
    temporary_path = Path(temporary)
    try:
        source = sqlite3.connect(template.resolve().as_uri() + "?mode=ro", uri=True)
        target = sqlite3.connect(temporary_path)
        try:
            source.backup(target)
            source.close()
            target.execute("PRAGMA journal_mode=DELETE")
            tables = {r[0] for r in target.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}
            if MIGRATIONS not in tables:
                raise ValueError("template has no Fluent migration history")
            expected_migrations = Counter(r[0] for r in target.execute(f'SELECT name FROM {quoted(MIGRATIONS)}'))
            if not expected_migrations:
                raise ValueError("template has an empty Fluent migration history")
            application_tables = tables - {MIGRATIONS}
            for table in application_tables:
                if target.execute(f"SELECT 1 FROM {quoted(table)} LIMIT 1").fetchone():
                    raise ValueError(f"schema template contains application data: {table}")
            target.execute("PRAGMA foreign_keys=OFF")
            target.execute("BEGIN")
            seen: set[str] = set()
            metadata: dict[str, list[dict]] = {}
            expected: dict[str, list[int]] = {}
            exported_counts: dict[str, int] = {}
            received_counts: Counter = Counter()
            source_migrations: Counter = Counter()
            complete = False
            with snapshot.open(encoding="utf-8") as stream:
                header = json.loads(next(stream))
                if header.get("format") != FORMAT:
                    raise ValueError("unsupported snapshot format")
                for line in stream:
                    entry = json.loads(line)
                    if complete:
                        raise ValueError("data appears after snapshot completion marker")
                    if entry.get("complete") is True:
                        complete = True
                    elif "table" in entry:
                        table = entry["table"]
                        if table in seen or table not in tables:
                            raise ValueError(f"duplicate or unexpected source table: {table}")
                        seen.add(table)
                        columns = entry["columns"]
                        names = [c["name"] for c in columns]
                        target_names = [r[1] for r in target.execute(f"PRAGMA table_info({quoted(table)})")]
                        if len(names) != len(set(names)) or set(names) != set(target_names):
                            raise ValueError(f"column mismatch: {table}")
                        if type(entry.get("count")) is not int or entry["count"] < 0:
                            raise ValueError("missing or invalid source table count")
                        exported_counts[table] = entry["count"]
                        metadata[table] = columns
                        expected[table] = [0, 0]
                    elif "row" in entry:
                        table = entry["row"]
                        if table not in metadata:
                            raise ValueError("row appears before table metadata")
                        columns = metadata[table]
                        received_counts[table] += 1
                        row = entry["values"]
                        if set(row) != {c["name"] for c in columns}:
                            raise ValueError(f"row column mismatch: {table}")
                        if table == MIGRATIONS:
                            source_migrations[row["name"]] += 1
                            continue
                        values = [convert(row[c["name"]], c["type"]) for c in columns]
                        target.execute(f"INSERT INTO {quoted(table)} ({','.join(quoted(c['name']) for c in columns)}) VALUES ({','.join('?' for _ in values)})", values)
                        expected[table][0] += 1
                        expected[table][1] = (expected[table][1] + row_hash(values)) % MODULUS
                    else:
                        raise ValueError("unrecognized snapshot record")
            if not complete or seen != tables:
                raise ValueError("incomplete snapshot or table inventory mismatch")
            if any(received_counts[t] != exported_counts[t] for t in seen):
                raise ValueError("snapshot row counts differ from source table inventory")
            if source_migrations != expected_migrations:
                raise ValueError("source and template migration histories differ; build both from the same revision")
            report = {"format": FORMAT, "tables": {}}
            for table in sorted(application_tables):
                columns = metadata[table]
                count, total = 0, 0
                query = f"SELECT {','.join(quoted(c['name']) for c in columns)} FROM {quoted(table)}"
                for row in target.execute(query):
                    # Floating column affinities can change integer values to REAL.
                    normalized = [float(v) if v is not None and c["type"] in {"timestamp", "timestamptz", "float4", "float8"} else v for c, v in zip(columns, row)]
                    count += 1
                    total = (total + row_hash(normalized)) % MODULUS
                if [count, total] != expected[table]:
                    raise ValueError(f"row verification failed: {table}")
                report["tables"][table] = {"rows": count, "multiset_sha256": digest(count, total)}
            if target.execute("PRAGMA foreign_key_check").fetchone():
                raise ValueError("foreign key verification failed")
            if target.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
                raise ValueError("SQLite integrity verification failed")
            target.commit()
        finally:
            source.close()
            target.close()
        with temporary_path.open("rb") as verified:
            os.fsync(verified.fileno())
        os.link(temporary_path, output)  # Atomic no-clobber, including a concurrent creator.
        return report
    finally:
        temporary_path.unlink(missing_ok=True)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    export = commands.add_parser("export", help="read-only Postgres snapshot using libpq PG* env")
    export.add_argument("--output", type=Path, required=True)
    export.add_argument("--schema", default="public")
    transfer = commands.add_parser("import", help="create a verified SQLite file without modifying the template")
    transfer.add_argument("--snapshot", type=Path, required=True)
    transfer.add_argument("--template", type=Path, required=True)
    transfer.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "export":
            export_snapshot(args.output, args.schema)
            print("Snapshot exported. Treat this file as a database backup containing secrets.")
        else:
            print(json.dumps(import_snapshot(args.snapshot, args.template, args.output), indent=2))
    except (ValueError, OSError, sqlite3.Error, json.JSONDecodeError, StopIteration) as error:
        # Never print database exceptions: they may contain private row values.
        parser.exit(1, f"Migration failed ({type(error).__name__}); output was not published. Verify schema, snapshot completeness and permissions.\n")


if __name__ == "__main__":
    main()
