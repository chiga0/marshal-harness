#!/usr/bin/env python3
"""核验实际构建页面的站内链接、片段和 OpenAPI 下载字节。"""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
SITE = ROOT / '.marshal/docs-site'


class Page(HTMLParser):
    def __init__(self, html):
        super().__init__()
        self.ids = set()
        self.links = []
        self.feed(html)

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if attrs.get('id'):
            self.ids.add(attrs['id'])
        for key in ('href', 'src'):
            if attrs.get(key):
                self.links.append(attrs[key])


def main():
    html = {p.resolve(): Page(p.read_text()) for p in SITE.rglob('*.html')}
    if not html:
        raise SystemExit('没有构建页面')
    errors = []
    count = 0
    for source, page in html.items():
        for target in page.links:
            url = urlsplit(target)
            if url.scheme or url.netloc or target.startswith('/'):
                continue
            path = (source.parent / unquote(url.path)).resolve() if url.path else source
            if path.is_dir():
                path /= 'index.html'
            count += 1
            if not path.is_file():
                errors.append(f'{source.relative_to(SITE)}: 不存在 {target}')
            elif url.fragment and path in html and unquote(url.fragment) not in html[path].ids:
                errors.append(f'{source.relative_to(SITE)}: 片段不存在 {target}')
    if (SITE / 'api/openapi.json').read_bytes() != (ROOT / 'packages/task-api/openapi.json').read_bytes():
        errors.append('OpenAPI 下载与唯一源文件不同')
    if errors:
        raise SystemExit('\n'.join(errors))
    print(f'通过：{len(html)} 个 HTML，{count} 个站内链接，OpenAPI 原字节一致')


if __name__ == '__main__':
    main()
