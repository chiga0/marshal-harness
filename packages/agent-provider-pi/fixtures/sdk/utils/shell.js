export const getShellConfig = shellPath => ({shell: shellPath ?? '/bin/bash', args: ['-c']});
export const getShellEnv = () => ({...process.env});
