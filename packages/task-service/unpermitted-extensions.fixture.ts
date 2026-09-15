// Explicit TEST configuration selector; never distributed. Existing fixtures
// retain their default v3/v4 behavior. Their post-permit deterministic Planner
// inputs are supplied by the Provider fixture, not a wrapped v5 prepare.
const mode = process.env.MARSHAL_V5_EXTENSION;
if (!['repair', 'questions'].includes(mode)) throw Error('test-only v5 extension');
if (mode === 'repair') {
  process.env.MARSHAL_REPAIR_FIXTURE = '1'; process.env.MARSHAL_REPAIR_SCENARIO = 'positive'; process.env.MARSHAL_REPAIR_V5 = '1';
} else {
  process.env.MARSHAL_QUESTION_FIXTURE = '1'; process.env.MARSHAL_QUESTION_SCENARIO = 'positive'; process.env.MARSHAL_QUESTION_V5 = '1';
}
export default (await import(mode === 'repair' ? './same-plan-repair.fixture.mjs' : './runtime-question-recovery.fixture.mjs')).default;
