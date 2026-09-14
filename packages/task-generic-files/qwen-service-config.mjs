// Explicit Qwen-only file-delivery composition. Never apply these native flags
// to arbitrary ACP providers or treat them as OS containment/custody evidence.
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesConfig} from './index.mjs';
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
export default createGenericFilesConfig({provider: createAcpProvider({id: 'qwen-acp',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env,
  args: ['--acp', '--approval-mode', 'default',
    '--core-tools', 'read_file,write_file,edit,grep_search,glob,list_directory',
    '--exclude-tools', 'run_shell_command,monitor,agent,web_fetch,web_search,mcp__*,read_mcp_resource,tool_search'],
})});
