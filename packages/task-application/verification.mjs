import {encode, digest} from '../task-store/store.mjs';
import {checkedGraph} from './graph.mjs';
import {clone, isText, reject} from './model.mjs';

const ports = new WeakMap(), receipts = new WeakMap();
const hash = value => digest(encode(value));
const check = (value, code = 'unsupported_task') => { if (!value) reject(code, 422); };
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const keys = (value, names) => object(value) && Object.keys(value).every(key => names.includes(key));
const sha = value => typeof value === 'string' && /^sha256:[a-f0-9]{64}$/.test(value);
const same = (a, b) => hash(a) === hash(b);
const pathOK = value => isText(value, 1024) && value.normalize('NFC') === value && !/[\\:\x00-\x1f\x7f]/.test(value) &&
  value.split('/').length <= 8 && value.split('/').every(part => part && !part.startsWith('.') && !/[. ]$/.test(part));
function paths(values) {
  check(Array.isArray(values) && values.length <= 64 && values.every(pathOK));
  const ordered = [...values].sort(), aliases = new Map();
  for (const value of ordered) {
    const parts = value.split('/');
    for (let count = 1; count <= parts.length; count++) {
      const prefix = parts.slice(0, count).join('/'), alias = prefix.toLowerCase();
      check(!aliases.has(alias) || aliases.get(alias) === prefix); aliases.set(alias, prefix);
    }
  }
  for (let i = 0; i < ordered.length; i++) for (let j = 0; j < i; j++) {
    const a = ordered[i].toLowerCase(), b = ordered[j].toLowerCase();
    check(a !== b && !a.startsWith(b + '/') && !b.startsWith(a + '/'));
  }
  return ordered;
}
const layoutDigest = layout => hash({profile: 'task-file-business/v1', ...layout});
// task-files produces this byte digest, not Store's canonical JSON digest.
const fileDigest = files => digest(Buffer.from(JSON.stringify(files.map(({path, digest, bytes}) => ({path, digest, bytes})))));

/** Parent-only capability. No public mint/deserialize operation exists. The
 * trusted start closure must launch the independent, bounded checker and retain
 * its ORIGINAL cleanup. A model's role/provider/status is never this capability. */
export function createVerificationPort({id, policy, bindPlan, start, interactionPolicyDigests = [], repairPolicyDigests = [], publicationExpected = null}) {
  check(typeof id === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$/.test(id) &&
    keys(policy, ['id', 'version', 'description']) && isText(policy.id, 128) && isText(policy.version, 128) &&
    isText(policy.description, 4096) && typeof bindPlan === 'function' && typeof start === 'function', 'invalid_verification_config');
  check(Array.isArray(interactionPolicyDigests) && interactionPolicyDigests.length <= 32 && interactionPolicyDigests.every(sha) &&
    new Set(interactionPolicyDigests).size === interactionPolicyDigests.length, 'invalid_verification_config');
  check(Array.isArray(repairPolicyDigests) && repairPolicyDigests.length <= 1 && repairPolicyDigests.every(sha) &&
    (!repairPolicyDigests.length || start.repairBinding?.policyDigest === repairPolicyDigests[0] &&
      start.repairBinding.verificationPolicyDigest === hash(policy)), 'invalid_verification_config');
  check(publicationExpected === null || typeof publicationExpected === 'function', 'invalid_verification_config');
  const port = Object.freeze({id, ...(start.custodyProfile ? {custodyProfile: clone(start.custodyProfile)} : {}), start({ticket, prepared, executionContext}) {
    const binding = hash(ticket), handle = start({ticket, prepared, executionContext});
    check(handle && typeof handle.stop === 'function' && typeof handle.started?.then === 'function' &&
      typeof handle.completion?.then === 'function', 'invalid_verification_result');
    return Object.freeze({started: handle.started, stop: (...args) => handle.stop(...args),
      completion: Promise.resolve(handle.completion).then(raw => {
        check(raw?.type === 'verification' && ['passed', 'failed'].includes(raw.status), 'invalid_verification_result');
        const data = clone(raw), receipt = Object.freeze(Object.create(null));
        receipts.set(receipt, {port, binding, data});
        return Object.freeze({type: 'verification', status: data.status, cleanup: clone(data.cleanup), receipt});
      })});
  }});
  ports.set(port, {policy: clone(policy), bindPlan, publicationExpected, interactionPolicyDigests: [...interactionPolicyDigests],
    repairBinding: repairPolicyDigests.length ? clone(start.repairBinding) : null}); return port;
}

