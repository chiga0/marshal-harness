#!/usr/bin/env python3
"""将固定签名 Node 发行资产同字节镜像到 OSS；不构建、不签名、不覆盖。"""
import argparse
import ast
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


class Rejected(Exception):
    """仅携带固定、不含输入或凭据的错误代码。"""


def require(condition, code):
    if not condition:
        raise Rejected(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def installer_constants(data):
    # 只解析受信安装器内嵌 Python AST，绝不执行安装器或其表达式。
    source = data.decode().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
    wanted = {'VERSION', 'SOURCE', 'ZIP', 'ZIP_SHA', 'MANIFEST_SHA', 'HELPER_SHA', 'PUBLIC_KEY'}
    values = {}
    for node in ast.parse(source).body:
        if isinstance(node, ast.Assign):
            for target in node.targets:
                if isinstance(target, ast.Name) and target.id in wanted:
                    require(target.id not in values, 'duplicate_installer_constant')
                    values[target.id] = ast.literal_eval(node.value)
    require(set(values) == wanted and all(isinstance(v, str) for v in values.values()), 'invalid_installer_constants')
    require(values['VERSION'] == 'v1.0.1', 'unsupported_release')
    require(re.fullmatch(r'[a-f0-9]{40}', values['SOURCE']), 'invalid_source')
    require(re.fullmatch(r'[a-zA-Z0-9_.-]+\.zip', values['ZIP']), 'invalid_archive_name')
    return values


def validate_stage(stage, installer):
    trusted = installer.read_bytes()
    constants = installer_constants(trusted)
    names = [constants['ZIP'], 'SHA256SUMS', 'SHA256SUMS.minisig', 'manifest.json', 'distribution.mjs', 'install-node.sh']
    limits = [16 * 1024 * 1024, 65536, 4096, 65536, 65536, 65536]
    assets = {}
    for name, limit in zip(names, limits):
        path = stage / name
        require(not path.is_symlink() and path.is_file(), 'invalid_stage_file')
        with path.open('rb') as stream:
            data = stream.read(limit + 1)
        require(0 < len(data) <= limit, 'invalid_stage_size')
        assets[name] = data
    require(assets['install-node.sh'] == trusted, 'installer_mismatch')
    checks = {}
    for line in assets['SHA256SUMS'].decode().splitlines():
        if line.startswith('#') or not line.strip():
            continue
        match = re.fullmatch(r'([a-f0-9]{64})  ([a-zA-Z0-9_.-]+)', line)
        require(match is not None and match[2] not in checks, 'invalid_checksums')
        checks[match[2]] = match[1]
    require(checks == {constants['ZIP']: constants['ZIP_SHA'], 'manifest.json': constants['MANIFEST_SHA']}, 'release_identity_mismatch')
    for name, expected in [(constants['ZIP'], constants['ZIP_SHA']), ('manifest.json', constants['MANIFEST_SHA']), ('distribution.mjs', constants['HELPER_SHA'])]:
        require(digest(assets[name]) == expected, 'asset_digest_mismatch')
    require(json.loads(assets['manifest.json'])['sourceHead'] == constants['SOURCE'], 'manifest_source_mismatch')
    # 对已读入的同一份字节验签，避免验签后再次从 stage 取不同字节。
    with tempfile.TemporaryDirectory(prefix='marshal-oss-verify-') as directory:
        sums = Path(directory) / 'SHA256SUMS'
        signature = Path(directory) / 'SHA256SUMS.minisig'
        sums.write_bytes(assets['SHA256SUMS'])
        signature.write_bytes(assets['SHA256SUMS.minisig'])
        result = subprocess.run(['minisign', '-V', '-P', constants['PUBLIC_KEY'], '-m', str(sums), '-x', str(signature)],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=False)
        require(result.returncode == 0, 'signature_invalid')
    return constants['VERSION'], assets


def destination(env):
    region = env.get('MARSHAL_OSS_REGION', '')
    require(re.fullmatch(r'[a-z0-9]+(?:-[a-z0-9]+)+', region), 'invalid_oss_region')
    require(not region.endswith('-internal'), 'public_endpoint_required')
    endpoint = 'https://oss-' + region + '.aliyuncs.com'
    bucket = env.get('MARSHAL_OSS_BUCKET', '')
    require(re.fullmatch(r'[a-z0-9][a-z0-9-]{1,61}[a-z0-9]', bucket), 'invalid_oss_bucket')
    prefix = env.get('MARSHAL_OSS_PREFIX', '')
    require(re.fullmatch(r'[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)*', prefix), 'invalid_oss_prefix')
    return endpoint, region, bucket, prefix


def create_bucket(env, config):
    import oss2
    import requests

    class NoRedirectSession(requests.Session):
        def request(self, *args, **kwargs):
            kwargs['allow_redirects'] = False
            return super().request(*args, **kwargs)

    endpoint, region, bucket, _ = config
    key = env.get('MARSHAL_OSS_ACCESS_KEY_ID', '')
    secret = env.get('MARSHAL_OSS_ACCESS_KEY_SECRET', '')
    require(key and secret, 'missing_oss_credentials')
    session = oss2.Session()
    session.session = NoRedirectSession()
    session.session.trust_env = False
    return oss2.Bucket(oss2.AuthV4(key, secret), endpoint, bucket, region=region, session=session, connect_timeout=30)


def same_object(bucket, key, data, sdk):
    try:
        result = bucket.get_object(key)
    except sdk.exceptions.NoSuchKey:
        return False
    try:
        require(result.content_length == len(data), 'remote_size_mismatch')
        remote = result.read(len(data) + 1)
        require(len(remote) == len(data) and digest(remote) == digest(data), 'remote_digest_mismatch')
    finally:
        result.close()
    return True


def sync(bucket, prefix, version, assets, sdk):
    # OSS 在曾启用版本控制的 bucket 上可能忽略 forbid-overwrite。
    require(bucket.get_bucket_versioning().status in (None, ''), 'bucket_versioning_must_be_unconfigured')
    require(version == 'v1.0.1', 'unsupported_release')
    keys = {name: prefix + '/' + version + '/' + name for name in assets}
    # 先检查整个集合，再进行任何写入；安装器始终最后发布。
    existing = {name: same_object(bucket, keys[name], data, sdk) for name, data in assets.items()}
    order = [name for name in assets if name != 'install-node.sh'] + ['install-node.sh']
    for name in order:
        data = assets[name]
        if not existing[name]:
            try:
                bucket.put_object(keys[name], data, headers={'x-oss-forbid-overwrite': 'true'})
            except sdk.exceptions.ServerError as error:
                # 并发同字节发布可以重跑；其他冲突/权限/网络错误全部 fail closed。
                if error.status != 409 or error.code != 'FileAlreadyExists':
                    raise
        require(same_object(bucket, keys[name], data, sdk), 'remote_object_missing')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--stage', type=Path, required=True)
    args = parser.parse_args()
    try:
        config = destination(os.environ)
        version, assets = validate_stage(args.stage, Path(__file__).with_name('install-node.sh'))
        import oss2
        sync(create_bucket(os.environ, config), config[3], version, assets, oss2)
    except Exception:
        # SDK 错误可能包含签名 URL、请求头或服务端内容，不能回显原始异常。
        print('OSS 镜像失败；未授权覆盖。请检查固定发行资产、OSS 配置、权限和网络。', file=sys.stderr)
        return 1
    print('固定 v1.0.1 六项资产已同字节镜像并回读校验；未更新 latest。')
    return 0


if __name__ == '__main__':
    sys.exit(main())
