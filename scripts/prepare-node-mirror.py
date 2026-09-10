"""从固定已发布 GitHub Release 准备镜像材料；不使用 OSS 凭据。"""
import ast
import json
import pathlib
import subprocess
import sys
import tempfile


def prepare(tag, parent):
    installer = pathlib.Path(__file__).with_name('install-node.sh')
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
    if release != {'tagName': tag, 'isDraft': False, 'isPrerelease': False}:
        raise ValueError('必须是已经公开的 stable Release')
    stage = pathlib.Path(tempfile.mkdtemp(prefix='marshal-oss-', dir=parent))
    base = 'https://github.com/chiga0/marshal-harness/releases/download/' + tag + '/'
    files = [(name, base + name) for name in
             ('SHA256SUMS', 'SHA256SUMS.minisig', pins['ZIP'], 'manifest.json')]
    files.append(('distribution.mjs', 'https://raw.githubusercontent.com/chiga0/marshal-harness/'
                  + pins['SOURCE'] + '/packages/task-distribution/index.mjs'))
    for name, url in files:
        print('下载 ' + name, flush=True)
        subprocess.run(['curl', '-q', '-fsSL', '--proto', '=https', '--proto-redir', '=https',
                        '--connect-timeout', '15', '--max-time', '120', '--retry', '2',
                        '--retry-all-errors', '--max-filesize', '16777216',
                        '--output', str(stage / name), url], check=True, timeout=400)
    (stage / 'install-node.sh').write_bytes(installer.read_bytes())
    return stage


if __name__ == '__main__':
    try:
        stage = prepare(sys.argv[1], sys.argv[2])
        # This file is consumed by the workflow, not shell-evaluated.
        pathlib.Path(sys.argv[3]).write_text(str(stage))
    except Exception:
        print('镜像材料准备失败；未访问 OSS。', file=sys.stderr)
        sys.exit(1)
