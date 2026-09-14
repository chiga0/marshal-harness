import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesReviewWireConfig} from './review-wire.mjs';
export default createGenericFilesReviewWireConfig({provider:createAcpProvider({id:'controlled',executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.mjs',import.meta.url)), 'short-wire', 'review-wire']})});
