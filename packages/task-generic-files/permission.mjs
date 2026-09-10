import fs from 'node:fs';
import path from 'node:path';
import {MAX_FILE_BYTES} from './policy.mjs';
const deny = () => ({outcome: {outcome: 'cancelled'}});
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const fields = (value, required, optional = []) => value && typeof value === 'object' && !Array.isArray(value) &&
  required.every(key => Object.hasOwn(value, key)) && Object.keys(value).every(key => [...required, ...optional].includes(key));
/** Qwen native ACP arguments, not a tool-title guess or an OS sandbox. */
export function filePermission(ticket, cwd, request) {
  const layout = ticket?.input?.fileLayout, call = request?.toolCall, input = call?.rawInput;
  if (ticket?.role !== 'author' || !path.isAbsolute(cwd ?? '') || !layout || !Array.isArray(request?.options) || !text(input?.file_path)) return deny();
  let allowed, create = false;
  if (call.kind === 'read' && fields(input, ['file_path'], ['offset', 'limit']) &&
    ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 0 && input[key] <= 10000))
    allowed = [...layout.inputs.map(item => item.path), ...layout.allowedPaths];
  else if (call.kind === 'edit' && fields(input, ['file_path', 'content']) && text(input.content) && Buffer.byteLength(input.content) <= MAX_FILE_BYTES) {
    allowed = layout.allowedPaths; create = true;
  } else if (call.kind === 'edit' && fields(input, ['file_path', 'old_string', 'new_string'], ['replace_all']) &&
    [input.old_string, input.new_string].every(value => text(value) && Buffer.byteLength(value) <= MAX_FILE_BYTES) &&
    (input.replace_all === undefined || typeof input.replace_all === 'boolean')) {
    allowed = layout.allowedPaths; create = input.old_string === '';
  } else return deny();
  const target = allowed.find(name => input.file_path === name || input.file_path === path.join(cwd, name));
  if (!target || target.split('/').some(part => !part || part === '.' || part === '..')) return deny();
  try {
    if (fs.realpathSync(cwd) !== cwd || !fs.lstatSync(cwd).isDirectory()) return deny();
    const absolute = path.join(cwd, target), parent = path.dirname(absolute);
    if (fs.realpathSync(parent) !== parent) return deny();
    try {const stat = fs.lstatSync(absolute); if (!stat.isFile() || stat.nlink !== 1 || fs.realpathSync(absolute) !== absolute) return deny();}
    catch (error) {if (!create || error.code !== 'ENOENT') return deny();}
  } catch {return deny();}
  const options = request.options.filter(option => option?.kind === 'allow_once' && option.optionId === 'proceed_once');
  return options.length === 1 ? {outcome: {outcome: 'selected', optionId: options[0].optionId}} : deny();
}
