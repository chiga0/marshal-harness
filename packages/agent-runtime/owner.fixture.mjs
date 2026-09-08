// Checked-in owner used only to prove inherited-IPC crash cleanup.
import { fileURLToPath } from 'node:url';
import { launchAcp } from './index.mjs';
const agent = fileURLToPath(new URL('./fake-agent.fixture.mjs', import.meta.url));
if (!process.send || process.env.AGENT_RUNTIME_OWNER_FIXTURE !== '1') process.exit(2);
const runtime = await launchAcp({ executable: process.execPath, args: [agent], cwd: process.cwd(),
  deadline: Date.now() + 15000, onUpdate: event => {
    const value = JSON.parse(event.update.content.text);
    if (value.descendantPid) process.send({ type: 'descendant', pid: value.descendantPid });
  } });
await runtime.client.initialize();
const { sessionId } = await runtime.client.newSession({ cwd: process.cwd() });
await runtime.client.prompt(sessionId, [{ type: 'text', text: 'descendant' }]);
process.send({ type: 'started', started: runtime.started });
setInterval(() => {}, 1000);
