"""安装入口的离线失败关闭测试；真实发行下载另作实机验收，不在 CI 请求网络。"""
import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile
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

    def invoke(self, args=None, which=lambda name: '/test/' + name, version=b'v24.15.0', run=None, program=PROGRAM, success=False):
        output = io.StringIO()
        def denied(*args, **kwargs):
            self.calls.append(args)
            raise AssertionError('unexpected subprocess')
        with patch('sys.argv', ['install', *(args if args is not None else ['--prefix', str(self.target)])]), \
             patch('shutil.which', side_effect=which), \
             patch('subprocess.check_output', return_value=version), \
             patch('subprocess.run', side_effect=run or denied), \
             patch('time.sleep'), \
             patch('sys.platform', 'linux'), patch('os.uname', return_value=type('U', (), {'machine': 'x86_64'})()), \
             contextlib.redirect_stderr(output), contextlib.redirect_stdout(output):
            if success:
                exec(program, {})
            else:
                with self.assertRaises(SystemExit) as stopped:
                    exec(program, {})
                self.assertEqual(stopped.exception.code, 1)
        return output.getvalue()

    def fixture(self):
        # 仅替换测试程序中的固定摘要；生产 pin 不变。恢复由 stub 截获，
        # 本测试证明获取/验签调用/摘要与 carrier 验证路径，不声称真实发行验收。
        namespace = {}
        text = SCRIPT.read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
        exec(text.split('try:\n    args', 1)[0], namespace)
        payload = b'fixture entrypoint'
        manifest = json.dumps({'sourceHead': namespace['SOURCE'], 'entrypoint': 'main.mjs', 'files': [
            {'path': 'main.mjs', 'bytes': len(payload), 'digest': 'sha256:' + hashlib.sha256(payload).hexdigest()}
        ]}).encode()
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, 'w') as archive:
            archive.writestr('main.mjs', payload)
            archive.writestr('manifest.json', manifest)
        assets = {namespace['ZIP']: buffer.getvalue(), 'manifest.json': manifest, 'distribution.mjs': b'fixture helper'}
        for constant, filename in [('ZIP_SHA', namespace['ZIP']), ('MANIFEST_SHA', 'manifest.json'), ('HELPER_SHA', 'distribution.mjs')]:
            text = text.replace(namespace[constant], hashlib.sha256(assets[filename]).hexdigest())
        assets['SHA256SUMS'] = ''.join(hashlib.sha256(assets[name]).hexdigest() + '  ' + name + '\n' for name in (namespace['ZIP'], 'manifest.json')).encode()
        assets['SHA256SUMS.minisig'] = b'fixture signature'
        return compile(text, str(SCRIPT), 'exec'), assets

    def source_run(self, assets):
        def run(argv, **kwargs):
            self.calls.append(argv)
            if argv[0] == '/test/curl':
                dest = Path(argv[argv.index('--output') + 1])
                dest.write_bytes(assets[dest.name])
            elif argv[0] == '/test/node':
                self.assertEqual((Path(argv[5]) / 'main.mjs').read_bytes(), b'fixture entrypoint')
            else:
                self.assertEqual(argv[0], '/test/minisign')
        return run

    def test_help_without_runtime(self):
        result = subprocess.run(['/bin/bash', str(SCRIPT), '--help'], env={'PATH': '/nonexistent'}, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('仅安装', result.stdout)

    def test_unknown_arguments(self):
        self.assertIn('参数错误', self.invoke(['--version', 'latest']))

    def test_parameter_validation(self):
        for args in (['--prefix'], ['--base-url'], ['--offline-dir'],
                     ['--prefix', 'one', '--prefix', 'two'], ['--offline-dir', ''],
                     ['--base-url', '--prefix'], ['--base-url', 'https://example.org/', '--offline-dir', '.']):
            with self.subTest(args=args):
                self.invoke(args)
                self.assertEqual(self.calls, [])
                self.assertEqual(list(self.parent.iterdir()), [])

    def test_mirror_url_validation_never_echoes_secrets(self):
        for url in ('http://example.org/v1/', 'https://user:SECRET@example.org/',
                    'https://example.org/?SECRET', 'https://example.org/#SECRET',
                    'https://example.org:SECRET/', 'https://[SECRET/', 'https://example.org/\nSECRET',
                    'https://example.org\\SECRET/', 'https://@example.org/', 'https:///SECRET'):
            with self.subTest(url=url):
                output = self.invoke(['--base-url', url])
                self.assertIn('镜像地址无效', output)
                self.assertNotIn('SECRET', output)
                self.assertEqual(self.calls, [])

    def test_mirror_and_default_all_five_asset_urls(self):
        program, assets = self.fixture()
        for base in (None, 'https://mirror.example/releases/v1.0.1'):
            self.calls = []
            args = ['--prefix', str(self.target)] + (['--base-url', base] if base else [])
            self.invoke(args, program=program, success=True, run=self.source_run(assets))
            downloads = [call for call in self.calls if call[0] == '/test/curl']
            self.assertEqual(len(downloads), 5)
            for call in downloads:
                name = Path(call[call.index('--output') + 1]).name
                if base:
                    self.assertEqual(call[-1], base + '/' + name)
                else:
                    self.assertEqual(call[-1], 'https://github-releases.oss-cn-hangzhou.aliyuncs.com/marshal-harness/v1.0.1/' + name)
                self.assertEqual(call[call.index('--proto') + 1], '=https')
                self.assertEqual(call[call.index('--proto-redir') + 1], '=https')

    def test_offline_full_verification_without_curl(self):
        program, assets = self.fixture()
        offline = self.parent / 'offline'
        offline.mkdir()
        for name, data in assets.items():
            (offline / name).write_bytes(data)
        self.invoke(['--offline-dir', str(offline), '--prefix', str(self.target)], program=program,
                    success=True, which=lambda n: None if n == 'curl' else '/test/' + n,
                    run=self.source_run(assets))
        self.assertEqual([call[0] for call in self.calls], ['/test/minisign', '/test/node'])
        evidence = next(self.parent.glob('.marshal-download-*'))
        for name, data in assets.items():
            self.assertEqual((evidence / name).read_bytes(), data)

    def test_offline_missing_symlink_fifo_directory_and_oversize_fail_closed(self):
        offline = self.parent / 'offline'
        offline.mkdir()
        candidate = offline / 'SHA256SUMS'
        args = ['--prefix', str(self.target), '--offline-dir', str(offline)]
        self.assertIn('SHA256SUMS', self.invoke(args))
        candidate.symlink_to(self.parent / 'missing')
        self.assertIn('SHA256SUMS', self.invoke(args))
        candidate.unlink()
        os.mkfifo(candidate)
        self.assertIn('类型或大小无效', self.invoke(args))
        candidate.unlink()
        candidate.mkdir()
        self.assertIn('SHA256SUMS', self.invoke(args))
        candidate.rmdir()
        candidate.write_bytes(b'x' * 65537)
        self.assertIn('类型或大小无效', self.invoke(args))
        self.assertEqual(self.calls, [])
        self.assertFalse(self.target.exists())

    def test_offline_corrupt_helper_prevents_execution(self):
        program, assets = self.fixture()
        offline = self.parent / 'offline'
        offline.mkdir()
        for name, data in assets.items():
            (offline / name).write_bytes(b'corrupt' if name == 'distribution.mjs' else data)
        output = self.invoke(['--prefix', str(self.target), '--offline-dir', str(offline)],
                             program=program, run=self.source_run(assets))
        self.assertIn('固定恢复器摘要错误', output)
        self.assertEqual([call[0] for call in self.calls], ['/test/minisign'])

    def test_offline_other_tampering_and_signature_fail_closed(self):
        program, assets = self.fixture()
        offline = self.parent / 'offline'
        offline.mkdir()
        args = ['--prefix', str(self.target), '--offline-dir', str(offline)]
        zip_name = next(name for name in assets if name.endswith('.zip'))
        for corrupt, reason in [('SHA256SUMS', '签名清单格式错误'), (zip_name, 'ZIP 摘要错误'),
                                ('manifest.json', 'manifest 摘要错误')]:
            self.calls = []
            for name, data in assets.items():
                (offline / name).write_bytes(b'corrupt' if name == corrupt else data)
            self.assertIn(reason, self.invoke(args, program=program, run=self.source_run(assets)))
            self.assertEqual([call[0] for call in self.calls], ['/test/minisign'])
            self.assertFalse(self.target.exists())
        for name, data in assets.items():
            (offline / name).write_bytes(data)
        self.calls = []
        def reject_signature(argv, **kwargs):
            self.calls.append(argv)
            self.assertEqual(argv[0], '/test/minisign')
            raise subprocess.CalledProcessError(1, argv)
        self.invoke(args, program=program, run=reject_signature)
        self.assertEqual(len(self.calls), 1)

    def test_offline_directory_symlink_rejected(self):
        link = self.parent / 'link'
        link.symlink_to(self.parent, target_is_directory=True)
        self.assertIn('非符号链接目录', self.invoke(['--offline-dir', str(link)]))
        self.assertEqual(self.calls, [])

    def test_bounded_network_retries_and_no_mirror_fallback(self):
        for code, attempts in ((56, 3), (28, 3), (60, 1), (22, 1)):
            self.calls = []
            def run(argv, **kwargs):
                self.calls.append(argv)
                self.assertEqual(kwargs['stderr'], subprocess.DEVNULL)
                raise subprocess.CalledProcessError(code, argv, stderr='https://user:SECRET@redirect/')
            output = self.invoke(['--prefix', str(self.target), '--base-url', 'https://mirror.example/v1/'], run=run)
            self.assertEqual(len(self.calls), attempts)
            self.assertTrue(all(call[-1] == 'https://mirror.example/v1/SHA256SUMS' for call in self.calls))
            self.assertIn('下载失败：SHA256SUMS', output)
            self.assertNotIn('SECRET', output)
            self.assertFalse(self.target.exists())

    def test_transient_error_then_success_keeps_full_validation(self):
        program, assets = self.fixture()
        success_run = self.source_run(assets)
        failed = False
        def run(argv, **kwargs):
            nonlocal failed
            if argv[0] == '/test/curl' and not failed:
                failed = True
                self.calls.append(argv)
                Path(argv[argv.index('--output') + 1]).write_bytes(b'partial')
                raise subprocess.CalledProcessError(56, argv)
            return success_run(argv, **kwargs)
        self.invoke(program=program, success=True, run=run)
        self.assertEqual(len([call for call in self.calls if call[0] == '/test/curl']), 6)

    def test_missing_dependency(self):
        self.assertIn('缺少 minisign', self.invoke(which=lambda n: None if n == 'minisign' else '/test/' + n))

    def test_wrong_or_malformed_node_rejected_before_paths_or_network(self):
        for version in (b'v20.20.0', b'v21.7.3', b'v022.22.1', b'22.22.1', b'v22', b'v22.22',
                        b'v22.22.1-extra', b'v22.22.1\nextra', b'', b'garbage'):
            with self.subTest(version=version):
                self.assertIn('Node >=22', self.invoke(version=version))
                self.assertEqual(self.calls, [])
                self.assertFalse(self.target.exists())
                self.assertEqual(list(self.parent.iterdir()), [])

    def test_node_22_and_newer_pass_initial_version_gate(self):
        # An existing destination deliberately stops after version admission,
        # before any download or runtime execution; this is not a compatibility
        # claim for every SQLite build or an actual installation of old assets.
        self.target.mkdir()
        for version in (b'v22.0.0', b'v22.22.1', b'v23.1.0', b'v24.15.0', b'v26.0.0'):
            with self.subTest(version=version):
                self.assertIn('拒绝覆盖', self.invoke(version=version))
                self.assertEqual(self.calls, [])
                self.assertTrue(self.target.is_dir())

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
