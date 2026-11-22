#!/usr/bin/env python3
"""Check local draft contract consistency; not a substitute for ATProto validation."""
import argparse
import json
from pathlib import Path
import re
import sys

NSID = re.compile(r'^dev\.mycontextprotocol\.(?:[a-z][a-z0-9]*\.)+[a-zA-Z][a-zA-Z0-9]*$')
TYPES = {'boolean', 'integer', 'string', 'bytes', 'cid-link', 'blob', 'array', 'object', 'params', 'token', 'ref', 'union', 'unknown', 'record', 'query', 'procedure', 'subscription', 'permission-set'}


def check(root, require_complete=False):
    directory = root / 'lexicons'
    manifest = json.loads((directory / 'operations.json').read_text())
    schemas = {}
    for path in directory.glob('*.json'):
        if path.name == 'operations.json':
            continue
        value = json.loads(path.read_text())
        assert value.get('lexicon') == 1, f'{path}: unsupported Lexicon version'
        nsid = value.get('id', '')
        assert NSID.fullmatch(nsid), f'{path}: invalid product NSID'
        assert path.stem == nsid, f'{path}: filename must equal NSID'
        assert isinstance(value.get('defs'), dict) and value['defs'], f'{path}: definitions required'
        assert nsid not in schemas, f'{path}: duplicate NSID'
        schemas[nsid] = value

    def walk(node, owner):
        if not isinstance(node, dict):
            return
        kind = node.get('type')
        assert kind in TYPES, f'{owner}: invalid definition type {kind}'
        if kind in ('object', 'params'):
            assert isinstance(node.get('properties'), dict), f'{owner}: properties required'
            assert set(node.get('required', [])) <= node['properties'].keys(), f'{owner}: unknown required property'
            for child in node['properties'].values():
                walk(child, owner)
        elif kind == 'array':
            walk(node['items'], owner)
        elif kind == 'ref':
            target, _, fragment = node['ref'].partition('#')
            target = target or owner
            assert target in schemas and (fragment or 'main') in schemas[target]['defs'], f'{owner}: unresolved reference {node["ref"]}'
        elif kind in ('query', 'procedure'):
            assert kind != 'query' or 'input' not in node, f'{owner}: query has input body'
            if 'parameters' in node:
                assert node['parameters'].get('type') == 'params'
                walk(node['parameters'], owner)
            for direction in ('input', 'output'):
                if direction in node:
                    assert node[direction].get('encoding') == 'application/json'
                    walk(node[direction]['schema'], owner)

    for nsid, value in schemas.items():
        for definition in value['defs'].values():
            walk(definition, nsid)
    routes, names = set(), set()
    pending = 0
    for item in manifest['operations']:
        route = (item['service'], item['method'], item['path'])
        assert route not in routes, f'duplicate route: {route}'
        routes.add(route)
        assert NSID.fullmatch(item['nsid']), f'invalid operation NSID: {item["nsid"]}'
        assert item['nsid'] not in names, f'duplicate operation NSID: {item["nsid"]}'
        names.add(item['nsid'])
        assert (root / item['source']).is_file(), f'missing owning source: {item["source"]}'
        assert item['authorization'] in ('account', 'admin', 'internal', 'tenant')
        if item['contract'] == 'draft':
            assert item['nsid'] in schemas, f'missing draft schema: {item["nsid"]}'
            expected = 'query' if item['method'] == 'GET' else 'procedure'
            assert schemas[item['nsid']]['defs']['main']['type'] == expected
        else:
            assert item['contract'] == 'pending'
            pending += 1
        assert item['runtime'] in ('pending', 'implemented')
        assert item['publication'] in ('pending', 'published')
        if require_complete:
            assert item['contract'] == 'draft' and item['runtime'] == 'implemented' and item['publication'] == 'published', f'incomplete operation: {item["nsid"]}'
    for item in manifest['exceptions']:
        assert item['reason'] and (root / item['source']).is_file()
    frontend = sorted(str(p.relative_to(root)) for p in (root / 'apps/web/app/api').rglob('route.ts'))
    assert frontend == manifest['frontend']['sources'], 'frontend API route inventory drift'
    assert manifest['namespace'] == 'dev.mycontextprotocol'
    return len(schemas), len(routes), pending


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--require-complete', action='store_true', help='Fail until every operation is implemented and published (release gate).')
    args = parser.parse_args()
    try:
        schemas, routes, pending = check(Path(__file__).resolve().parents[1], args.require_complete)
    except (AssertionError, KeyError, ValueError) as error:
        print(f'Lexicon check failed: {error}', file=sys.stderr)
        return 1
    print(f'Checked {schemas} draft schemas and {routes} operation mappings; {pending} contracts remain pending.')
    return 0


if __name__ == '__main__':
    sys.exit(main())
