"""Read a standalone SQLite backup in one read-only transaction; emit typed JSONL."""
import base64
import json
import math
import pathlib
import re
import sqlite3
import sys


def quoted(value):
    return '"' + value.replace('"', '""') + '"'


def normalized(value):
    return ' '.join(re.sub(r'\bIF\s+NOT\s+EXISTS\s+', '', value, flags=re.I).split())


def objects(db):
    return dict(db.execute("SELECT name,sql FROM sqlite_schema WHERE type IN ('table','index','view','trigger') AND name NOT LIKE 'sqlite_%'"))


def cell(value):
    if value is None:
        return {'type': 'null'}
    if isinstance(value, int):
        if not -(1 << 63) <= value < (1 << 63):
            raise ValueError('integer range')
        return {'type': 'integer', 'value': str(value)}
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError('non-finite real')
        return {'type': 'float', 'value': value}
    if isinstance(value, bytes):
        return {'type': 'blob', 'base64': base64.b64encode(value).decode('ascii')}
    if isinstance(value, str):
        return {'type': 'text', 'value': value}
    raise ValueError('unsupported SQLite value')


def emit(value):
    print(json.dumps(value, ensure_ascii=False, separators=(',', ':'), allow_nan=False))


def main():
    settings = json.load(sys.stdin)
    baseline = settings['baseline']
    expected = sqlite3.connect(':memory:')
    expected.executescript(baseline)
    expected_objects = objects(expected)
    path = pathlib.Path(sys.argv[1]).resolve()
    # A copied live .sqlite without its WAL is not a consistent backup. Require
    # the operator to use SQLite's backup API before this offline export.
    wal = pathlib.Path(str(path) + '-wal')
    if wal.exists() and wal.stat().st_size:
        raise ValueError('standalone backup required')
    source = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True)
    source.execute('PRAGMA query_only=ON')
    source.execute('BEGIN')
    if source.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
        raise ValueError('source integrity')
    if source.execute('PRAGMA foreign_key_check').fetchone() is not None:
        raise ValueError('source foreign keys')
    actual = objects(source)
    if not set(expected_objects).issubset(actual):
        raise ValueError('source baseline missing')
    if any(name not in settings['allowed'] or normalized(sql) != normalized(settings['allowed'][name]) for name, sql in actual.items()):
        raise ValueError('source schema is not shipped')
    actual = {name: sql for name, sql in actual.items() if name not in settings['excluded']}
    names = sorted(row[0] for row in source.execute("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'") if row[0] not in settings['excluded'])
    tables = []
    for name in names:
        columns = [row[1] for row in source.execute('PRAGMA table_info(' + quoted(name) + ')')]
        count = source.execute('SELECT COUNT(*) FROM ' + quoted(name)).fetchone()[0]
        tables.append({'name': name, 'columns': columns, 'rows': count})
    legacy = set(actual) == set(expected_objects)
    emit({'format': 'mycontext-sqlite-snapshot-v1' if legacy else 'mycontext-sqlite-snapshot-v2', 'schema': actual, 'tables': tables})
    for table in tables:
        columns = ','.join(quoted(name) for name in table['columns'])
        query = 'SELECT ' + columns + ' FROM ' + quoted(table['name']) + ' ORDER BY rowid'
        for row in source.execute(query):
            emit({'table': table['name'], 'values': [cell(value) for value in row]})
    emit({'complete': True})
    source.rollback()
    source.close()
    expected.close()


try:
    main()
except Exception:
    # SQL errors and malformed text can contain private row contents. The Go
    # caller reports a safe inspection error, never this process's raw stderr.
    raise SystemExit(2)
