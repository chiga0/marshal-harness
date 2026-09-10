#!/usr/bin/env python3
"""OSS 同字节、不可覆盖镜像的离线单元测试。"""
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('sync_node_oss', Path(__file__).with_name('sync-node-oss.py'))
syncer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(syncer)


class NoSuchKey(Exception):
    pass


class ServerError(Exception):
    def __init__(self, status=409, code='FileAlreadyExists'):
        self.status, self.code = status, code


SDK = SimpleNamespace(exceptions=SimpleNamespace(NoSuchKey=NoSuchKey, ServerError=ServerError))


class Bucket:
    def __init__(self):
        self.objects = {}
        self.writes = []
        self.versioning = None
        self.race = None
        self.corrupt = False

    def get_bucket_versioning(self):
        return SimpleNamespace(status=self.versioning)

    def get_object(self, key):
        if key not in self.objects:
            raise NoSuchKey()
        result = io.BytesIO(self.objects[key])
        result.content_length = len(self.objects[key])
        return result

    def put_object(self, key, data, headers):
        assert headers == {'x-oss-forbid-overwrite': 'true'}
        if self.race is not None:
            self.objects[key] = data if self.race == 'same' else b'conflict'
            raise ServerError()
        assert key not in self.objects
        self.writes.append(key)
        self.objects[key] = b'bad' if self.corrupt else data


class SyncTests(unittest.TestCase):
    def setUp(self):
        self.bucket = Bucket()
        self.assets = {name: name.encode() for name in ('a.zip', 'SHA256SUMS', 'SHA256SUMS.minisig', 'manifest.json', 'distribution.mjs', 'install-node.sh')}

    def run_sync(self):
        syncer.sync(self.bucket, 'marshal/node', 'v1.0.1', self.assets, SDK)

    def test_upload_verify_and_idempotent(self):
        self.run_sync()
        self.assertEqual(len(self.bucket.writes), 6)
        self.assertEqual(self.bucket.writes[-1], 'marshal/node/v1.0.1/install-node.sh')
        self.run_sync()
        self.assertEqual(len(self.bucket.writes), 6)

    def test_conflict_preflight_before_any_write(self):
        self.bucket.objects['marshal/node/v1.0.1/install-node.sh'] = b'wrong'
        with self.assertRaises(syncer.Rejected):
            self.run_sync()
        self.assertEqual(self.bucket.writes, [])

    def test_same_size_conflict(self):
        self.bucket.objects['marshal/node/v1.0.1/a.zip'] = b'X.zip'
        with self.assertRaisesRegex(syncer.Rejected, 'remote_digest_mismatch'):
            self.run_sync()

    def test_same_bytes_race_is_accepted(self):
        self.bucket.race = 'same'
        self.run_sync()
        self.assertEqual(len(self.bucket.objects), 6)

    def test_different_bytes_race_rejected(self):
        self.bucket.race = 'different'
        with self.assertRaises(syncer.Rejected):
            self.run_sync()

    def test_readback_failure_never_publishes_installer(self):
        self.bucket.corrupt = True
        with self.assertRaises(syncer.Rejected):
            self.run_sync()
        self.assertEqual(self.bucket.writes, ['marshal/node/v1.0.1/a.zip'])

    def test_versioning_fail_closed(self):
        for status in ('Enabled', 'Suspended', 'unknown'):
            self.bucket.versioning = status
            with self.assertRaises(syncer.Rejected):
                self.run_sync()
        self.assertEqual(self.bucket.writes, [])

    def test_other_server_error_not_swallowed(self):
        with patch.object(self.bucket, 'put_object', side_effect=ServerError(403, 'Denied')):
            with self.assertRaises(ServerError):
                self.run_sync()

    def test_unexpected_version_rejected(self):
        with self.assertRaises(syncer.Rejected):
            syncer.sync(self.bucket, 'marshal/node', 'latest', self.assets, SDK)


