"""安装入口的离线失败关闭测试；真实发行下载另作实机验收，不在 CI 请求网络。"""
import contextlib
import io
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name('install-node.sh')
PROGRAM = compile(SCRIPT.read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0], str(SCRIPT), 'exec')


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.parent = Path(self.tmp.name).resolve()
        self.parent.chmod(0o700)
        self.target = self.parent / 'install'
        self.calls = []
        self.addCleanup(self.tmp.cleanup)

    def invoke(self, args=None, which=lambda name: '/test/' + name, version=b'v24.15.0', run=None):
        output = io.StringIO()
        def denied(*args, **kwargs):
            self.calls.append(args)
            raise AssertionError('unexpected subprocess')
        with patch('sys.argv', ['install', *(args if args is not None else ['--prefix', str(self.target)])]), \
             patch('shutil.which', side_effect=which), \
             patch('subprocess.check_output', return_value=version), \
             patch('subprocess.run', side_effect=run or denied), \
             patch('sys.platform', 'linux'), patch('os.uname', return_value=type('U', (), {'machine': 'x86_64'})()), \
             contextlib.redirect_stderr(output), contextlib.redirect_stdout(output):
            with self.assertRaises(SystemExit) as stopped:
                exec(PROGRAM, {})
        self.assertEqual(stopped.exception.code, 1)
        return output.getvalue()

    def test_help_without_runtime(self):
        result = subprocess.run(['/bin/bash', str(SCRIPT), '--help'], env={'PATH': '/nonexistent'}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('仅安装', result.stdout)

    def test_unknown_arguments(self):
        self.assertIn('参数错误', self.invoke(['--version', 'latest']))

    def test_missing_dependency(self):
        self.assertIn('缺少 minisign', self.invoke(which=lambda n: None if n == 'minisign' else '/test/' + n))

    def test_wrong_node(self):
        self.assertIn('Node 24.15.0', self.invoke(version=b'v22.0.0'))

    def test_existing_target_preserved(self):
        self.target.mkdir()
        marker = self.target / 'user-file'
        marker.write_text('keep')
        self.assertIn('拒绝覆盖', self.invoke())
        self.assertEqual(marker.read_text(), 'keep')

    def test_symlink_target_rejected(self):
        self.target.symlink_to(self.parent / 'missing')
        self.assertIn('规范路径', self.invoke())
        self.assertTrue(self.target.is_symlink())

    def test_public_parent_rejected(self):
        self.parent.chmod(0o755)
        self.assertIn('0700', self.invoke())
        self.assertFalse(self.target.exists())

    def test_signature_failure_precedes_zip_or_execution(self):
        def run(argv, **kwargs):
            self.calls.append(argv)
            if argv[0] == '/test/curl':
                Path(argv[argv.index('--output') + 1]).write_bytes(b'invalid')
            else:
                self.assertEqual(argv[0], '/test/minisign')
                self.assertIn('RWQAYJ7SGGbem0iFIm1Hjh8837yNiXVQajajH8efRf3E2ziZi7itc1Nq', argv)
                raise subprocess.CalledProcessError(1, argv)
        self.invoke(run=run)
        self.assertEqual(len(self.calls), 3)
        self.assertFalse(self.target.exists())

    def test_signed_but_different_release_fails_before_zip(self):
        def run(argv, **kwargs):
            self.calls.append(argv)
            if argv[0] == '/test/curl':
                Path(argv[argv.index('--output') + 1]).write_bytes(b'0' * 64 + b'  different.zip\n')
        self.assertIn('发行身份不一致', self.invoke(run=run))
        self.assertEqual(len(self.calls), 3)
        self.assertFalse(self.target.exists())


if __name__ == '__main__':
    unittest.main()
