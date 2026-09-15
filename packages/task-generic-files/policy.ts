import {encode, digest} from '../task-store/store.ts';
export const MAX_FILE = 8192;
export const MAX_INPUT = 32768;
export const RULE = '小型 UTF-8 文件交付：独立 Agent 审查是否满足原需求；固定程序只核验实际文件、编码、摘要及完整交付，不证明任意业务事实。不执行候选代码，不授权外部写入、SQL发布或补数。';
export const GUIDANCE = '根据本次原始需求规划，不使用固定业务模板。DAG 仅含1至8个author和一个verifier汇合终点，节点ID由本次任务决定；Review是Core受管阶段，不另加reviewer节点。providerId均为null。每个author只输出result.md（最多8192 UTF-8字节），可读取布局列出的inputs及upstream文件。最终分支连接verifier，其成果逐份保留。用户要求单个完整文件时，不让多个并列作者分别重做整份交付；简单任务可由一个author完成，独立Review仍由Core安排。确有互补分工时，用依赖连接到最终整合author，再连接verifier；verifier只验收，不替作者合并文件。不要压缩原budget；缺少必要信息时ask，不猜答案。repair.scope为plan-authors时仅允许已批准计划中的author，nodeIds空列表不是禁修；仍须实际独立意见、原预算及最多一轮。';
export const check = (value, code = 'generic_files_invalid') => {if (!value) throw Error(code);};
export function utf8(bytes, max = MAX_FILE) {
  check(bytes instanceof Uint8Array && bytes.length > 0 && bytes.length <= max, 'generic_files_size');
  const text = new TextDecoder('utf-8', {fatal: true}).decode(bytes);
  check(!text.includes('\0') && text.trim().length > 0, 'generic_files_text'); return text;
}
export function expectedFiles(ticket) {
  const verification = ticket.input.verification;
  return verification.binding.deliveries.map(item => {
    const manifest = verification.manifests.find(value => value.nodeId === item.nodeId)?.manifest;
    const file = manifest?.files.find(value => value.path === item.path);
    check(file && file.bytes > 0 && file.bytes <= MAX_FILE, 'generic_files_candidate');
    return {path: item.targetPath, digest: file.digest, bytes: file.bytes};
  });
}
export function equal(a, b) {return digest(encode(a)) === digest(encode(b));}
