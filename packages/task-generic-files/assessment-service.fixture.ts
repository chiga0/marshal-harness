import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.ts';
import {createGenericFilesReviewWireConfig} from './review-wire.ts';
export default createGenericFilesReviewWireConfig({assessmentContract:'task-review-assessment/v1',provider:createAcpProvider({id:'controlled',env:process.env.MARSHAL_TEST_PROMPT_LOG?{MARSHAL_TEST_PROMPT_LOG:process.env.MARSHAL_TEST_PROMPT_LOG}:{},executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.ts',import.meta.url)), 'short-wire', 'assessment-wire']}),
  managedProvider:createAcpProvider({id:'controlled-managed',executable:process.execPath,
    args:[fileURLToPath(new URL('./agent.fixture.ts',import.meta.url)), 'short-wire', 'assessment-wire']})});
