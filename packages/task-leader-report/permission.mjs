import fs from 'node:fs';
import path from 'node:path';
import {fields, regions} from './policy.mjs';
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0');
const deny = () => ({outcome: {outcome: 'cancelled'}});
/** Pi native arguments only. These are command permissions, not an OS sandbox. */
export function filePermission(ticket, cwd, request) {
  if (ticket?.role !== 'author' || !regions.includes(ticket.nodeId) || !path.isAbsolute(cwd ?? '') ||
    !Array.isArray(request?.options)) return deny();
  const call = request.toolCall, input = call?.rawInput, name = call?._meta?.toolName;
  if (call?._meta?.provider !== 'pi' || !text(input?.path)) return deny();
  let target, create = false;
  if (name === 'read' && call.kind === 'read' && Object.keys(input).every(key => ['path', 'offset', 'limit'].includes(key)) &&
    ['offset', 'limit'].every(key => input[key] === undefined || Number.isSafeInteger(input[key]) && input[key] >= 1 && input[key] <= 10000))
    target = ['sales.json', ticket.nodeId + '.json'].find(file => input.path === file || input.path === path.join(cwd, file));
  else if (name === 'write' && call.kind === 'edit' && fields(input, ['path', 'content']) && text(input.content) && Buffer.byteLength(input.content) <= 4096) {
    target = ticket.nodeId + '.json'; create = true;
  } else if (name === 'edit' && call.kind === 'edit') {
    const edits = fields(input, ['path', 'edits']) ? input.edits : fields(input, ['path', 'oldText', 'newText']) ? [{oldText: input.oldText, newText: input.newText}] : null;
    if (!Array.isArray(edits) || edits.length < 1 || edits.length > 8 || !edits.every(edit => fields(edit, ['oldText', 'newText']) &&
      [edit.oldText, edit.newText].every(value => text(value) && Buffer.byteLength(value) <= 4096)) || Buffer.byteLength(JSON.stringify(edits)) > 8192) return deny();
    target = ticket.nodeId + '.json';
  } else return deny();
  if (!target || input.path !== target && input.path !== path.join(cwd, target)) return deny();
  try {
    if (fs.realpathSync(cwd) !== cwd || !fs.lstatSync(cwd).isDirectory()) return deny();
    try {const file = path.join(cwd, target), stat = fs.lstatSync(file);
      if (!stat.isFile() || stat.nlink !== 1 || fs.realpathSync(file) !== file) return deny();
    } catch (error) {if (!create || error.code !== 'ENOENT') return deny();}
  } catch {return deny();}
  const allow = request.options.filter(option => option?.kind === 'allow_once' && option.optionId === 'allow-once');
  return allow.length === 1 ? {outcome: {outcome: 'selected', optionId: 'allow-once'}} : deny();
}
