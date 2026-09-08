// Deterministic Agent process used only by runtime tests; no model or network.
import { spawn } from 'node:child_process';
if (process.env.NODE_TEAM_RUNTIME_FIXTURE === '1') {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  const task = JSON.parse(input);
  if (task.mode === 'overflow') process.stdout.write('x'.repeat(1024 * 1024 + 1));
  else if (task.mode === 'fail') process.exitCode = 4;
  else if (task.mode === 'hang') setInterval(() => {}, 1000);
  else if (task.mode === 'descendant') {
    const descendant = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore', env: {} });
    process.stdout.write(JSON.stringify({ descendant: descendant.pid }) + '\n');
    setInterval(() => {}, 1000);
  } else {
    await new Promise(resolve => setTimeout(resolve, 150));
    process.stdout.write(JSON.stringify({ name: task.name, content: task.name === 'normalize.mjs' ? 'export const normalize = () => [];' : 'export const summarize = () => ({});' }));
  }
}
