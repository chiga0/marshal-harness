#!/usr/bin/env bash
# 固定 Node stable 安装器；不构建、不提权、不覆盖、不启动服务。
set -euo pipefail
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  printf '%s\n' '用法：bash install-node.sh [--prefix /absolute/private-parent/install-dir]' '安装固定 v1.0.1。依赖：Node >=22、python3、curl、minisign；服务启动另检 SQLite 必需能力。' '默认：$HOME/.local/share/marshal-node/v1.0.1。仅安装，不配置 Agent 或启动 HTTP。' '自定义目标的父目录须已存在、当前用户所有、0700；目标必须不存在。'
  exit 0
fi
command -v python3 >/dev/null || { printf '%s\n' '缺少 python3，请先安装。' >&2; exit 1; }
python3 -I -B - "$@" <<'PY'
import hashlib, json, os, pathlib, re, shlex, shutil, stat, subprocess, sys, tempfile, zipfile

VERSION = 'v1.0.1'
SOURCE = 'b90d7e7247a690db2af740078c285331569aa496'
ZIP = 'marshal-node-candidate-b90d7e72.zip'
ZIP_SHA = 'a94f53073c96f813a7fbd24edc15a77c32130329a3fbef877d8371e9ec17a2a1'
MANIFEST_SHA = '10c747d2f25dce6c085a736c2ed3e55f19ed9f0517e25a3b8e8561f08f9240f7'
HELPER_SHA = '61446c045da5af78434967ae851781a9581b3de4cd820e30b2173256d593bd80'
PUBLIC_KEY = 'RWQAYJ7SGGbem0iFIm1Hjh8837yNiXVQajajH8efRf3E2ziZi7itc1Nq'
BASE = 'https://github.com/chiga0/marshal-harness/releases/download/' + VERSION + '/'
MAX = 16 * 1024 * 1024
stage = None

def require(condition, code):
    if not condition:
        raise ValueError(code)

def canonical(p):
    require(p.is_absolute() and str(p.resolve()) == str(p), '目录必须是无符号链接的绝对规范路径')
    # sticky /tmp 可安全承载当前用户独占的临时目录；其他可被外人改写的祖先不接受。
    for ancestor in p.parents:
        s = ancestor.stat()
        require(not (s.st_mode & 0o022) or bool(s.st_mode & stat.S_ISVTX), '安装路径祖先不可被其他用户写入')

def private(p):
    canonical(p)
    s = p.lstat()
    require(stat.S_ISDIR(s.st_mode) and s.st_uid == os.getuid() and stat.S_IMODE(s.st_mode) == 0o700, '父目录必须属于当前用户且权限为 0700')

def digest(data):
    return hashlib.sha256(data).hexdigest()

