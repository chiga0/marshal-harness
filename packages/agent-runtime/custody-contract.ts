import {createHash, createPublicKey, verify} from 'node:crypto';

export const CUSTODY_PROFILE = 'node-execution-custody/v1';
export const MAX_OBSERVATION = 32768;
export const canonical = value => {
  const visit = item => {
    if (item === null || typeof item === 'boolean' || typeof item === 'string' && item.isWellFormed()) return JSON.stringify(item);
    if (typeof item === 'number' && Number.isFinite(item)) return JSON.stringify(item);
    if (Array.isArray(item)) return '[' + item.map(visit).join(',') + ']';
    if (item && Object.getPrototypeOf(item) === Object.prototype) return '{' + Object.keys(item).sort().map(key => JSON.stringify(key) + ':' + visit(item[key])).join(',') + '}';
    throw new Error('custody_invalid_value');
  };
  const bytes = Buffer.from(visit(value));
  if (bytes.length > MAX_OBSERVATION) throw new Error('custody_limit');
  return bytes;
};
export const custodyDigest = value => 'sha256:' + createHash('sha256').update(canonical(value)).digest('hex');
const id = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const keys = (value, names) => value && Object.getPrototypeOf(value) === Object.prototype && Object.keys(value).sort().join(',') === names.sort().join(',');
export function validBinding(value) {
  return keys(value, ['storeId', 'generation', 'taskId', 'workerId', 'commandId', 'reservationDigest', 'inputDigest', 'planDigest', 'deadline', 'ownerExpiresAt', 'executionProfile']) &&
    ['storeId', 'taskId', 'workerId', 'commandId'].every(key => id(value[key])) && /^[1-9][0-9]{0,18}$/.test(value.generation) &&
    sha(value.reservationDigest) && sha(value.inputDigest) && (value.planDigest === null || sha(value.planDigest)) &&
    Number.isSafeInteger(value.deadline) && value.deadline > 0 && Number.isSafeInteger(value.ownerExpiresAt) && value.ownerExpiresAt > 0 &&
    keys(value.executionProfile, ['id', 'scope', 'eligible']) && id(value.executionProfile.id) &&
    value.executionProfile.scope === 'inherited-process-group' && typeof value.executionProfile.eligible === 'boolean';
}
export function validDescriptor(value, binding) {
  if (!keys(value, ['profile', 'custodyId', 'executionId', 'publicKey', 'binding', 'bindingDigest']) || value.profile !== CUSTODY_PROFILE ||
      !id(value.custodyId) || !id(value.executionId) || !validBinding(value.binding) || value.bindingDigest !== custodyDigest(value.binding) ||
      binding && custodyDigest(binding) !== value.bindingDigest || typeof value.publicKey !== 'string' || !/^[A-Za-z0-9+/]{59}=$/.test(value.publicKey)) return false;
  try { return createPublicKey({key: Buffer.from(value.publicKey, 'base64'), format: 'der', type: 'spki'}).asymmetricKeyType === 'ed25519'; } catch { return false; }
}
export function verifyObservation(descriptor, observation) {
  try {
    if (!validDescriptor(descriptor) || !keys(observation, ['payload', 'signature']) ||
        !keys(observation.payload, ['profile', 'custodyId', 'executionId', 'bindingDigest', 'permitReceived', 'cleanup', 'observedAt']) ||
        typeof observation.signature !== 'string' || !/^[A-Za-z0-9+/]{86}==$/.test(observation.signature)) return false;
    const p = observation.payload;
    return p.profile === CUSTODY_PROFILE && p.custodyId === descriptor.custodyId && p.executionId === descriptor.executionId &&
      p.bindingDigest === descriptor.bindingDigest && typeof p.permitReceived === 'boolean' && Number.isFinite(Date.parse(p.observedAt)) &&
      verify(null, canonical(p), createPublicKey({key: Buffer.from(descriptor.publicKey, 'base64'), format: 'der', type: 'spki'}), Buffer.from(observation.signature, 'base64'));
  } catch { return false; }
}
