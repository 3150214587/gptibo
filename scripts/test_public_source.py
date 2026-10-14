"""Regression checks for accidental inclusion of private release artifacts."""
import hashlib
import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('public_source', Path(__file__).with_name('scan-public-source.py'))
scanner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scanner)


class PublicSourceTests(unittest.TestCase):
    def test_private_sidecars_and_exports_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            names = ['store.db-wal', 'store.db-shm', 'store.sqlite3', 'store.sqlite-wal',
                     'store.sqlite3-shm', 'order.snapshot.json', 'credentials.keystore',
                     'config.bak', '.env.production', '.dev.vars', '.secrets/token']
            for name in names:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('private fixture')
            findings = scanner.scan(root, names)
            self.assertEqual({item[0] for item in findings}, set(names))
            self.assertTrue(all(item[2] == 'private/runtime artifact' for item in findings))

    def test_replacing_reviewed_png_cannot_bypass_scan(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = 'public/email-logo.png'
            target = root / name
            target.parent.mkdir()
            original = Path(__file__).resolve().parents[1] / name
            data = original.read_bytes()
            self.assertEqual(hashlib.sha256(data).hexdigest(), scanner.BINARY_ASSETS[name])
            target.write_bytes(data)
            self.assertEqual(scanner.scan(root, [name]), [])
            for replacement in [data + b'private metadata', b'private text replacing image']:
                target.write_bytes(replacement)
                self.assertEqual(scanner.scan(root, [name])[0][2], 'unreviewed binary asset')

    def test_symlink_cannot_import_private_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'linked.txt').symlink_to(root / 'missing-private-file')
            self.assertEqual(scanner.scan(root, ['linked.txt'])[0][2], 'symlink forbidden in public export')

    def test_internal_addresses_require_review_and_values_stay_hidden(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = 'config.txt'
            (root / name).write_text('\n'.join('https://' + host + '/api' for host in ['10.23.45.67', '[fd00::1234]']))
            findings = scanner.scan(root, [name])
            self.assertEqual(findings, [(name, 1, 'unexpected external host'),
                                        (name, 2, 'unexpected external host')])

    def test_blank_examples_and_reserved_endpoints_are_allowed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = '.env.integrations.example'
            (root / name).write_text('API_KEY=\nAPI_BASE=https://supply.example.invalid/v1\n')
            self.assertEqual(scanner.scan(root, [name]), [])


if __name__ == '__main__':
    unittest.main()
