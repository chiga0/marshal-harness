import {pathToFileURL} from 'node:url';
import {startTaskService} from './composition.mjs';
import {parseLaunchArguments, prepareLaunch} from './launch.mjs';
import {safeManagedDiagnostic} from '../task-application/leader-ports.mjs';

// The module is trusted deployment code, never an HTTP-provided plugin/argv.
async function main(argv) {
  const args = parseLaunchArguments(argv);
  if (args.help) {
    process.stdout.write('固定 Node 服务：--config /absolute/trusted/config.mjs [--data-dir /absolute/private/data] [--mode auto|create|open] [--port 0]\n' +
      '默认数据目录 HOME/.marshal-node/task-service；不存在才创建，已有根只按原格式打开。--root 保留为 --data-dir 的互斥旧别名。\n');
    return;
  }
  const configuration = (await import(pathToFileURL(args.config).href)).default;
  if (!configuration || typeof configuration !== 'object' || Array.isArray(configuration)) throw new Error('configuration');
  const target = prepareLaunch(args); let service, managedDiagnostics = 0;
  try {
    target.check();
    service = await startTaskService({...configuration, root: target.root, mode: target.mode, port: args.port,
      onDiagnostic: report => {
        const diagnostic = safeManagedDiagnostic(report);
        if (diagnostic && managedDiagnostics < 32) {managedDiagnostics++; process.stderr.write(JSON.stringify(diagnostic) + '\n');}
        if (report.code.startsWith('service_')) process.exitCode = 1;
        return configuration.onDiagnostic?.(report);
      }});
    target.check(); // Composition now owns the same named private root graph.
  } catch (error) {
    if (service) await service.shutdown();
    throw error;
  } finally {target.close();}
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