export class TaskVerification {
  constructor(app, port) {
    check(port === null || ports.has(port), 'invalid_verification_config');
    this.app = app; this.port = port;
  }
  supportsQuestions(policyDigest) {return this.port !== null && ports.get(this.port).interactionPolicyDigests.includes(policyDigest);}
  expectedPublication(ticket) {
    const callback = this.port && ports.get(this.port).publicationExpected;
    check(typeof callback === 'function', 'unsupported_task');
    const value = callback({ticket: clone(ticket)});
    check(value !== undefined && typeof value?.then !== 'function' && encode(value).length <= 1048576, 'unsupported_task');
    return clone(value);
  }
  repairBinding(policyDigest) {
    const config = this.port && ports.get(this.port), binding = config && config.repairBinding;
    check(binding && binding.policyDigest === policyDigest && binding.verificationPolicyDigest === hash(config.policy), 'unsupported_task');
    return clone(binding);
  }
  bind(record, plan) {
    if (!this.port) return null;
    this.app.artifacts.requireDepot();
    const {policy, bindPlan} = ports.get(this.port);
    const value = bindPlan({taskInput: clone(record.input), proposal: clone(plan), inputArtifacts: clone(record.inputArtifacts ?? [])});
    check(keys(value, ['nodeId', 'description', 'layouts', 'deliveries']) && isText(value.description, 4096));
    const graph = checkedGraph(plan.nodes, plan.edges), sinks = plan.nodes.filter(node => !graph.outgoing.get(node.id).size);
    check(sinks.length === 1 && sinks[0].id === value.nodeId && sinks[0].role === 'verifier' &&
      (sinks[0].providerId === null || sinks[0].providerId === this.port.id) && plan.nodes.length > 1);
    check(Array.isArray(value.layouts) && value.layouts.length === plan.nodes.length);
    const ids = new Set(), layouts = value.layouts.map(layout => {
      check(keys(layout, ['nodeId', 'inputs', 'allowedPaths']) && graph.incoming.has(layout.nodeId) && !ids.has(layout.nodeId)); ids.add(layout.nodeId);
      check(Array.isArray(layout.inputs) && layout.inputs.length <= 64);
      const inputs = layout.inputs.map(input => {
        check(keys(input, ['path', 'source']) && pathOK(input.path));
        const source = input.source;
        if (source?.kind === 'input') {
          check(keys(source, ['kind', 'id']) && (record.inputArtifacts ?? []).some(ref => ref.id === source.id));
        } else {
          check(keys(source, ['kind', 'nodeId', 'path']) && source.kind === 'upstream' && pathOK(source.path) &&
            graph.incoming.get(layout.nodeId).has(source.nodeId));
        }
        return {path: input.path, source: clone(source)};
      }).sort((a, b) => a.path < b.path ? -1 : 1);
      const allowedPaths = paths(layout.allowedPaths); paths([...inputs.map(item => item.path), ...allowedPaths]);
      return {nodeId: layout.nodeId, inputs, allowedPaths};
    }).sort((a, b) => a.nodeId < b.nodeId ? -1 : 1);
    for (const layout of layouts) for (const input of layout.inputs) if (input.source.kind === 'upstream')
      check(layouts.find(item => item.nodeId === input.source.nodeId).allowedPaths.includes(input.source.path));
    const verifierLayout = layouts.find(item => item.nodeId === value.nodeId);
    check(verifierLayout.allowedPaths.length === 0 && Array.isArray(value.deliveries) && value.deliveries.length > 0 && value.deliveries.length <= 64);
    const deliveries = value.deliveries.map(item => {
      check(keys(item, ['nodeId', 'path', 'targetPath']) && graph.incoming.get(value.nodeId).has(item.nodeId) && pathOK(item.path) && pathOK(item.targetPath));
      return {nodeId: item.nodeId, path: item.path, targetPath: item.targetPath};
    }).sort((a, b) => a.targetPath < b.targetPath ? -1 : 1);
    paths(deliveries.map(item => item.targetPath));
    // Every output of every final delivery branch is retained exactly once.
    const required = [...graph.incoming.get(value.nodeId)].flatMap(nodeId => layouts.find(item => item.nodeId === nodeId).allowedPaths.map(path => ({nodeId, path})));
    check(required.length === deliveries.length && required.every(item => deliveries.filter(value => item.nodeId === value.nodeId && item.path === value.path).length === 1));
    check(same(verifierLayout.inputs, deliveries.map(item => ({path: item.targetPath, source: {kind: 'upstream', nodeId: item.nodeId, path: item.path}}))));
    const binding = {profile: 'task-verification/v1', providerId: this.port.id, policy: clone(policy), policyDigest: hash(policy),
      nodeId: value.nodeId, description: value.description, layouts, deliveries};
    // Explicit supported-profile bound; never truncate requirements/layouts.
    check(encode(binding).length <= 49152);
    const visible = [JSON.stringify({policy, description: value.description}), ...layouts.map(layout => JSON.stringify({layout})),
      ...deliveries.map(delivery => JSON.stringify({delivery}))];
    check(visible.every(item => isText(item, 4096)) && plan.acceptance.length + visible.length <= 32);
    plan.acceptance.push(...visible); return binding;
  }
  configured(binding) {
    check(binding && this.port && binding.providerId === this.port.id &&
      same(binding.policy, ports.get(this.port).policy), 'unsupported_task');
  }
  resolve(task, nodeId, upstream) {
    const binding = task.verification; this.configured(binding);
    const template = binding.layouts.find(item => item.nodeId === nodeId); check(template);
    const inputs = template.inputs.map(item => {
      if (item.source.kind === 'input') return clone(item);
      const source = upstream.find(value => value.nodeId === item.source.nodeId); check(source, 'candidate_manifest_conflict');
      return {path: item.path, source: {kind: 'upstream', workerId: source.workerId, path: item.source.path}};
    });
    return {inputs, allowedPaths: clone(template.allowedPaths)};
  }
  candidate(ticket, value) {
    check(value?.profile === 'task-file-business/v1' && value.taskId === ticket.taskId && value.nodeId === ticket.nodeId &&
      value.workerId === ticket.workerId && value.planDigest === ticket.planDigest && value.reservationDigest === ticket.reservationDigest &&
      value.layoutDigest === layoutDigest(ticket.input.fileLayout) && Array.isArray(value.files), 'candidate_manifest_conflict');
    const files = value.files.map(file => {
      check(keys(file, ['path', 'digest', 'bytes']) && pathOK(file.path) && sha(file.digest) && Number.isSafeInteger(file.bytes) && file.bytes >= 0, 'candidate_manifest_conflict');
      return {path: file.path, digest: file.digest, bytes: file.bytes};
    }).sort((a, b) => a.path < b.path ? -1 : 1);
    check(same(paths(files.map(file => file.path)), ticket.input.fileLayout.allowedPaths) && files.reduce((sum, file) => sum + file.bytes, 0) <= 8388608 &&
      value.manifestDigest === fileDigest(files), 'candidate_manifest_conflict');
    const inputs = ticket.input.fileLayout.inputs.map(item => {
      const source = item.source.kind === 'input' ? ticket.input.inputArtifacts.find(ref => ref.id === item.source.id) :
        ticket.input.upstream.find(entry => entry.workerId === item.source.workerId)?.result.files.find(file => file.path === item.source.path);
      check(source, 'candidate_manifest_conflict'); return {path: item.path, digest: source.digest, bytes: source.bytes};
    });
    check(value.inputDigest === fileDigest(inputs), 'candidate_manifest_conflict');
    // Never duplicate untrusted reports or arbitrary fields in the compact index.
    return {profile: value.profile, taskId: value.taskId, nodeId: value.nodeId, workerId: value.workerId,
      planDigest: value.planDigest, reservationDigest: value.reservationDigest, layoutDigest: value.layoutDigest,
      files, inputDigest: value.inputDigest, manifestDigest: value.manifestDigest};
  }
  receipt(ticket, result) {
    const receipt = receipts.get(result?.receipt);
    check(result?.type === 'verification' && receipt?.port === this.port && receipt.binding === hash(ticket) && result.status === receipt.data.status &&
      same(receipt.data.cleanup, result.cleanup), 'invalid_verification_receipt');
    return receipt.data;
  }
  recheck(tx, task, ticket) {
    this.configured(task.verification);
    check(task.approved?.planDigest === ticket.planDigest && task.plan?.digest === ticket.planDigest &&
      task.verification.nodeId === ticket.nodeId && ticket.executionType === 'verification', 'candidate_manifest_conflict');
    check(this.app.repair.current(task, ticket), 'candidate_manifest_conflict');
    const workers = task.repair || task.leader ? this.app.repair.selected(tx, task) :
      this.app.execution.workers(tx, task).filter(({record}) => record.worker.id !== ticket.workerId);
    check(workers.every(({record}) => record.worker.status === 'completed' && record.cleanup?.cleaned === true), 'candidate_manifest_conflict');
    const current = workers.filter(({record}) => record.ticket.planDigest === ticket.planDigest);
    check(current.length === task.plan.nodes.length - 1 && current.every(({record}) => record.worker.status === 'completed' && record.candidate), 'candidate_manifest_conflict');
    const manifest = current.map(({record}) => ({workerId: record.worker.id, nodeId: record.worker.nodeId,
      resultDigest: record.resultDigest, manifest: record.candidate})).sort((a, b) => a.nodeId < b.nodeId ? -1 : 1);
    check(same(manifest, ticket.input.verification.manifests) && same(task.verification, ticket.input.verification.binding), 'candidate_manifest_conflict');
    if (task.leader) check(task.leader.review?.verdict === 'accept' && task.leader.review.selectionDigest === this.app.leader.selectionDigest(tx, task) &&
      same(this.app.leader.replies(tx, task).refs, ticket.input.leaderReplyRefs), 'candidate_manifest_conflict');
    if (task.runtimeQuestions) check(this.supportsQuestions(task.runtimeQuestions.policyDigest) &&
      same(task.repair ? this.app.runtimeQuestions.inherited(tx, task, workers.flatMap(({record}) => record.interactionRefs ?? [])) :
        this.app.runtimeQuestions.refs(tx, task), ticket.input.interactionRefs), 'candidate_manifest_conflict');
    return manifest;
  }
  stage(ticket, result) {
    const data = this.receipt(ticket, result);
    // Recheck before I/O as well as at the final transaction; never use new-owner
    // or cancelled evidence to create ready metadata. Orphan bytes are harmless.
    const eligible = this.app.transaction(false, tx => {
      const {record, task} = this.app.execution.ticket(tx, ticket);
      if (record.worker.status === 'completed' || !this.app.repair.current(task, ticket)) return false;
      if (task.task.status === 'cancelling' || ['failed', 'cancelled', 'intervention'].includes(task.task.status) || this.app.now() >= ticket.deadline) return false;
      // The original managed launcher can prove a failed spawn was cleaned
      // without ever announcing an execution. This is cleanup, not a checker's
      // independent negative verdict; do not stage evidence or a Decision.
      if (data.status === 'failed' && data.cleanup?.cleaned === true && data.cleanup.started === null &&
          record.executionId === null && record.worker.startedAt === null) return false;
      if (data.cleanup?.cleaned === true)
        check(data.cleanup.started?.executionId === record.executionId && data.cleanup.started?.startedAt === record.worker.startedAt,
          'invalid_verification_receipt');
      this.recheck(tx, task, ticket); return true;
    });
    if (!eligible || data.cleanup?.cleaned !== true) return {data, staged: null};
    if (data.contentRejection != null) {
      const rejection = data.contentRejection, policy = ticket.input.plan.repair;
      const command = this.repairBinding(policy?.policyDigest);
      check(data.status === 'failed' && data.evidence?.content instanceof Uint8Array && data.delivery == null &&
        keys(rejection, ['policyDigest', 'failedAssertions', 'reportDigest']) && rejection.policyDigest === policy.policyDigest &&
        sha(rejection.reportDigest) && Array.isArray(rejection.failedAssertions) && rejection.failedAssertions.length > 0 &&
        rejection.failedAssertions.every(name => command.assertions.includes(name)) && new Set(rejection.failedAssertions).size === rejection.failedAssertions.length,
      'invalid_verification_receipt');
    }
    check(data.cleanup.started && Number.isFinite(Date.parse(data.cleanup.started.startedAt)), 'invalid_verification_receipt');
    if (data.status === 'passed') {
      for (const producer of ticket.input.verification.manifests) for (const file of producer.manifest.files) this.app.artifacts.bytes(file);
      for (const input of ticket.input.inputArtifacts) this.app.artifacts.bytes(input);
    }
    // A negative checker observation is durable even without a report artifact.
    // Never fabricate missing evidence, or admit a failed checker's delivery.
    const outputs = data.status === 'passed' ? [['evidence', data.evidence], ['delivery', data.delivery]] :
      data.evidence == null ? [] : [['evidence', data.evidence]];
    return {data, staged: this.app.artifacts.stageOutputs(outputs)};
  }
}
