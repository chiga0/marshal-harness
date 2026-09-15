// Explicit trusted deployment file for the EXISTING task-service/main.ts.
// Importing this configuration does not start a model or a server.
import {createQwenWindowConfig} from './index.ts';
export default createQwenWindowConfig({qwenEntry: process.env.MARSHAL_QWEN_ENTRY});
