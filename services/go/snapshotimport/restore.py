"""Restore a validated typed snapshot into an unpublished private SQLite file."""
import base64
import json
import sqlite3
import sys


def quote(value):
    return '"' + value.replace('"', '""') + '"'


def decode(value):
    kind = value['type']
    if kind == 'null':
        return None
    if kind == 'integer':
        return int(value['value'])
    if kind == 'float':
        return float(value['value'])
    if kind == 'blob':
        return base64.b64decode(value['base64'], validate=True)
    if kind == 'text':
        return value['value']
    raise ValueError('unsupported cell')


def main():
    settings = json.load(sys.stdin)
    db = sqlite3.connect(sys.argv[2])
    db.execute('PRAGMA journal_mode=DELETE')
    db.execute('PRAGMA foreign_keys=ON')
    db.executescript(settings['baseline'])
    db.execute('BEGIN IMMEDIATE')
    db.execute('PRAGMA defer_foreign_keys=ON')
    with open(sys.argv[1], encoding='utf-8') as source:
        header = json.loads(next(source))
        columns = {t['name']: t['columns'] for t in header['tables']}
        complete = False
        for line in source:
            record = json.loads(line)
            if complete:
                raise ValueError('trailing data')
            if record.get('complete'):
                complete = True
                continue
            name = record['table']
            names = columns[name]
            db.execute('INSERT INTO ' + quote(name) + '(' + ','.join(quote(n) for n in names) + ') VALUES(' + ','.join('?' for _ in names) + ')', [decode(v) for v in record['values']])
        if not complete:
            raise ValueError('incomplete snapshot')
    if db.execute('PRAGMA foreign_key_check').fetchone() is not None:
        raise ValueError('foreign keys')
    db.commit()
    if db.execute('PRAGMA integrity_check').fetchall() != [('ok',)]:
        raise ValueError('integrity')
    if db.execute('PRAGMA foreign_key_check').fetchone() is not None:
        raise ValueError('foreign keys')
    db.close()


try:
    main()
except Exception:
    # Never expose SQLite errors containing private values or SQL.
    raise SystemExit(2)
