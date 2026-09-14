// New-install Qwen files-only composition; not OS containment.
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesReviewWireConfig} from './review-wire.mjs';
import {QWEN_FILE_ARGS} from './qwen-file-tools.mjs';
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
export default createGenericFilesReviewWireConfig({provider: createAcpProvider({id: 'qwen-acp', usageExtension: 'qwen-transcript/v1',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env, args: QWEN_FILE_ARGS})});
