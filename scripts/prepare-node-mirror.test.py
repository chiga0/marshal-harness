import importlib.util
import pathlib
import subprocess
import tempfile
import sys
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('prepare', pathlib.Path(__file__).with_name('prepare-node-mirror.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PreparationTests(unittest.TestCase):
    def test_wrong_tag_no_network(self):
        with patch('subprocess.check_output') as call:
            with self.assertRaises(ValueError):
                module.prepare('v0.0.0', '/unused')
            call.assert_not_called()

    def test_draft_or_prerelease_no_download(self):
        for metadata in (b'{"tagName":"v1.0.1","isDraft":true,"isPrerelease":false}',
                         b'{"tagName":"v1.0.1","isDraft":false,"isPrerelease":true}'):
            with patch('subprocess.check_output', return_value=metadata), patch('subprocess.run') as call:
                with self.assertRaises(ValueError):
                    module.prepare('v1.0.1', '/unused')
                call.assert_not_called()

    def test_fixed_sources_and_installer_copy(self):
        with tempfile.TemporaryDirectory() as parent, \
             patch('subprocess.check_output', return_value=b'{"tagName":"v1.0.1","isDraft":false,"isPrerelease":false}'), \
             patch('subprocess.run') as call:
            stage = module.prepare('v1.0.1', parent)
            self.assertEqual(call.call_count, 5)
            for args in call.call_args_list:
                command = args.args[0]
                self.assertIn('--retry-all-errors', command)
                self.assertTrue(command[-1].startswith('https://'))
            self.assertEqual((stage / 'install-node.sh').read_bytes(),
                             pathlib.Path(__file__).with_name('install-node.sh').read_bytes())


if __name__ == '__main__':
    unittest.main()
