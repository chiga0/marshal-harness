// Closed, non-authoritative metadata only. Never forward exception text/cause.
const codes = new Set(['supervisor_failed', 'service_supervisor_failed', 'service_owner_unavailable',
  'service_http_unavailable', 'service_cleanup_unconfirmed', 'service_shutdown_unavailable',
  'service_dispose_failed', 'service_custody_close_failed', 'service_close_failed']);
const stages = new Set(['reconcile-or-dispatch', 'close-reconcile', 'failure-fence', 'deadline-reconcile',
  'preparing', 'prepared', 'starting', 'running', 'collecting', 'finishing', 'unknown', 'terminal',
  'provider-stop', 'provider-cleanup', 'provider-completion', 'deadline', 'progress', 'started', 'answer-ack']);
const ports = new Set(['scan', 'reconcile', 'poll', 'settleControl', 'expandDispatch', 'nextWork', 'mayStart',
  'started', 'progress', 'fail', 'finish', 'registerQuestion', 'dispatchAnswer', 'acknowledgeAnswer',
  'custodyBinding', 'bindCustody', 'recordExtraScope']);
export function safeServiceDiagnostic(value) {
  if (!value || !codes.has(value.code)) return null;
  return {code: value.code, ...(stages.has(value.stage) ? {stage: value.stage} : {}),
    ...(ports.has(value.port) ? {port: value.port} : {})};
}
export const terminalServiceDiagnostic = value => codes.has(value?.code) && value.code.startsWith('service_');
