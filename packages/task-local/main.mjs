import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {pathToFileURL, fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {discoverAgents} from './discovery.mjs';
import {installCommand} from './install-command.mjs';

const fail = code => { throw new Error(code); };
const codes = new Set(['invalid_arguments', 'unsafe_settings', 'installation_missing_or_ambiguous',
  'configuration_required', 'connection_unavailable', 'service_start_failed', 'settings_missing', 'command_install_conflict',
  'agent_unavailable', 'running_configuration_conflict']);
const optionFields = {'--config': 'config', '--connection-file': 'connectionFile', '--data-dir': 'dataDir',
  '--ui': 'ui', '--port': 'port', '--agent-executable': 'agentExecutable', '--install-root': 'installRoot'};
function executable(value) {
  try {
    const resolved = fs.realpathSync(absolute(value)), stat = fs.statSync(resolved);
    if (!stat.isFile() || !(stat.mode & 0o111)) fail('agent_unavailable');
    fs.accessSync(resolved, fs.constants.X_OK);
    return resolved;
  } catch {fail('agent_unavailable');}
}
function serviceAddress(value) {
  if (typeof value !== 'string') return undefined;
  try {
    const url = new URL(value);
    if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || !url.port || url.username || url.password ||
      url.search || url.hash || url.pathname !== '/') fail('service_start_failed');
    return url.origin;
  } catch {fail('service_start_failed');}
}
function absolute(value) {
  if (typeof value !== 'string' || !path.isAbsolute(value) || /[\x00-\x1f\x7f]/.test(value)) fail('invalid_arguments');
  return path.resolve(value);
}
function privateDir(dir) {
  if (!fs.existsSync(dir)) fs.mkdirSync(dir, {mode: 0o700});
  const s = fs.lstatSync(dir);
  if (!s.isDirectory() || s.uid !== process.getuid() || (s.mode & 0o077) || fs.realpathSync(dir) !== dir) fail('unsafe_settings');
}
function readPrivate(file) {
  const fd = fs.openSync(file, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const s = fs.fstatSync(fd);
    if (!s.isFile() || s.uid !== process.getuid() || (s.mode & 0o077) || s.size > 65536) fail('unsafe_settings');
    const b = Buffer.alloc(65537), n = fs.readSync(fd, b, 0, b.length, 0);
    if (n > 65536) fail('unsafe_settings');
    return JSON.parse(b.subarray(0, n).toString('utf8'));
  } finally {fs.closeSync(fd);}
}
function save(file, value) {
  const temporary = path.join(path.dirname(file), `.settings-${process.pid}-${randomUUID()}`);
  const fd = fs.openSync(temporary, 'wx', 0o600);
  try {fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}
  fs.renameSync(temporary, file);
}
function installation(root) {
  root = absolute(root);
  for (const name of [root, path.join(root, 'packages'), path.join(root, 'packages/task-client'), path.join(root, 'packages/task-service')]) {
    const s = fs.lstatSync(name);
    if (!s.isDirectory() || s.uid !== process.getuid() || (s.mode & 0o022)) fail('unsafe_settings');
  }
  for (const name of ['packages/task-client/index.mjs', 'packages/task-service/main.mjs']) {
    const s = fs.lstatSync(path.join(root, name));
    if (!s.isFile() || s.uid !== process.getuid() || (s.mode & 0o022)) fail('unsafe_settings');
  }
  return root;
}
async function connect(settings) {
  if (!settings.connectionFile) fail('connection_unavailable');
  const {TaskClient} = await import(pathToFileURL(path.join(installation(settings.installRoot), 'packages/task-client/index.mjs')));
  const connection = readPrivate(absolute(settings.connectionFile));
  const client = new TaskClient({baseURL: connection.url, token: connection.token});
  await client.request('ready.get', {timeoutMs: 2000});
  return client;
}
export async function connectLocal({settingsDir = path.join(fs.realpathSync(os.homedir()), '.marshal-client')} = {}) {
  privateDir(absolute(settingsDir));
  const settings = readPrivate(path.join(settingsDir, 'local.json'));
  if (!settings || settings.version !== 1) fail('unsafe_settings');
  return connect(settings);
}
export async function run(argv, {home = os.homedir(), output = value => console.log(JSON.stringify(value)), startupTimeoutMs = 30000, stopTimeoutMs = 5000} = {}) {
  const command = argv[0] ?? '--help', options = {};
  if (command === '--help') {
    output({commands: ['init', 'status', 'serve'], options: ['--install-root', '--config', '--connection-file', '--settings-dir',
      '--data-dir', '--port', '--ui', '--no-ui', '--agent-executable'],
      note: 'init 检测并记录；serve 优先复用既有配置，无配置时使用本机 Qwen 通用文件团队；不自动授权外部业务发布。'}); return;
  }
  if (!['init', 'status', 'serve'].includes(command)) fail('invalid_arguments');
  for (let i = 1; i < argv.length; i += 2) {
    const key = argv[i];
    if (key === '--no-ui') {
      if (key in options || '--ui' in options) fail('invalid_arguments');
      options[key] = true; i--; continue;
    }
    if (key === '--ui' && '--no-ui' in options) fail('invalid_arguments');
    if (![...Object.keys(optionFields), '--settings-dir'].includes(key) || key in options || !argv[i+1]) fail('invalid_arguments');
    if (key === '--port') {
      if (!/^(0|[1-9][0-9]{0,4})$/.test(argv[i+1]) || Number(argv[i+1]) > 65535) fail('invalid_arguments');
      options[key] = Number(argv[i+1]);
    } else options[key] = absolute(argv[i+1]);
  }
  const dir = options['--settings-dir'] ?? path.join(fs.realpathSync(home), '.marshal-client');
  privateDir(dir);
  const file = path.join(dir, 'local.json');
  let settings = fs.existsSync(file) ? readPrivate(file) : {version: 1};
  if (!settings || settings.version !== 1) fail('unsafe_settings');
  // A live recorded service is not silently repurposed by a new launch request.
  // Check the original connection before applying any requested overrides.
  const previous = {...settings};
  if (command === 'serve' && previous.connectionFile) {
    let live = false; try {await connect(previous); live = true;} catch {}
    if (live) {
      const changed = options['--no-ui'] && previous.ui !== undefined ||
        Object.entries(optionFields).some(([option, field]) => option in options && options[option] !== previous[field]);
      if (changed) fail('running_configuration_conflict');
      output({state: 'connected', settingsFile: file, ...(previous.address ? {address: previous.address} : {}),
        ...(previous.ui && previous.address ? {uiUrl: previous.address + '/ui/'} : {})}); return;
    }
  }
  settings.installRoot = installation(options['--install-root'] ?? settings.installRoot ?? path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..'));
  for (const [option, field] of Object.entries(optionFields)) if (option in options) settings[field] = options[option];
  if (options['--no-ui']) delete settings.ui;
  if (options['--config']) delete settings.generic;
  if (command === 'init') {
    settings.agents = await discoverAgents();
    save(file, settings);
    let launcher;
    try {launcher = {...installCommand({installRoot: settings.installRoot, home: fs.realpathSync(home)}), state: 'installed'};}
    catch {launcher = {state: 'conflict', code: 'command_install_conflict'};}
    let connected = false; try {await connect(settings); connected = true;} catch {}
    output({state: connected ? 'connected' : 'initialized', installRoot: settings.installRoot,
      agents: settings.agents, launcher, settingsFile: file, serviceConfigured: Boolean(settings.config),
      next: connected ? null : 'serve'});
    return;
  }
  try {await connect(settings); output({state: 'connected', settingsFile: file,
    ...(settings.address ? {address: settings.address} : {}), ...(settings.ui && settings.address ? {uiUrl: settings.address + '/ui/'} : {})}); return;} catch {
    if (command === 'status') fail('connection_unavailable');
  }
  if (!settings.config || settings.generic === true) {
    settings.config = path.join(settings.installRoot, 'packages/task-generic-files/service-config.mjs');
    settings.generic = true;
    const selected = settings.agentExecutable ?? (await discoverAgents()).find(agent => agent.id === 'qwen')?.resolvedPath;
    if (!selected) fail('agent_unavailable');
    settings.agentExecutable = executable(selected);
    if (!settings.dataDir) {
      const parent = path.join(fs.realpathSync(home), '.marshal-node');
      privateDir(parent);
      settings.dataDir = path.join(parent, 'generic-team');
    }
  }
  // Configuration remains trusted local deployment code; detection is not an adapter/permission policy.
  const c = fs.lstatSync(settings.config);
  if (!c.isFile() || c.uid !== process.getuid() || (c.mode & 0o022)) fail('unsafe_settings');
  save(file, settings);
  const childArgs = [path.join(settings.installRoot, 'packages/task-service/main.mjs'), '--config', settings.config];
  for (const [option, field] of [['--data-dir', 'dataDir'], ['--port', 'port'], ['--ui', 'ui']]) {
    if (settings[field] !== undefined) childArgs.push(option, String(settings[field]));
  }
  const env = {...process.env};
  if (settings.agentExecutable) env.MARSHAL_AGENT_EXECUTABLE = executable(settings.agentExecutable);
  const child = spawn(process.execPath, childArgs, {env, stdio: ['ignore', 'pipe', 'pipe']});
  let pending = '', admitted = false, failed = false, stopping = false, killTimer, probing = false;
  let probe = Promise.resolve();
  const stop = () => {
    if (stopping) return; stopping = true;
    child.kill('SIGTERM');
    // Only this launcher's own child, never a discovered PID or foreign group.
    // Forced exit is failure, not evidence that Worker cleanup completed.
    killTimer = setTimeout(() => {failed = true; child.kill('SIGKILL');}, stopTimeoutMs);
  };
  const terminate = () => {failed = true; stop();};
  const timer = setTimeout(terminate, startupTimeoutMs);
  child.stderr.on('data', () => {}); // Do not copy trusted-config diagnostics or credentials to the chat.
  child.stdout.on('data', chunk => {
    if (probing || admitted || failed) return;
    pending += chunk.toString('utf8');
    if (Buffer.byteLength(pending) > 65536) {terminate(); return;}
    const newline = pending.indexOf('\n');
    if (newline < 0) return;
    probing = true;
    probe = (async () => {try {
      const record = JSON.parse(pending.slice(0, newline));
      const candidate = {...settings, connectionFile: absolute(record.connectionFile)};
      delete candidate.address;
      const address = serviceAddress(record.address);
      if (address) candidate.address = address;
      if (settings.ui && !address) fail('service_start_failed');
      await connect(candidate);
      if (failed || stopping || child.exitCode !== null || child.signalCode !== null) return;
      save(file, candidate); admitted = true; clearTimeout(timer);
      output({state: 'connected', settingsFile: file, connectionFile: candidate.connectionFile,
        ...(address ? {address} : {}), ...(settings.ui ? {uiUrl: address + '/ui/'} : {})});
    } catch {terminate();}})();
  });
  process.on('SIGINT', stop); process.on('SIGTERM', stop);
  try {
    await new Promise((resolve, reject) => {
      child.once('error', reject);
      child.once('exit', code => {probe.then(() => code === 0 && admitted && !failed ? resolve() : reject(new Error('service_start_failed')), reject);});
    });
  } finally {clearTimeout(timer); clearTimeout(killTimer); process.off('SIGINT', stop); process.off('SIGTERM', stop);}
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  run(process.argv.slice(2)).catch(error => {
    console.error(JSON.stringify({code: codes.has(error.message) ? error.message : 'local_setup_failed'}));
    process.exitCode = 1;
  });
}
