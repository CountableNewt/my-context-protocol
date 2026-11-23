#!/usr/bin/env python3
"""Regression checks for the draft-contract release gate and reference checks."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import sys
import unittest

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('check_lexicons', ROOT / 'scripts/check-lexicons.py')
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class ContractChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.lexicons = self.root / 'lexicons'
        self.lexicons.mkdir()
        for source in (ROOT / 'lexicons').glob('*.json'):
            (self.lexicons / source.name).write_text(source.read_text())
        self.manifest = json.loads((self.lexicons / 'operations.json').read_text())
        for item in self.manifest['operations'] + self.manifest['exceptions']:
            source = self.root / item['source']
            source.parent.mkdir(parents=True, exist_ok=True)
            source.touch()
        for name in self.manifest['frontend']['sources']:
            source = self.root / name
            source.parent.mkdir(parents=True, exist_ok=True)
            source.touch()

    def save(self):
        (self.lexicons / 'operations.json').write_text(json.dumps(self.manifest))

    def test_current_inventory_is_consistent(self):
        schemas, operations, pending = checker.check(self.root)
        self.assertGreater(schemas, 0)
        self.assertGreaterEqual(schemas, operations)
        self.assertEqual(pending, 0)

    def test_unimplemented_operations_cannot_pass_release_gate(self):
        with self.assertRaisesRegex(AssertionError, 'incomplete operation'):
            checker.check(self.root, require_complete=True)

    def test_duplicate_route_rejected(self):
        duplicate = copy.deepcopy(self.manifest['operations'][0])
        duplicate['nsid'] += 'Duplicate'
        self.manifest['operations'].append(duplicate)
        self.save()
        with self.assertRaisesRegex(AssertionError, 'duplicate route'):
            checker.check(self.root)

    def test_frontend_route_drift_rejected(self):
        path = self.root / 'apps/web/app/api/new/route.ts'
        path.parent.mkdir(parents=True)
        path.touch()
        with self.assertRaisesRegex(AssertionError, 'route inventory drift'):
            checker.check(self.root)

    def test_unresolved_reference_rejected(self):
        path = self.lexicons / 'dev.mycontextprotocol.project.getProject.json'
        value = json.loads(path.read_text())
        value['defs']['main']['output']['schema']['ref'] = 'dev.mycontextprotocol.project.missing#view'
        path.write_text(json.dumps(value))
        with self.assertRaisesRegex(AssertionError, 'unresolved reference'):
            checker.check(self.root)

    def test_draft_requires_matching_schema(self):
        (self.lexicons / 'dev.mycontextprotocol.project.getProject.json').unlink()
        with self.assertRaisesRegex(AssertionError, 'missing draft schema'):
            checker.check(self.root)


if __name__ == '__main__':
    unittest.main()
