#!/usr/bin/env python3
"""从导航和唯一 OpenAPI 生成公开文档；历史/源码链接绑定构建提交。"""
from pathlib import Path
import hashlib
import json
import posixpath
import re
import shutil
import subprocess
from urllib.parse import quote, urlsplit
import yaml

ROOT = Path(__file__).resolve().parents[1]
DEST = ROOT / '.marshal/docs-source'
SOURCE = ROOT / 'docs'
SPEC = ROOT / 'packages/task-api/openapi.json'


def pages(tree):
    for entry in tree:
        if isinstance(entry, str):
            yield entry
        else:
            for value in entry.values():
                if isinstance(value, list):
                    yield from pages(value)
                else:
                    yield value


def main():
    config = yaml.load((ROOT / 'mkdocs.yml').read_text(), Loader=yaml.BaseLoader)
    published = set(pages(config['nav']))
    generated = {'api/http-reference.md', 'build-info.md'}
    missing = [p for p in published - generated if not (SOURCE / p).is_file()]
    if missing:
        raise SystemExit('缺少导航源文件: ' + ', '.join(sorted(missing)))
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    git_base = 'https://github.com/chiga0/marshal-harness/blob/' + revision + '/'
    if DEST.exists():
        shutil.rmtree(DEST)
    DEST.mkdir(parents=True)
    mapping = {'docs/' + p: p for p in published}
    mapping['packages/task-api/openapi.json'] = 'api/openapi.json'

    def rewrite_link(target, page):
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or target.startswith(('#', '/')):
            return target
        source = posixpath.normpath(posixpath.join('docs', posixpath.dirname(page), parsed.path))
        if source in mapping:
            path = posixpath.relpath(mapping[source], posixpath.dirname(page) or '.')
            return path + ('?' + parsed.query if parsed.query else '') + ('#' + parsed.fragment if parsed.fragment else '')
        if not (ROOT / source).exists():
            # 历史正文常引用已退役文件；只对在站内发布的当前页面严格拒绝坏链接。
            raise ValueError(f'{page}: 链接目标不存在: {target}')
        return git_base + quote(source, safe='/') + ('#' + parsed.fragment if parsed.fragment else '')

    for page in sorted(published - generated):
        content = (SOURCE / page).read_text()
        # 跳过 fenced code；只转换 Markdown 正文链接，保持示例原样。
        chunks = re.split(r'(^```[^\n]*\n.*?^```\s*$)', content, flags=re.M | re.S)
        for i in range(0, len(chunks), 2):
            chunks[i] = re.sub(r'(?<=\]\()([^\s)]+)(?=\))', lambda m: rewrite_link(m[1], page), chunks[i])
        destination = DEST / page
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(''.join(chunks))

    spec_bytes = SPEC.read_bytes()
    spec = json.loads(spec_bytes)
    api = DEST / 'api'
    api.mkdir(exist_ok=True)
    (api / 'openapi.json').write_bytes(spec_bytes)
    operations = [(path, method, op) for path, methods in spec['paths'].items()
                  for method, op in methods.items() if method in {'get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'}]
    schemas = spec['components']['schemas']
    reference = ['# HTTP 操作与 Schema', '',
                 '本页由唯一 OpenAPI 自动生成，操作、请求、响应和 Schema 不维护第二份手写副本。', '',
                 '[下载 OpenAPI 3.1](openapi.json) · [标准契约语义](../standard-api.md) · [版本支持](../api-support.md)', '',
                 f'合同版本：`{spec["info"]["version"]}`；{len(operations)} 个操作，{len(schemas)} 个 Schema。HTTP 版本标识不等于软件发行版本或全量稳定承诺。', '',
                 '## 访问和公共约束', '', '```json',
                 json.dumps({k: spec[k] for k in ['servers', 'security'] if k in spec}, ensure_ascii=False, indent=2), '```', '',
                 '公共安全与参数定义：', '', '```json',
                 json.dumps({k: v for k, v in spec['components'].items() if k != 'schemas'}, ensure_ascii=False, indent=2), '```', '',
                 '## 操作索引', '', '| 操作 | 路径 | operationId |', '| --- | --- | --- |']
    for path, method, op in operations:
        reference.append(f'| {method.upper()} | `{path}` | [{op["operationId"]}](#{op["operationId"]}) |')
    for path, method, op in operations:
        reference.extend(['', f'## {op["operationId"]}', '', f'`{method.upper()} {path}`', '', op.get('summary', ''), '',
                          '```json', json.dumps(op, ensure_ascii=False, indent=2), '```'])
    reference.extend(['', '## Schema 定义', '', '`$ref` 路径引用下载文件中的 `components`；下面逐项保留完整定义。'])
    for name, value in schemas.items():
        reference.extend(['', f'### {name}', '', '```json', json.dumps(value, ensure_ascii=False, indent=2), '```'])
    (api / 'http-reference.md').write_text('\n'.join(reference) + '\n')
    (DEST / 'build-info.md').write_text(
        '# 构建来源\n\n'
        f'- 来源提交：[`{revision}`](https://github.com/chiga0/marshal-harness/tree/{revision})。\n'
        f'- OpenAPI SHA-256：`{hashlib.sha256(spec_bytes).hexdigest()}`。\n'
        '- 文档导航来自同一提交的 `mkdocs.yml`；站外源码和历史链接绑定此提交。\n'
        '- 本地未提交预览可能包含工作区改动；公开发布仅使用 CI checkout 的提交。\n'
        '- 设计定义目标，版本支持与 Roadmap 记录实现；部署成功不代表产品验收通过。\n')
    print(f'已生成 {len(published)} 页、{len(operations)} 操作/{len(schemas)} Schema，源 {revision}')


if __name__ == '__main__':
    main()
