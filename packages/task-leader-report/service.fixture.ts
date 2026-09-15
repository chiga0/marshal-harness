// External zero-model provider DI only. All product modules/factories/checkers
// come from the exact verified package. Never added to production inventory.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {verify} from '../task-distribution/index.mjs';
const installed = process.env.MARSHAL_REPORT_TEST_INSTALLED;
const manifest = verify({root: installed, manifestDigest: process.env.MARSHAL_REPORT_TEST_MANIFEST});
assert.equal(manifest.sourceHead, process.env.MARSHAL_REPORT_TEST_SOURCE);
const load = file => import(pathToFileURL(path.join(installed, 'packages', file)).href);
const [{createAcpProvider}, {createLeaderReportConfig}] = await Promise.all([load('agent-provider-acp/index.mjs'), load('task-leader-report/index.mjs')]);
const journal = process.env.MARSHAL_REPORT_TEST_JOURNAL;
const record = value => {const fd = fs.openSync(journal, fs.constants.O_APPEND | fs.constants.O_CREAT | fs.constants.O_WRONLY | fs.constants.O_NOFOLLOW, 0o600);
  try {assert.ok(fs.fstatSync(fd).size < 1024 * 1024); fs.writeFileSync(fd, JSON.stringify(value) + '\n'); fs.fsyncSync(fd);} finally {fs.closeSync(fd);}};
const native = createAcpProvider({id: 'controlled-fixture', executable: process.execPath, args: [fileURLToPath(new URL('./agent.fixture.mjs', import.meta.url))],
  env: {MARSHAL_REPORT_TEST_BARRIER: process.env.MARSHAL_REPORT_TEST_BARRIER}, custodyProfile: {id: 'report-fixture-v1', scope: 'inherited-process-group', eligible: true}});
const provider = {...native, start(input) {
  // ACP intentionally drops all external _meta. This test-only transport mapper
  // generates Pi's native shape from our closed peer verbs; production Pi does
  // that in its trusted native bridge. This is not real Pi/ACP interchangeability.
  const workerId = path.basename(input.cwd), handle = native.start({...input, onPermission: (request, context) => {
    assert.ok(['read', 'write'].includes(request.toolCall.title));
    return input.onPermission({...request, toolCall: {...request.toolCall,
      _meta: {provider: 'pi', toolName: request.toolCall.title}}}, context);
  }});
  return {...handle, started: handle.started.then(value => {record({type: 'started', workerId, value}); return value;}),
    completion: handle.completion.then(value => {record({type: 'finished', workerId, status: value.status, reason: value.reason, cleanup: value.cleanup}); return value;})};
}};
const config = createLeaderReportConfig({provider, reportRoot: process.env.MARSHAL_REPORT_ROOT, readBaseURL: process.env.MARSHAL_REPORT_URL});
record({type: 'configuration', leader: config.leader.policyDigest, review: config.review.policyDigest, verification: config.verification.policyDigest,
  publication: config.publication.configurationDigest});
export default config;
