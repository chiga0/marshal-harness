import {supportsNode} from '../task-store/runtime.mjs';
import fs from 'node:fs';
import path from 'node:path';
import http from 'node:http';
import {fileURLToPath} from 'node:url';
import {check} from './policy.mjs';
/** Read-only, finite local business target; no UI, mutation or authority API.
 * The same UID can read these reports. Do not bind externally or use for secrets. */
export async function startReportServer({root, port}) {
  check(path.isAbsolute(root ?? '') && fs.realpathSync(root) === root && Number.isSafeInteger(port) && port >= 0 && port <= 65535, 'report_reader_configuration');
  const fd = fs.openSync(root, fs.constants.O_RDONLY | fs.constants.O_DIRECTORY | fs.constants.O_NOFOLLOW), identity = fs.fstatSync(fd);
  const privateDirectory = value => value.isDirectory() && value.uid === process.getuid() && (value.mode & 0o777) === 0o700;
  try {check(privateDirectory(identity) && privateDirectory(fs.lstatSync(path.dirname(root))), 'report_reader_private_root');} catch (error) {fs.closeSync(fd); throw error;}
  const server = http.createServer({maxHeaderSize: 8192}, (request, response) => {
    response.setHeader('Connection', 'close'); response.setHeader('Cache-Control', 'no-store'); let file;
    try {
      check(request.method === 'GET' && request.headers.host === '127.0.0.1:' + server.address().port &&
        !request.headers.authorization && !request.headers.cookie && !request.headers.origin && !request.headers['transfer-encoding'] &&
        (request.headers['content-length'] === undefined || request.headers['content-length'] === '0'), 'report_reader_request');
      check(/^\/reports\/[A-Za-z0-9][A-Za-z0-9_-]{0,127}-[a-f0-9]{64}\.json$/.test(request.url), 'report_reader_path');
      const current = fs.lstatSync(root); check(privateDirectory(current) && current.dev === identity.dev && current.ino === identity.ino &&
        fs.realpathSync(root) === root, 'report_reader_identity');
      const target = path.join(root, request.url.slice('/reports/'.length));
      file = fs.openSync(target, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
      const before = fs.fstatSync(file);
      check(before.isFile() && before.nlink === 1 && before.uid === process.getuid() && (before.mode & 0o777) === 0o600 && before.size > 0 && before.size <= 1048576, 'report_reader_file');
      const body = Buffer.alloc(before.size + 1); let count = 0;
      while (count < body.length) {const read = fs.readSync(file, body, count, body.length - count, count); if (!read) break; count += read;}
      const after = fs.fstatSync(file), named = fs.lstatSync(target), parent = fs.lstatSync(root);
      check(count === before.size && after.size === before.size && after.mtimeMs === before.mtimeMs && after.ctimeMs === before.ctimeMs &&
        named.dev === before.dev && named.ino === before.ino && parent.dev === identity.dev && parent.ino === identity.ino, 'report_reader_changed');
      response.writeHead(200, {'Content-Type': 'application/json', 'Content-Length': count}); response.end(body.subarray(0, count));
    } catch {response.writeHead(404, {'Content-Length': '0'}); response.end();}
    finally {if (file !== undefined) fs.closeSync(file);}
  });
  server.headersTimeout = 3000; server.requestTimeout = 3000; server.setTimeout(3000, socket => socket.destroy()); server.maxConnections = 8;
  try {await new Promise((resolve, reject) => {server.once('error', reject); server.listen(port, '127.0.0.1', resolve);});}
  catch (error) {fs.closeSync(fd); throw error;}
  let closing;
  return {url: 'http://127.0.0.1:' + server.address().port + '/reports/', close() {
    closing ??= new Promise((resolve, reject) => {server.close(error => {fs.closeSync(fd); error ? reject(error) : resolve();}); server.closeAllConnections();}); return closing;
  }};
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    // Fixed URL/port survives service and reader restarts; no implicit port choice.
    check(supportsNode() && process.argv.length === 6 && process.argv[2] === '--root' && process.argv[4] === '--port' &&
      /^[1-9][0-9]{0,4}$/.test(process.argv[5]), 'report_reader_arguments');
    const reader = await startReportServer({root: process.argv[3], port: Number(process.argv[5])});
    process.stdout.write(JSON.stringify({url: reader.url, readOnly: true}) + '\n');
    for (const signal of ['SIGINT', 'SIGTERM']) process.once(signal, () => void reader.close().then(() => {process.exitCode = 0;}, () => {process.exitCode = 1;}));
  } catch {process.stderr.write('{"code":"report_reader_unavailable"}\n'); process.exitCode = 1;}
}
