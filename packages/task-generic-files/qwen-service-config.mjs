// Explicit Qwen-only file-delivery composition. Never apply these native flags
// to arbitrary ACP providers or treat them as OS containment/custody evidence.
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesConfig} from './index.mjs';
import {QWEN_FILE_ARGS} from './qwen-file-tools.mjs';
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
export default createGenericFilesConfig({provider: createAcpProvider({id: 'qwen-acp',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env,
  args: QWEN_FILE_ARGS,
})});
