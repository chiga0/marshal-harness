// Deterministic SDK seam only; never imports Pi/model/authentication code.
import fs from 'node:fs/promises';
import path from 'node:path';
const definition = (name, execute) => ({name, label: name, description: 'fixture ' + name,
  parameters: {type: 'object'}, promptSnippet: 'original ' + name, promptGuidelines: ['original guideline'], execute});
export const createReadToolDefinition = cwd => definition('read', async (_id, input) =>
  ({content: [{type: 'text', text: await fs.readFile(path.resolve(cwd, input.path), 'utf8')}]}));
export const createWriteToolDefinition = cwd => definition('write', async (_id, input) => {
  await fs.writeFile(path.resolve(cwd, input.path), input.content); return {content: [{type: 'text', text: 'written'}]};
});
export const createEditToolDefinition = cwd => definition('edit', async (_id, input) => {
  const file = path.resolve(cwd, input.path), source = await fs.readFile(file, 'utf8');
  await fs.writeFile(file, source.replace(input.oldText, input.newText)); return {content: [{type: 'text', text: 'edited'}]};
});
export const createBashToolDefinition = (cwd, {operations}) => definition('bash', async (_id, input, signal) => {
  const output = [];
  const {exitCode} = await operations.exec(input.command, cwd, {signal, timeout: input.timeout, env: process.env, onData: bytes => output.push(bytes)});
  if (exitCode !== 0) throw Error('fixture nonzero');
  return {content: [{type: 'text', text: Buffer.concat(output).toString()}]};
});
export const createGrepToolDefinition = () => definition('grep', async () => { throw Error('fixture unused'); });
export const createFindToolDefinition = () => definition('find', async () => { throw Error('fixture unused'); });
export const createLsToolDefinition = () => definition('ls', async () => { throw Error('fixture unused'); });

