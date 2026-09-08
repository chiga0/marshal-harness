import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {startTaskService} from './composition.mjs';

// The module is trusted deployment code, never an HTTP-provided plugin/argv.
async function main(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = argv[i], value = argv[i + 1];
    if (!['--root', '--mode', '--config', '--port'].includes(key) || !value || Object.hasOwn(args, key)) throw new Error('configuration');
    args[key] = value;
  }
  if (!path.isAbsolute(args['--config'] ?? '') || !args['--root'] || !['create', 'open'].includes(args['--mode']) ||
      args['--port'] !== undefined && !/^(0|[1-9][0-9]{0,4})$/.test(args['--port'])) throw new Error('configuration');
  const configuration = (await import(pathToFileURL(args['--config']).href)).default;
  if (!configuration || typeof configuration !== 'object' || Array.isArray(configuration)) throw new Error('configuration');
  const service = await startTaskService({...configuration, root: args['--root'], mode: args['--mode'], port: Number(args['--port'] ?? 0),
    onDiagnostic: report => {
      if (report.code.startsWith('service_')) process.exitCode = 1;
      return configuration.onDiagnostic?.(report);
    }});
  process.stdout.write(JSON.stringify({profile: service.snapshot().profile, address: service.address, connectionFile: service.connectionFile}) + '\n');
  let stopping = false;
  const stop = async () => {
    if (stopping) return; stopping = true;
    const result = await service.shutdown();
    process.stdout.write(JSON.stringify({state: result.state, clean: result.shutdownClean, code: result.failure}) + '\n');
    process.exitCode = result.failure || !result.shutdownClean ? 1 : 0;
  };
  process.once('SIGTERM', stop); process.once('SIGINT', stop);
}

main(process.argv.slice(2)).catch(() => {
  process.stderr.write('{"code":"service_start_unavailable"}\n');
  process.exitCode = 1;
});
