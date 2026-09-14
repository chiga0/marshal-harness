import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.mjs';
import {createGenericFilesConfig} from './index.mjs';
export default createGenericFilesConfig({provider:createAcpProvider({id:'controlled',executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.mjs',import.meta.url))]})});