class ValidationTests(unittest.TestCase):
    def test_sdk_v4_session_disables_redirects_and_environment_proxy(self):
        import oss2
        import requests
        env = {'MARSHAL_OSS_ACCESS_KEY_ID': 'test-key', 'MARSHAL_OSS_ACCESS_KEY_SECRET': 'test-secret'}
        with patch.object(oss2, 'Bucket') as constructor:
            syncer.create_bucket(env, ('https://oss-cn-hangzhou.aliyuncs.com', 'cn-hangzhou', 'test-bucket', 'node'))
        self.assertIsInstance(constructor.call_args.args[0], oss2.AuthV4)
        self.assertEqual(constructor.call_args.kwargs['region'], 'cn-hangzhou')
        session = constructor.call_args.kwargs['session'].session
        self.assertFalse(session.trust_env)
        with patch.object(requests.Session, 'request') as request:
            session.request('GET', 'https://oss-cn-hangzhou.aliyuncs.com', allow_redirects=True)
        self.assertFalse(request.call_args.kwargs['allow_redirects'])

    def test_endpoint_restrictions(self):
        env = {'MARSHAL_OSS_ENDPOINT': 'https://oss-cn-hangzhou.aliyuncs.com', 'MARSHAL_OSS_BUCKET': 'marshal-releases', 'MARSHAL_OSS_PREFIX': 'marshal/node'}
        self.assertEqual(syncer.destination(env)[1], 'cn-hangzhou')
        for endpoint in ('http://oss-cn-hangzhou.aliyuncs.com', 'https://evil.test', 'https://oss-cn-hangzhou.aliyuncs.com.evil.test', 'https://key@oss-cn-hangzhou.aliyuncs.com', 'https://oss-cn-hangzhou.aliyuncs.com/path', 'https://oss-cn-hangzhou.aliyuncs.com:443', 'https://oss-cn-hangzhou-internal.aliyuncs.com'):
            with self.subTest(endpoint=endpoint), self.assertRaises(syncer.Rejected):
                syncer.destination(dict(env, MARSHAL_OSS_ENDPOINT=endpoint))
        for prefix in ('', '../foo', '/foo', 'foo/', 'foo//bar', 'foo/../bar', 'foo?bar'):
            with self.assertRaises(syncer.Rejected):
                syncer.destination(dict(env, MARSHAL_OSS_PREFIX=prefix))

    def test_real_installer_ast(self):
        constants = syncer.installer_constants(Path(__file__).with_name('install-node.sh').read_bytes())
        self.assertEqual(constants['VERSION'], 'v1.0.1')
        self.assertEqual(constants['SOURCE'], 'b90d7e7247a690db2af740078c285331569aa496')

    def fixture(self, directory):
        stage = Path(directory) / 'stage'
        stage.mkdir()
        files = {'candidate.zip': b'zip', 'manifest.json': json.dumps({'sourceHead': 'b' * 40}).encode(), 'distribution.mjs': b'helper'}
        constants = {'VERSION': 'v1.0.1', 'SOURCE': 'b' * 40, 'ZIP': 'candidate.zip', 'ZIP_SHA': syncer.digest(files['candidate.zip']), 'MANIFEST_SHA': syncer.digest(files['manifest.json']), 'HELPER_SHA': syncer.digest(files['distribution.mjs']), 'PUBLIC_KEY': 'test'}
        installer = Path(directory) / 'install-node.sh'
        installer.write_text("python3 <<'PY'\n" + '\n'.join(f'{key} = {value!r}' for key, value in constants.items()) + "\nraise RuntimeError('never execute')\nPY\n")
        files['SHA256SUMS'] = (constants['ZIP_SHA'] + '  candidate.zip\n' + constants['MANIFEST_SHA'] + '  manifest.json\n').encode()
        files['SHA256SUMS.minisig'] = b'signature'
        files['install-node.sh'] = installer.read_bytes()
        for name, data in files.items():
            (stage / name).write_bytes(data)
        return stage, installer

    def test_stage_validates_and_never_executes_installer(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(syncer.subprocess, 'run', return_value=SimpleNamespace(returncode=0)) as run:
            stage, installer = self.fixture(directory)
            version, assets = syncer.validate_stage(stage, installer)
            self.assertEqual(version, 'v1.0.1')
            self.assertEqual(len(assets), 6)
            self.assertEqual(run.call_args.args[0][:2], ['minisign', '-V'])

    def test_each_asset_tamper_rejected(self):
        for name in ('candidate.zip', 'manifest.json', 'distribution.mjs', 'SHA256SUMS', 'install-node.sh'):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                stage, installer = self.fixture(directory)
                (stage / name).write_bytes(b'bad')
                with self.assertRaises(syncer.Rejected):
                    syncer.validate_stage(stage, installer)

    def test_invalid_signature(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(syncer.subprocess, 'run', return_value=SimpleNamespace(returncode=1)):
            stage, installer = self.fixture(directory)
            with self.assertRaisesRegex(syncer.Rejected, 'signature_invalid'):
                syncer.validate_stage(stage, installer)

    def test_symlink_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            stage, installer = self.fixture(directory)
            (stage / 'install-node.sh').unlink()
            (stage / 'install-node.sh').symlink_to(installer)
            with self.assertRaisesRegex(syncer.Rejected, 'invalid_stage_file'):
                syncer.validate_stage(stage, installer)

    def test_failure_output_does_not_expose_exception(self):
        with patch('sys.argv', ['sync-node-oss.py', '--stage', '/unused']), patch.object(syncer, 'destination', side_effect=Exception('secret-access-key')), patch('sys.stderr', new_callable=io.StringIO) as stderr:
            self.assertEqual(syncer.main(), 1)
            self.assertNotIn('secret-access-key', stderr.getvalue())


if __name__ == '__main__':
    unittest.main()