try:
    args = sys.argv[1:]
    require(not args or (len(args) == 2 and args[0] == '--prefix'), '参数错误；使用 --help')
    tools = {name: shutil.which(name) for name in ('node', 'curl', 'minisign')}
    for name, command in tools.items():
        require(command is not None, '缺少 ' + name + '；请先安装，安装器不会自动安装依赖')
    node_version = subprocess.check_output([tools['node'], '--version'], timeout=10).strip()
    node_match = re.fullmatch(rb'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)', node_version)
    require(node_match is not None and int(node_match[1]) >= 22, '需要 Node >=22；无法识别或不支持当前 Node 版本')
    require((sys.platform, os.uname().machine) in [('darwin', 'arm64'), ('linux', 'x86_64')], '已验证平台为 darwin-arm64 / linux-x64')
    os.umask(0o077)
    if args:
        target = pathlib.Path(args[1])
        canonical(target)
        private(target.parent)
    else:
        home = pathlib.Path.home().resolve()
        base = home / '.local' / 'share' / 'marshal-node'
        current = home
        for segment in ('.local', 'share', 'marshal-node'):
            current = current / segment
            if not os.path.lexists(current):
                current.mkdir(mode=0o700)
            canonical(current)
            require(current.is_dir() and current.stat().st_uid == os.getuid(), '安装路径不属于当前用户')
        private(base)
        target = base / VERSION
    require(not os.path.lexists(target), '目标已存在，拒绝覆盖；请使用新的 --prefix')
    stage = pathlib.Path(tempfile.mkdtemp(prefix='.marshal-download-', dir=target.parent))
    print('下载并核验固定 ' + VERSION + '；不启动 Agent 或服务。', flush=True)

    def download(url, name, limit):
        dest = stage / name
        subprocess.run([tools['curl'], '-q', '--fail', '--silent', '--show-error', '--location',
                        '--proto', '=https', '--proto-redir', '=https', '--connect-timeout', '20',
                        '--max-time', '180', '--retry', '2', '--max-filesize', str(limit),
                        '--output', str(dest), url], check=True, timeout=570)
        require(dest.stat().st_size <= limit, '下载文件超过大小上限')
        return dest

    sums = download(BASE + 'SHA256SUMS', 'SHA256SUMS', 65536)
    signature = download(BASE + 'SHA256SUMS.minisig', 'SHA256SUMS.minisig', 4096)
    subprocess.run([tools['minisign'], '-V', '-P', PUBLIC_KEY, '-m', str(sums), '-x', str(signature)], check=True, timeout=15)
    lines = sums.read_text().splitlines()
    checks = {}
    for line in lines:
        if line.startswith('#') or not line.strip():
            continue
        match = re.fullmatch(r'([a-f0-9]{64})  ([a-zA-Z0-9_.-]+)', line)
        require(match is not None and match[2] not in checks, '签名清单格式错误')
        checks[match[2]] = match[1]
    require(checks == {ZIP: ZIP_SHA, 'manifest.json': MANIFEST_SHA}, '签名清单与固定发行身份不一致')
    archive = download(BASE + ZIP, ZIP, MAX)
    require(digest(archive.read_bytes()) == ZIP_SHA, 'ZIP 摘要错误')
    manifest = download(BASE + 'manifest.json', 'manifest.json', 65536)
    require(digest(manifest.read_bytes()) == MANIFEST_SHA, 'manifest 摘要错误')
    # 验证器不是发行包中的运行时文件；取固定 source 文件并在执行前校验内置摘要。
    helper = download('https://raw.githubusercontent.com/chiga0/marshal-harness/' + SOURCE + '/packages/task-distribution/index.mjs', 'distribution.mjs', 65536)
    require(digest(helper.read_bytes()) == HELPER_SHA, '固定恢复器摘要错误')
    metadata = json.loads(manifest.read_bytes())
    require(metadata['sourceHead'] == SOURCE, 'sourceHead 错误')
    expected = {item['path']: item for item in metadata['files']}
    expected['manifest.json'] = {'bytes': manifest.stat().st_size, 'digest': 'sha256:' + MANIFEST_SHA}
    # 不调用 extractall：在创建任何 carrier 文件前验证整个 ZIP 的名称、类型、长度和内容。
    contents = {}
    with zipfile.ZipFile(archive) as zipped:
        require(len(zipped.infolist()) == len(expected), 'ZIP 清单数量错误')
        total = 0
        for item in zipped.infolist():
            name = item.filename
            require(name in expected and name not in contents and not item.is_dir(), 'ZIP 包含未知或重复路径')
            require(re.fullmatch(r'[a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_.-]+)*', name) is not None and not any(p in ('.', '..') for p in name.split('/')), 'ZIP 路径不安全')
            mode = item.external_attr >> 16
            require(stat.S_IFMT(mode) in (0, stat.S_IFREG) and not (item.flag_bits & 1), 'ZIP 文件类型不安全')
            require(item.file_size == expected[name]['bytes'] and item.file_size <= 2 * 1024 * 1024, 'ZIP 文件长度错误')
            total += item.file_size
            require(total <= MAX, 'ZIP 总长度超过上限')
            data = zipped.read(item)
            require('sha256:' + digest(data) == expected[name]['digest'], 'ZIP 文件摘要错误')
            contents[name] = data
    carrier = stage / 'carrier'
    carrier.mkdir(mode=0o700)
    for name, data in contents.items():
        dest = carrier / name
        dest.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        with dest.open('xb') as stream:
            stream.write(data)
    runner = "import {pathToFileURL} from 'node:url'; const [helper,carrier,target,manifestDigest,sourceHead]=process.argv.slice(1); const {restoreCarrier}=await import(pathToFileURL(helper)); console.log(JSON.stringify(restoreCarrier({carrier,target,manifestDigest,sourceHead})));"
    subprocess.run([tools['node'], '--input-type=module', '-e', runner, str(helper), str(carrier), str(target), 'sha256:' + MANIFEST_SHA, SOURCE], check=True, timeout=30)
    print('安装完成（未启动，未配置 Provider）：' + str(target))
    print('配置准备好后启动：\n' + ' '.join(shlex.quote(arg) for arg in [tools['node'], str(target / metadata['entrypoint']), '--config', '/absolute/trusted/config.mjs']))
    print('下载、签名及恢复证据保留于：' + str(stage))
except (ValueError, OSError, KeyError, TypeError, zipfile.BadZipFile, subprocess.SubprocessError):
    print('安装失败；未覆盖旧安装、未启动服务。请检查依赖、路径、网络及验签输出。', file=sys.stderr)
    if stage:
        print('已保留下载/失败证据：' + str(stage), file=sys.stderr)
    # 参数和环境错误提供固定原因，不回显配置、环境变量或下载内容。
    error = sys.exc_info()[1]
    if isinstance(error, ValueError) and not isinstance(error, json.JSONDecodeError):
        print(str(error), file=sys.stderr)
    sys.exit(1)
PY
