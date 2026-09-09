// Explicit zero-model failure injection after the original ACP process cleanup.
const {default: base} = await import(process.env.MARSHAL_DIAGNOSTIC_BASE_CONFIG ?? new URL('./leader-recovery.fixture.mjs', import.meta.url));
const providers = new Map([...base.providers].map(([id, native]) => [id, {...native, start(input) {
  const handle = native.start(input);
  return {...handle, completion: handle.completion.then(raw => ({...raw, status: 'failed', stopReason: 'error',
    reason: 'pi_agent_error', outputText: 'PRIVATE_OUTPUT_MUST_NOT_ESCAPE', extra: 'PRIVATE_EXTRA_MUST_NOT_ESCAPE'}))};
}}]));
export default {...base, providers};
