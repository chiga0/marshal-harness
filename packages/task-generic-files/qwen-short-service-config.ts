// Explicit files-only Qwen composition. Neither default nor OS containment.
import {createAcpProvider} from '../agent-provider-acp/index.ts';
import {createGenericFilesShortWireConfig} from './short-wire.ts';
import {QWEN_FILE_ARGS} from './qwen-file-tools.ts';
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
export default createGenericFilesShortWireConfig({provider: createAcpProvider({id: 'qwen-acp',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env,
  args: QWEN_FILE_ARGS,
})});
