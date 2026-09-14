// New-install Qwen files-only composition; not OS containment.
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesReviewWireConfig} from './review-wire.mjs';
import {QWEN_FILE_ARGS, QWEN_FILE_TOOLS, QWEN_EXCLUDED_TOOLS} from './qwen-file-tools.mjs';
const env = {};
for (const key of ['HOME', 'PATH', 'LANG', 'LC_ALL', 'LC_CTYPE', 'TMPDIR']) {
  if (typeof process.env[key] === 'string') env[key] = process.env[key];
}
// Native whole-tool deny removes these schemas from Qwen's registry.
// Keep the existing core allowlist: an empty --core-tools list is not deny-all.
export const QWEN_MANAGED_ARGS = Object.freeze([
  '--acp', '--approval-mode', 'default', '--core-tools', QWEN_FILE_TOOLS.join(','),
  '--exclude-tools', [...QWEN_EXCLUDED_TOOLS, ...QWEN_FILE_TOOLS].join(','),
]);
export default createGenericFilesReviewWireConfig({provider: createAcpProvider({id: 'qwen-acp', usageExtension: 'qwen-transcript/v1',
  executable: process.env.MARSHAL_AGENT_EXECUTABLE, env, args: QWEN_FILE_ARGS}),
  managedProvider: createAcpProvider({id: 'qwen-managed-acp', usageExtension: 'qwen-transcript/v1',
    executable: process.env.MARSHAL_AGENT_EXECUTABLE, env, args: QWEN_MANAGED_ARGS})});
