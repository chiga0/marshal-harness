// Test-only consumer checks; these never mint Core authority or receipts.
import {encode, digest} from '../task-store/store.mjs';
import {parseJson} from '../task-api/http-boundary.mjs';
import {leaderReplyDigest, validLeaderView, validLeaderReplyResponse} from '../task-api/contract.mjs';
import {data, choices, policy, bindPlan, reportFor, consumeDelivery} from './scenario.fixture.mjs';

export const equal = (a, b) => {try {return encode(a).equals(encode(b));} catch {return false;}};
export class LiveError extends Error {constructor(code) {super(code); this.code = code;}}
export const check = (value, code) => {if (!value) throw new LiveError(code);};
const sha = value => /^sha256:[a-f0-9]{64}$/.test(value ?? '');
const text = value => typeof value === 'string' && value.isWellFormed() && !value.includes('\0') && value.trim();

/** Values come from the original Core ticket, not the CLI's predeclared answer.
 * Rechecking these hashes cannot replace Core's current Store/provenance check. */
export function businessReply(taskId, input) {
  const refs = input?.leaderReplyRefs, replies = input?.leaderReplies;
  check(Array.isArray(refs) && refs.length === 1 && Array.isArray(replies) && replies.length === 1, 'original_reply_missing');
  const reply = replies[0], {answer, ...ref} = reply;
  check(equal(ref, refs[0]) && text(reply.requestId) && sha(reply.requestDigest) && choices.includes(answer) &&
    reply.replyDigest === leaderReplyDigest(taskId, reply.requestId, {requestDigest: reply.requestDigest, answer}), 'original_reply_mismatch');
  return {...reply};
}
export function verificationRequest(ticket, readArtifact) {
  const sources = ticket?.input?.inputArtifacts;
  check(Array.isArray(sources) && sources.length === 1 && sources[0].kind === 'input' && sources[0].name === 'sales.json' &&
    sources[0].digest === digest(encode(data)) && sources[0].bytes === encode(data).length && typeof readArtifact === 'function', 'original_input_mismatch');
  const source = sources[0], bytes = readArtifact(source);
  check(bytes instanceof Uint8Array && bytes.byteLength === source.bytes && digest(bytes) === source.digest &&
    equal(parseJson(Buffer.from(bytes)), data), 'original_input_mismatch');
  const reply = businessReply(ticket.taskId, ticket.input);
  check(ticket.input.verification?.binding?.profile === 'task-verification/v1', 'verification_binding_missing');
  return {taskId: ticket.taskId, sales: parseJson(Buffer.from(bytes)), source, reply,
    leaderReplyRefs: ticket.input.leaderReplyRefs, leaderReplies: ticket.input.leaderReplies,
    verification: ticket.input.verification, fileLayout: ticket.input.fileLayout,
    ...(ticket.input.interactionRefs ? {interactionRefs: ticket.input.interactionRefs} : {})};
}
export function validatePlan(task, plan, body, inputId, leaderPolicy) {
  check(task.status === 'awaiting-approval' && plan.taskId === task.id && task.plan?.digest === plan.digest &&
    task.plan.revision === plan.revision, 'plan_identity_mismatch');
  check(plan.nodes?.length === 3 && plan.nodes.every((node, i) => node.id === ['east', 'west', 'verify'][i] &&
    node.role === (i === 2 ? 'verifier' : 'author') && node.providerId === null && text(node.goal) &&
    Array.isArray(node.scope) && node.scope.every(text)), 'plan_nodes_mismatch');
  check(equal(plan.edges, [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]) &&
    equal(plan.budget, body.limits) && equal(plan.deliverables, body.requirements.deliverables) && equal(plan.assumptions, []), 'plan_scope_mismatch');
  const binding = bindPlan({inputArtifacts: [{id: inputId}], proposal: plan});
  const expected = [{policy, description: binding.description}, ...binding.layouts.map(layout => ({layout})),
    ...binding.deliveries.map(delivery => ({delivery})), {profile: leaderPolicy.profile, policyDigest: digest(encode(leaderPolicy)),
      repair: leaderPolicy.repair, review: leaderPolicy.review, publication: leaderPolicy.publication, completion: 'leader-delivery'}];
  const actual = plan.acceptance.map(value => {try {return parseJson(Buffer.from(value));} catch {return null;}});
  check(expected.every(value => actual.filter(item => equal(item, value)).length === 1), 'plan_trusted_contract_missing');
  return {expectedRevision: task.revision, planRevision: plan.revision, planDigest: plan.digest};
}
export async function replyOnce(client, task, view, value, key) {
  check(validLeaderView(view, task.id) && view.taskRevision === task.revision && view.pendingRequest?.status === 'pending' &&
    Date.now() < Date.parse(view.pendingRequest.deadlineAt), 'request_not_current');
  const q = view.pendingRequest;
  if (q.kind === 'business') check(task.status === 'awaiting-answer' && task.plan === null && q.nodeIds.length === 0 &&
    equal(q.options.map(option => option.value).sort(), [...choices].sort()) && choices.includes(value), 'business_request_mismatch');
  else check(q.kind === 'publication' && task.status === 'awaiting-confirmation' && value === 'allow', 'publication_request_mismatch');
  const request = {path: {taskId: task.id, requestId: q.id}, idempotencyKey: key,
    body: {expectedRevision: task.revision, requestDigest: q.requestDigest, ...(q.kind === 'business' ? {answer: value} : {decision: value})}};
  const receipt = await client.request('task.leader.reply', request);
  check(validLeaderReplyResponse({...request.path, body: request.body}, receipt) && receipt.replayed === false, 'reply_receipt_mismatch');
  return {request, receipt};
}
export function assertReplyReplay(original, replay) {
  check(replay?.replayed === true && equal({...replay, replayed: false}, original.receipt), 'reply_replay_changed');
}
export function verifyAcceptance({taskId, planDigest, delivery, proofs, answered, answer}) {
  check(proofs.length === 1 && proofs[0].artifact.taskId === taskId && proofs[0].artifact.kind === 'evidence', 'verification_evidence_missing');
  const proof = parseJson(proofs[0].content), actual = proof.assertions?.[0]?.actual;
  check(proof.profile === 'task-verification-command/v1' && proof.binding?.planDigest === planDigest &&
    proof.binding.policyDigest === digest(encode(policy)) && proof.delivery?.digest === delivery.artifact.digest &&
    proof.delivery.bytes === delivery.artifact.bytes && proof.assertions.length === 1 && proof.assertions[0].name === 'leader-regions' &&
    equal(actual, {report: reportFor(answer), reply: {requestId: answered.receipt.requestId, requestDigest: answered.receipt.requestDigest,
      replyDigest: answered.receipt.replyDigest, answer}}), 'verification_original_reply_mismatch');
  return {evidenceId: proofs[0].artifact.id, evidenceDigest: proofs[0].artifact.digest, executionId: proof.executionId,
    inputDigest: proof.binding.inputDigest, reservationDigest: proof.binding.reservationDigest};
}
export function authorizeReport({task, plan, view, audit, artifact, content, answer, publication, nameFor}) {
  check(validLeaderView(view, task.id) && view.taskRevision === task.revision && view.pendingRequest?.kind === 'publication' &&
    view.pendingRequest.status === 'pending' && view.review?.verdict === 'accept' && audit.acceptance.status === 'passed', 'publication_evidence_missing');
  const a = view.pendingRequest.authorization;
  check(artifact.taskId === task.id && artifact.kind === 'delivery' && artifact.status === 'ready' && artifact.mediaType === 'application/json' &&
    artifact.digest === digest(content) && artifact.bytes === content.byteLength && a.taskId === task.id && a.planDigest === plan.digest &&
    a.artifactId === artifact.id && a.artifactDigest === artifact.digest && a.bytes === artifact.bytes &&
    a.acceptanceDigest === audit.acceptance.digest && a.reviewDigest === view.review.digest &&
    a.targetId === publication.id && a.targetPolicyDigest === publication.policyDigest && a.operation === 'create-if-absent' &&
    a.name === nameFor({taskId: task.id, artifactDigest: artifact.digest}) && Date.parse(a.expiresAt) <= Date.parse(task.deadlineAt) &&
    Date.now() < Date.parse(a.expiresAt), 'publication_authorization_mismatch');
  consumeDelivery(content, answer);
  return {...a};
}
export function authorOverlap(facts) {
  const first = ['east', 'west'].map(nodeId => facts.find(value => value.role === 'author' && value.nodeId === nodeId));
  check(first.every(Boolean) && first[0].workerId !== first[1].workerId && first[0].executionId !== first[1].executionId, 'author_identity_missing');
  const start = Math.max(...first.map(value => Date.parse(value.startedAt))), end = Math.min(...first.map(value => Date.parse(value.agentExitedAt)));
  check(Number.isFinite(start) && Number.isFinite(end) && end > start, 'authors_did_not_overlap'); return end - start;
}
export function expectedReport(ticket) {return reportFor(businessReply(ticket.taskId, ticket.input).answer);}
