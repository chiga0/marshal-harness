import fs from 'node:fs';
import {configuration, receipt, hash} from './leader-ports.mjs';
import {parseAssessmentProposal, validateReviewAssessment} from './review-assessment-contract.mjs';
import {encode, digest} from '../task-store/store.mjs';

const registrations = new WeakMap(), evidence = new WeakMap();
export const reviewAssessmentSourceDigest = digest(encode([
  './review-assessment.mjs', './review-assessment-contract.mjs', './leader.mjs', '../task-service/composition.mjs',
].map(name => ({name, digest: digest(fs.readFileSync(new URL(name, import.meta.url)))}))));
const policy = Object.freeze({profile: 'task-review-assessment/v1', sourceDigest: reviewAssessmentSourceDigest});

export function registerReviewAssessments(port, options) {
  configuration(port, 'review');
  const descriptor = options && typeof options === 'object' && !Array.isArray(options)
    ? Object.getOwnPropertyDescriptor(options, 'profile') : null;
  if (registrations.has(port) || !descriptor || !Object.hasOwn(descriptor, 'value') ||
      Reflect.ownKeys(options).length !== 1 || descriptor.value !== policy.profile)
    throw new TypeError('invalid_review_assessment_configuration');
  registrations.set(port, policy);
  return port;
}
export const reviewAssessmentsEnabled = port => registrations.has(port);
export const reviewAssessmentPolicy = port => registrations.get(port) ?? null;

/** Observe one original completion, without replacing its value or receipt. */
export function startReviewWithAssessments(port, options) {
  if (!registrations.has(port) || options.ticket.executionType !== 'review') return port.start(options);
  const ticket = structuredClone(options.ticket), binding = hash(ticket), native = options.provider;
  let calls = 0, captured = null;
  const provider = {...native, start(input) {
    if (++calls !== 1) throw new TypeError('invalid_review_assessment_provider');
    const handle = native.start(input);
    return {...handle, started: handle.started, stop: (...args) => handle.stop(...args),
      completion: Promise.resolve(handle.completion).then(raw => {
        // A capture problem cannot rewrite the Provider's original outcome.
        try {captured = structuredClone(raw);} catch {captured = null;}
        return raw;
      })};
  }};
  const handle = port.start({...options, provider});
  return {...handle, started: handle.started, stop: (...args) => handle.stop(...args),
    completion: Promise.resolve(handle.completion).then(result => {
      const data = receipt(port, ticket, result);
      if (calls === 1 && captured && data.value && data.status === 'completed' && data.cleanup?.cleaned === true) {
        try {
          const parsed = parseAssessmentProposal({ticket, completion: captured});
          if (hash(parsed.report) === hash(data.value)) {
            const assessment = validateReviewAssessment(parsed.assessment, data.value, ticket.input.review);
            evidence.set(result, {port, binding, reportDigest: hash(data.value), assessment: structuredClone(assessment)});
          }
        } catch { /* Core requires this capability; absence never downgrades to v1. */ }
      }
      return result;
    })};
}

export function reviewAssessmentEvidence(port, ticket, result) {
  if (!registrations.has(port)) return null;
  const data = receipt(port, ticket, result), stored = evidence.get(result);
  return stored?.port === port && stored.binding === hash(ticket) && data.value && stored.reportDigest === hash(data.value)
    ? structuredClone(stored.assessment) : null;
}
