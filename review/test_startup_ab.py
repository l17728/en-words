"""Small offline tests. Temporary SQLite files are NOT startup performance samples."""
from pathlib import Path
from contextlib import closing
import sqlite3
import tempfile
import unittest

import run_startup_ab as runner


class StartupABTests(unittest.TestCase):
    def test_frozen_interleaved_pairs_are_complete(self):
        self.assertEqual(runner.ORDER, ('A1', 'B1', 'B2', 'A2', 'A3', 'B3'))
        self.assertEqual(len(set(runner.ORDER)), 6)

    def test_schema_snapshot_preserves_columns_index_and_fk(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'schema.db'
            with closing(sqlite3.connect(path)) as db:
                db.executescript('CREATE TABLE p(id INTEGER PRIMARY KEY); CREATE TABLE c(id INTEGER, p INTEGER REFERENCES p(id)); CREATE UNIQUE INDEX ci ON c(p,id);')
            snap = runner.snapshot(path.resolve())
            self.assertEqual([row[1] for row in snap['tables']['c']['columns']], ['id', 'p'])
            self.assertEqual(snap['tables']['c']['foreign_keys'][0][2], 'p')
            self.assertEqual(snap['tables']['c']['indexes'][0][2], 1)
            self.assertEqual([row[2] for row in snap['tables']['c']['index_columns']['ci'][:2]], ['p', 'id'])

    def test_changed_trigger_is_not_silently_normalized(self):
        with tempfile.TemporaryDirectory() as tmp:
            snapshots = []
            for number in (1, 2):
                path = Path(tmp) / f'{number}.db'
                with closing(sqlite3.connect(path)) as db:
                    db.executescript(f'CREATE TABLE t(n INTEGER); CREATE TRIGGER changed AFTER INSERT ON t BEGIN UPDATE t SET n={number}; END;')
                snapshots.append(runner.snapshot(path.resolve()))
            self.assertNotEqual(snapshots[0], snapshots[1])

    def test_metadata_read_does_not_create_missing_database(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'missing.db'
            with self.assertRaises(sqlite3.OperationalError):
                runner.snapshot(path.resolve())
            self.assertFalse(path.exists())


if __name__ == '__main__':
    unittest.main()
