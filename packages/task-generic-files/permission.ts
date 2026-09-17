import fs from 'node:fs';
import path from 'node:path';
import {MAX_FILE} from './policy.ts';
const deny = () => ({outcome: {outcome: 'cancelled'}});
// Permissions are native command admission, not an OS sandbox.
export function filePermission(ticket, cwd, request) {
  if (ticket?.role !== 'author' || !path.isAbsolute(cwd ?? '') || !Array.isArray(request?.options)) return deny();
  const call = request.toolCall, original = call?.rawInput;
  if (!original || typeof original !== 'object' || Array.isArray(original)) return deny();
  let raw = original;
  if (Object.hasOwn(original, 'file_path')) {
    if (Object.hasOwn(original, 'path') || Object.hasOwn(original, 'text')) return deny();
    const {file_path, ...rest} = original; raw = {path: file_path, ...rest};
  } else if (Object.hasOwn(original, 'text')) {
    if (Object.hasOwn(original, 'content')) return deny();
    const {text, ...rest} = original; raw = {...rest, content: text};
  }
  if (typeof raw.path !== 'string' || raw.path.includes('\0')) return deny();
  const layout = ticket.input?.fileLayout;
  if (!layout || !['read', 'edit'].includes(call.kind)) return deny();
  const permitted = call.kind === 'read' ? [...layout.inputs.map(x => x.path), ...layout.allowedPaths] : layout.allowedPaths;
  const relative = permitted.find(name => raw.path === name || raw.path === path.join(cwd, name));
  if (!relative) return deny();
  // No unknown arguments or implicit commands. Other adapters require their own normalizer.
  const keys = Object.keys(raw);
  let createByEdit = false;
  if (call.kind === 'read') {
    if (!keys.every(k => ['path', 'offset', 'limit'].includes(k)) || !['offset', 'limit'].every(k => raw[k] === undefined || Number.isSafeInteger(raw[k]) && raw[k] >= (k === 'offset' ? 0 : 1) && raw[k] <= 10000)) return deny();
  } else {
    const bounded = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && Buffer.byteLength(value) <= MAX_FILE;
    const write = keys.length === 2 && keys.includes('content') && bounded(raw.content);
    const edit = keys.every(k => ['path', 'old_string', 'new_string', 'replace_all'].includes(k)) &&
      bounded(raw.old_string) && bounded(raw.new_string) &&
      (raw.replace_all === undefined || typeof raw.replace_all === 'boolean');
    if (!write && !edit) return deny();
    // Qwen uses an empty old_string to create a file, not to overwrite one.
    createByEdit = edit && raw.old_string === '';
    if (createByEdit && (relative !== 'result.md' || raw.new_string.length === 0)) return deny();
  }
  try {
    if (fs.realpathSync(cwd) !== cwd) return deny();
    const filename = path.join(cwd, relative);
    try {const stat = fs.lstatSync(filename); if (createByEdit || !stat.isFile() || stat.nlink !== 1 || fs.realpathSync(filename) !== filename) return deny();}
    catch (error) {if (error.code !== 'ENOENT' || call.kind !== 'edit' || relative !== 'result.md') return deny();}
  } catch {return deny();}
  const options = request.options.filter(x => x?.kind === 'allow_once');
  return options.length === 1 ? {outcome: {outcome: 'selected', optionId: options[0].optionId}} : deny();
}
