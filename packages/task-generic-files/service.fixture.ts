import {fileURLToPath} from 'node:url';
import {createAcpProvider} from '../agent-provider-acp/index.ts';
import {createGenericFilesConfig} from './index.ts';
export default createGenericFilesConfig({provider:createAcpProvider({id:'controlled',executable:process.execPath,
  args:[fileURLToPath(new URL('./agent.fixture.ts',import.meta.url))]})});
