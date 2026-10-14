import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import download_funasr_cpu_model as downloader


class ModelDownload(unittest.TestCase):
    def test_installer_lock_prevents_any_model_download(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'operation.lock').write_text('installer')
            with patch.object(downloader, 'ROOT', root), patch.object(downloader, 'Budget') as budget:
                with self.assertRaises(FileExistsError):
                    downloader.main()
                budget.assert_not_called()
            self.assertEqual((root / 'operation.lock').read_text(), 'installer')


if __name__ == '__main__':
    unittest.main()
