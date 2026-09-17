// Qwen's --core-tools applies only to its core set, not injected system tools.
// Keep explicit native denials as well; this is not OS sandbox evidence.
export const QWEN_FILE_TOOLS = Object.freeze([
  'read_file', 'write_file', 'edit', 'grep_search', 'glob', 'list_directory',
]);
export const QWEN_EXCLUDED_TOOLS = Object.freeze([
  'run_shell_command', 'monitor', 'agent', 'web_fetch', 'web_search', 'mcp__*',
  'read_mcp_resource', 'tool_search', 'skill', 'artifact', 'record_artifact',
  'get_goal', 'update_goal', 'propose_goal', 'report_findings', 'structured_output',
  'enter_plan_mode', 'exit_plan_mode', 'ask_user_question', 'enter_worktree', 'exit_worktree',
  'workflow', 'list_agents', 'task_stop', 'task_create', 'task_update', 'task_list',
  'team_create', 'team_delete', 'team_plan_approval', 'request_shutdown', 'send_message',
  'display_image', 'image_gen', 'zoom_image', 'notebook_edit', 'todo_write', 'save_memory',
  'lsp', 'cron_create', 'cron_list', 'cron_delete', 'loop_wakeup', 'create_sub_session',
]);
export const QWEN_FILE_ARGS = Object.freeze([
  '--acp', '--approval-mode', 'default', '--core-tools', QWEN_FILE_TOOLS.join(','),
  '--exclude-tools', QWEN_EXCLUDED_TOOLS.join(','),
]);
