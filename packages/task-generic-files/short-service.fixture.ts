import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesShortWireConfig} from './short-wire.mjs';
export default createGenericFilesShortWireConfig({provider:createAcpProvider({id:'controlled',executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.mjs',import.meta.url)), 'short-wire']})});
