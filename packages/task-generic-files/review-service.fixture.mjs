import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesReviewWireConfig} from './review-wire.mjs';
export default createGenericFilesReviewWireConfig({provider:createAcpProvider({id:'controlled',env:process.env.MARSHAL_TEST_PROMPT_LOG?{MARSHAL_TEST_PROMPT_LOG:process.env.MARSHAL_TEST_PROMPT_LOG}:{},executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.mjs',import.meta.url)), 'short-wire', 'review-wire']}),
  managedProvider:createAcpProvider({id:'controlled-managed',executable:process.execPath,
    args:[fileURLToPath(new URL('./agent.fixture.mjs',import.meta.url)), 'short-wire', 'review-wire']})});
