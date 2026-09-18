import test from 'node:test';
import assert from 'node:assert/strict';
import {completionReason} from './pi-reviewer-component.ts';
import {safeManagedDiagnostic} from '../packages/task-application/leader-ports.ts';
test('Pi组件只留原固定失败码，未知私有内容归null',()=>{
 const ticket={taskId:'component-task',workerId:'component-worker',providerId:'pi-review-component',executionType:'review'};
 assert.equal(completionReason({status:'failed',stopReason:null,reason:'pi_invalid_frame'},ticket,safeManagedDiagnostic),'pi_invalid_frame');
 assert.equal(completionReason({status:'failed',reason:'PRIVATE /path token-secret'},ticket,safeManagedDiagnostic),null);
 assert.equal(completionReason({status:'completed',stopReason:'end_turn',reason:'pi_agent_stop'},ticket,safeManagedDiagnostic),'pi_agent_stop');
});
