// Trusted configuration for the existing installed task-service/main.mjs.
// Import validates paths/target but does not start a model or auto-authorize it.
import {createPiLeaderReportConfig} from './index.mjs';
export default createPiLeaderReportConfig({piEntry: process.env.MARSHAL_PI_ENTRY, sdkEntry: process.env.MARSHAL_PI_SDK,
  reportRoot: process.env.MARSHAL_REPORT_ROOT, readBaseURL: process.env.MARSHAL_REPORT_URL});
