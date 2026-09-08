// Wire events include repeated partial-message snapshots, not only final code.
// This is a bounded transport budget, independent from the 64 KiB/file limit.
export const MAX_STDOUT_BYTES = 8 * 1024 * 1024;
export const MAX_STDERR_BYTES = 1024 * 1024;
