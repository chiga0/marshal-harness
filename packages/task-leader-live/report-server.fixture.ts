// A bounded read-only business target, not a website/deployment platform.
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {check} from './proof.fixture.mjs';

export async function startReportServer(root) {
  const stat = fs.lstatSync(root);
  check(fs.realpathSync(root) === root && stat.isDirectory() && (stat.mode & 0o777) === 0o700 && stat.uid === process.getuid(), 'report_root_invalid');
  const identity = `${stat.dev}:${stat.ino}`;
  const server = http.createServer((request, response) => {
    response.setHeader('Connection', 'close'); response.setHeader('Cache-Control', 'no-store');
    let fd;
    try {
      const current = fs.lstatSync(root), name = request.url?.slice('/reports/'.length);
      check(request.method === 'GET' && request.headers.host === `127.0.0.1:${server.address().port}` &&
        request.url?.startsWith('/reports/') && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}-[a-f0-9]{64}\.json$/.test(name) &&
        !request.headers.authorization && !request.headers.origin && !request.headers.cookie &&
        `${current.dev}:${current.ino}` === identity && fs.realpathSync(root) === root, 'report_request_invalid');
      fd = fs.openSync(path.join(root, name), fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
      const before = fs.fstatSync(fd);
      check(before.isFile() && before.nlink === 1 && (before.mode & 0o777) === 0o600 && before.size > 0 && before.size <= 1048576, 'report_file_invalid');
      const bytes = Buffer.alloc(before.size + 1), length = fs.readSync(fd, bytes, 0, bytes.length, 0), after = fs.fstatSync(fd);
      check(length === before.size && before.size === after.size && before.ctimeMs === after.ctimeMs && before.mtimeMs === after.mtimeMs, 'report_changed');
      response.writeHead(200, {'Content-Type': 'application/json', 'Content-Length': length}); response.end(bytes.subarray(0, length));
    } catch {response.writeHead(404, {'Content-Length': '0'}); response.end();}
    finally {if (fd !== undefined) fs.closeSync(fd);}
  });
  server.headersTimeout = 3000; server.requestTimeout = 3000; server.maxConnections = 8;
  await new Promise((resolve, reject) => {server.once('error', reject); server.listen(0, '127.0.0.1', resolve);});
  return {url: `http://127.0.0.1:${server.address().port}/reports/`, close: () => new Promise((resolve, reject) => {
    server.close(error => error ? reject(error) : resolve()); server.closeAllConnections();
  })};
}

export async function consumePublished(url, name, deadline) {
  const target = new URL(name, url);
  check(target.href === url + name && target.hostname === '127.0.0.1' && target.protocol === 'http:' && !target.username && !target.password &&
    deadline > Date.now(), 'report_url_invalid');
  const response = await fetch(target, {redirect: 'error', headers: {'Accept': 'application/json', 'Accept-Encoding': 'identity'},
    signal: AbortSignal.timeout(Math.min(10000, deadline - Date.now()))});
  check(response.status === 200 && response.headers.get('content-type') === 'application/json' &&
    !response.headers.get('content-encoding') && Number(response.headers.get('content-length')) > 0 &&
    Number(response.headers.get('content-length')) <= 1048576, 'report_http_invalid');
  const chunks = []; let total = 0;
  for await (const chunk of response.body) {total += chunk.length; check(total <= 1048576, 'report_http_limit'); chunks.push(chunk);}
  check(total === Number(response.headers.get('content-length')), 'report_http_truncated'); return Buffer.concat(chunks);
}
