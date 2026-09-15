// Adapter-local correlation, not Task authority or an adversarial attestation.
export const BRIDGE_PROFILE = 'marshal-pi-native-bridge/v1';
export const BRIDGE_ENV = 'MARSHAL_PI_NATIVE_BRIDGE';
export const BRIDGE_TITLE = 'Marshal approved tool operation';
export const QUESTION_TOOL = 'marshal_ask_user', QUESTION_PROFILE = 'task-runtime-question/v1';
export const TOOL_KINDS = Object.freeze({read: 'read', write: 'edit', edit: 'edit', grep: 'search', find: 'search', ls: 'search', bash: 'execute', [QUESTION_TOOL]: 'think'});
export const TOOL_FACTORIES = Object.freeze({read: 'createReadToolDefinition', write: 'createWriteToolDefinition', edit: 'createEditToolDefinition',
  grep: 'createGrepToolDefinition', find: 'createFindToolDefinition', ls: 'createLsToolDefinition', bash: 'createBashToolDefinition'});
export const text = (value, max) => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= max;
export const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
