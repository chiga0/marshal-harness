import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.ts';
import {createGenericFilesShortWireConfig} from './short-wire.ts';
export default createGenericFilesShortWireConfig({provider:createAcpProvider({id:'controlled',executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.ts',import.meta.url)), 'short-wire']})});
