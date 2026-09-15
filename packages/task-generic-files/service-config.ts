import {createExecutableAcpProvider} from '../agent-provider-acp/index.ts';
import {createGenericFilesConfig} from './index.ts';
// Native Agent owns credentials. Do not copy arbitrary environment/secrets.
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
export default createGenericFilesConfig({provider: createExecutableAcpProvider({id: 'qwen-acp',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env})});
