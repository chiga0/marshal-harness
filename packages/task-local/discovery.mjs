import { access, realpath, stat } from 'node:fs/promises';
import { constants } from 'node:fs';
import { isAbsolute, join } from 'node:path';

const commands = ['qwen', 'pi', 'opencode'];

/** 仅发现本机入口；不证明登录、协议、业务能力或启动授权。 */
export async function discoverAgents({ pathValue = process.env.PATH } = {}) {
  if (process.platform !== 'linux' && process.platform !== 'darwin') {
    throw new Error('agent_discovery_platform_unsupported');
  }
  if (pathValue === undefined) return [];
  if (typeof pathValue !== 'string' || Buffer.byteLength(pathValue) > 65536) {
    throw new TypeError('agent_discovery_path_invalid');
  }
  const entries = pathValue.split(':');
  if (entries.length > 256) throw new TypeError('agent_discovery_path_too_many_entries');
  const directories = [...new Set(entries.filter((entry) =>
    entry !== '' && isAbsolute(entry) && !/[\x00-\x1f\x7f]/u.test(entry)))];
  const found = [];
  for (const command of commands) {
    for (const directory of directories) {
      const executable = join(directory, command);
      try {
        const resolvedPath = await realpath(executable);
        const info = await stat(resolvedPath);
        if (!info.isFile() || (info.mode & 0o111) === 0) continue;
        await access(resolvedPath, constants.X_OK);
        found.push({ id: command, command, executable, resolvedPath });
        break;
      } catch (error) {
        if (!['ENOENT', 'ENOTDIR', 'EACCES', 'EPERM', 'ELOOP', 'ENAMETOOLONG'].includes(error.code)) throw error;
      }
    }
  }
  return found;
}
