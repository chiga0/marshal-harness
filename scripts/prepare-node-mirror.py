"""从固定已发布 GitHub Release 准备镜像材料；不使用 OSS 凭据。"""
import ast
import json
import pathlib
import subprocess
import sys
import tempfile


def prepare(tag, parent, channel='stable'):
    if channel not in ('stable', 'preview'):
        raise ValueError('未知分发通道')
    installer = pathlib.Path(__file__).with_name('install-node-preview.sh' if channel == 'preview' else 'install-node.sh')
    program = installer.read_text().split("<<'PY'\n", 1)[1].rsplit('\nPY', 1)[0]
    pins = {}
    for node in ast.parse(program).body:
        if isinstance(node, ast.Assign) and isinstance(node.value, ast.Constant):
            for target in node.targets:
                if isinstance(target, ast.Name):
                    pins[target.id] = node.value.value
    if tag != pins['VERSION']:
        raise ValueError('tag 与安装器固定版本不一致')
    release = json.loads(subprocess.check_output([
        'gh', 'release', 'view', tag, '--repo', 'chiga0/marshal-harness',
        '--json', 'tagName,isDraft,isPrerelease'], timeout=30))
    if release != {'tagName': tag, 'isDraft': False, 'isPrerelease': channel == 'preview'}:
        raise ValueError('Release 必须已公开且与显式通道匹配')
    stage = pathlib.Path(tempfile.mkdtemp(prefix='marshal-oss-', dir=parent))
    base = 'https://github.com/chiga0/marshal-harness/releases/download/' + tag + '/'
    files = [(name, base + name) for name in
             ('SHA256SUMS', 'SHA256SUMS.minisig', pins['ZIP'], 'manifest.json')]
    files.append(('distribution.mjs', 'https://raw.githubusercontent.com/chiga0/marshal-harness/'
                  + pins['SOURCE'] + '/packages/task-distribution/index.mjs'))
    # ADR0102：SOURCE 为迁移后提交时源文件为 index.ts；按时代双探，
    # 资产名沿用实际扩展，避免把含类型语法的文件以 .mjs 名义执行。
    probe = ('https://raw.githubusercontent.com/chiga0/marshal-harness/'
             + pins['SOURCE'] + '/packages/task-distribution/index.ts')
    if subprocess.run(['curl', '-q', '-fsSIL', '--proto', '=https', '--proto-redir', '=https',
                       '--connect-timeout', '15', '--max-time', '60', probe],
                      check=False, capture_output=True).returncode == 0:
        files = [entry for entry in files if entry[0] != 'distribution.mjs']
        files.append(('distribution.ts', probe))
    for name, url in files:
        print('下载 ' + name, flush=True)
        subprocess.run(['curl', '-q', '-fsSL', '--proto', '=https', '--proto-redir', '=https',
                        '--connect-timeout', '15', '--max-time', '120', '--retry', '2',
                        '--retry-all-errors', '--max-filesize', '16777216',
                        '--output', str(stage / name), url], check=True, timeout=400)
    (stage / installer.name).write_bytes(installer.read_bytes())
    return stage


if __name__ == '__main__':
    try:
        stage = prepare(sys.argv[1], sys.argv[2], sys.argv[4] if len(sys.argv) == 5 else 'stable')
        # This file is consumed by the workflow, not shell-evaluated.
        pathlib.Path(sys.argv[3]).write_text(str(stage))
    except Exception:
        print('镜像材料准备失败；未访问 OSS。', file=sys.stderr)
        sys.exit(1)
