import path from 'node:path';

export const PROTOCOL = 'marshal-owned-acp/v1';
export const DEFAULT_LIMITS = Object.freeze({ inputBytes: 16 * 1024 * 1024,
  outputBytes: 64 * 1024 * 1024, stderrBytes: 1024 * 1024 });
export const CLEANUP_GRACE_MS = 300;
export const CLEANUP_WAIT_MS = 5000;
export const BOOT_WAIT_MS = 10000;
export const BUFFER_BYTES = 64 * 1024;
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
export function validateOptions(value, now = Date.now()) {
  if (!object(value) || Object.keys(value).some(key => !['executable', 'args', 'cwd', 'env', 'deadline', 'limits'].includes(key)) ||
      !text(value.executable) || !path.isAbsolute(value.executable) || !text(value.cwd) || !path.isAbsolute(value.cwd) ||
      !Array.isArray(value.args) || value.args.length > 128 || value.args.some(arg => !text(arg) || Buffer.byteLength(arg) > 32768) ||
      !object(value.env) || Object.keys(value.env).length > 128 || Object.entries(value.env).some(([key, val]) =>
        !/^[A-Za-z_][A-Za-z0-9_]*$/u.test(key) || !text(val)) ||
      !Number.isSafeInteger(value.deadline) || value.deadline <= now || value.deadline - now > 86400000 ||
      !object(value.limits) || Object.keys(value.limits).sort().join(',') !== 'inputBytes,outputBytes,stderrBytes' ||
      Object.values(value.limits).some(limit => !Number.isSafeInteger(limit) || limit < 1 || limit > 512 * 1024 * 1024)) {
    throw new Error('runtime_invalid_options');
  }
  if (Buffer.byteLength(JSON.stringify(value)) > 128 * 1024) throw new Error('runtime_config_limit');
  return structuredClone(value);
}

export function executionId(value) { return typeof value === 'string' && /^[0-9a-f-]{36}$/u.test(value); }
