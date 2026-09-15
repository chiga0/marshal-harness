import {encode, digest, makeEvent} from '../task-store/store.mjs';
import {reject, isText, clone} from './model.mjs';

const parse = entry => entry ? JSON.parse(entry.bytes.toString('utf8')) : null;
const idOK = value => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(value);
const indexId = hash => 'digest-' + hash.slice(7);

// SQLite owns manifests and committed-reference history. The depot owns bytes,
// not readiness, Task ownership or acceptance. All filesystem I/O is outside
// transactions, with owner and exact metadata rechecked before commit/return.
export class TaskArtifacts {
  constructor(app, depot) { this.app = app; this.depot = depot; }
  requireDepot() { if (!this.depot) reject('unsupported_operation', 501); }
  bytes(ref) {
    this.requireDepot();
    let bytes;
    try { bytes = this.depot.get({digest: ref.digest, bytes: ref.bytes}); }
    catch { reject('application_unavailable', 503); }
    if (!(bytes instanceof Uint8Array) || bytes.byteLength !== ref.bytes || digest(bytes) !== ref.digest) reject('application_unavailable', 503);
    return Buffer.from(bytes);
  }
  metadata(tx, id) {
    if (!idOK(id)) reject('invalid_request', 400);
    const entry = parse(tx.projection('artifact', id));
    if (!entry || entry.type !== 'manifest' || entry.owner !== 'local-operator') reject('not_found', 404);
    if (entry.artifact.id !== id) reject('application_unavailable', 503);
    return entry.artifact;
  }
  recheck(tx, refs) {
    for (const ref of refs) {
      if (digest(encode(this.metadata(tx, ref.id))) !== digest(encode(ref))) reject('application_unavailable', 503);
    }
  }
  inputs(ids) {
    if (ids === undefined) return [];
    if (!Array.isArray(ids) || ids.length > 32 || ids.some(id => !idOK(id)) || new Set(ids).size !== ids.length) reject('invalid_request', 400);
    if (!ids.length) return [];
    this.requireDepot();
    const refs = this.app.transaction(false, tx => ids.map(id => {
      const ref = this.metadata(tx, id);
      if (ref.kind !== 'input' || ref.taskId !== null || ref.status !== 'ready') reject('artifact_not_ready', 409);
      return ref;
    }));
    for (const ref of refs) this.bytes(ref);
    return refs;
  }
  // Bytes precede the final acceptance transaction. A rollback can leave only
  // unreferenced depot objects, never an externally ready delivery manifest.
  stageOutputs(outputs) {
    this.requireDepot();
    return outputs.map(([kind, output]) => {
      if (!output || !isText(output.name, 255) || typeof output.mediaType !== 'string' || output.mediaType.length > 128 ||
          !/^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/.test(output.mediaType) ||
          !(output.content instanceof Uint8Array) || output.content.byteLength > 8388608) reject('invalid_verification_result', 422);
      const content = Buffer.from(output.content), ref = {digest: digest(content), bytes: content.length}, id = indexId(ref.digest);
      const known = this.app.transaction(false, tx => parse(tx.projection('artifact', id)));
      if (known) {
        if (known.type !== 'blob' || known.digest !== ref.digest || known.bytes !== ref.bytes) reject('application_unavailable', 503);
        this.bytes(ref);
      } else {
        let stored; try { stored = this.depot.put(content); } catch { reject('application_unavailable', 503); }
        if (stored?.digest !== ref.digest || stored?.bytes !== ref.bytes) reject('application_unavailable', 503);
      }
      return {id, known, kind, name: output.name, mediaType: output.mediaType, ref};
    });
  }
  commitOutputs(tx, taskId, staged, source) {
    return staged.map(item => {
      const existing = parse(tx.projection('artifact', item.id));
      // Two outputs may have identical bytes; both still have distinct, bound
      // manifests. Existing indexes must always describe the exact object.
      if (existing && (existing.type !== 'blob' || existing.digest !== item.ref.digest || existing.bytes !== item.ref.bytes) ||
          item.known && digest(encode(existing)) !== digest(encode(item.known))) reject('application_unavailable', 503);
      if (!existing) tx.putProjection('artifact', item.id, 0, source, encode({type: 'blob', ...item.ref}));
      const artifact = item.artifact ?? {id: this.app.newId('artifact'), taskId, name: item.name, kind: item.kind, status: 'ready',
        mediaType: item.mediaType, ...item.ref, createdAt: new Date(this.app.now()).toISOString()};
      tx.putProjection('artifact', artifact.id, 0, source, encode({type: 'manifest', owner: 'local-operator', artifact}));
      return artifact;
    });
  }
  dispatch(request) {
    this.requireDepot();
    if (request.operation === 'input.create') return this.upload(request);
    const artifact = this.app.transaction(false, tx => this.metadata(tx, request.artifactId));
    if (artifact.status !== 'ready') {
      if (request.operation === 'artifact.content') reject('artifact_not_ready', 409);
      return clone(artifact);
    }
    const content = this.bytes(artifact);
    this.app.transaction(false, tx => this.recheck(tx, [artifact]));
    return request.operation === 'artifact.content' ? {artifact: clone(artifact), content} : clone(artifact);
  }
  upload(request) {
    const body = request.body;
    if (!body || typeof body !== 'object' || Array.isArray(body) ||
        Object.keys(body).some(key => !['name', 'mediaType', 'contentBase64'].includes(key)) ||
        !isText(body.name, 255) || typeof body.mediaType !== 'string' || body.mediaType.length > 128 ||
        !/^[a-z0-9][a-z0-9.+-]*\/[a-z0-9][a-z0-9.+-]*$/.test(body.mediaType) ||
        typeof body.contentBase64 !== 'string' || body.contentBase64.length > 349528 ||
        !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(body.contentBase64)) reject('invalid_request', 400);
    const content = Buffer.from(body.contentBase64, 'base64');
    if (content.length > 262144 || content.toString('base64') !== body.contentBase64) reject('invalid_request', 400);
    // Original receipt is historical, not a new claim that missing bytes were
    // repaired. GET always checks current bytes before returning ready.
    const previous = this.app.replay(request);
    if (previous) return previous;
    const ref = {digest: digest(content), bytes: content.length}, id = indexId(ref.digest);
    const known = this.app.transaction(false, tx => parse(tx.projection('artifact', id)));
    if (known) {
      if (known.type !== 'blob' || known.digest !== ref.digest || known.bytes !== ref.bytes) reject('application_unavailable', 503);
      this.bytes(ref); // A committed-but-missing object must never be re-put.
    } else {
      let stored;
      try { stored = this.depot.put(content); } catch { reject('application_unavailable', 503); }
      if (stored?.digest !== ref.digest || stored?.bytes !== ref.bytes) reject('application_unavailable', 503);
    }
    return this.app.mutate(request, tx => {
      const existing = parse(tx.projection('artifact', id));
      if (digest(encode(existing)) !== digest(encode(known))) reject('application_unavailable', 503);
      const artifact = {id: this.app.newId('artifact'), taskId: null, name: body.name, kind: 'input',
        status: 'ready', mediaType: body.mediaType, ...ref, createdAt: new Date(this.app.now()).toISOString()};
      const stream = artifact.id, head = tx.head(stream);
      const event = makeEvent(stream, head.sequence + 1n, {type: 'input.created', artifact});
      const source = {stream, ...tx.append(stream, head, [event])};
      if (!existing) tx.putProjection('artifact', id, 0, source, encode({type: 'blob', ...ref}));
      tx.putProjection('artifact', artifact.id, 0, source, encode({type: 'manifest', owner: 'local-operator', artifact}));
      return {source, result: artifact};
    });
  }
}
