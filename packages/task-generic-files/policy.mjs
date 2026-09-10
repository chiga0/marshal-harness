import {encode, digest} from '../task-store/store.mjs';

export const MAX_FILE_BYTES = 16384;
export const RULE = '通用文本文件交付：独立 Agent Review 检查原需求、验收要求和全部真实候选内容；固定程序仅证明实际 UTF-8 文件、批准布局、摘要和完整交付，不证明任意语义正确或外部事实。只交付文件，不执行 SQL、不部署代码、不发布或补数。';
export const GUIDANCE = '按真实需求规划1至8个author与一个唯一verifier终点，可并行或有依赖，不固定节点名称。每个author仅写result.md（非空UTF-8、无NUL、最多16384 bytes），原材料位于inputs/<artifactId>，直接上游为upstream/<nodeId>.md；最终分支全部按results/<nodeId>.md打包。只读自身明确材料，禁止额外文件、shell、网络、MCP或外部操作。Review是独立执行，不在DAG伪造reviewer角色；初始没有自动repair。原目标如需要外部效果，必须澄清仅交付文件是否满足，否则拒绝，不能声称外部目标完成。没有publication，不请求发布；deliver后必须基于原验收与Review总结conclude。';
export const check = (condition, code = 'generic_files_invalid') => {if (!condition) throw Error(code);};
export const equal = (a, b) => encode(a).equals(encode(b));
export function expectedFiles(ticket) {
  const verification = ticket.input?.verification;
  check(verification?.binding && Array.isArray(verification.manifests));
  return verification.binding.deliveries.map(item => {
    const manifest = verification.manifests.find(entry => entry.nodeId === item.nodeId)?.manifest;
    const ref = manifest?.files.find(file => file.path === item.path);
    check(ref && manifest.files.length === 1 && item.path === 'result.md' &&
      item.targetPath === `results/${item.nodeId}.md` && ref.bytes > 0 && ref.bytes <= MAX_FILE_BYTES);
    return {nodeId: item.nodeId, path: item.targetPath, digest: ref.digest, bytes: ref.bytes};
  });
}
export function textBytes(bytes) {
  check(bytes.length > 0 && bytes.length <= MAX_FILE_BYTES);
  const content = new TextDecoder('utf-8', {fatal: true}).decode(bytes);
  check(!content.includes('\0') && content.trim().length > 0 && Buffer.from(content).equals(bytes));
  return content;
}
export function deliveryFrom(depot, ticket) {
  check(depot, 'generic_files_business_unavailable');
  return {profile: 'task-generic-files-delivery/v1', taskId: ticket.taskId, planDigest: ticket.planDigest,
    files: expectedFiles(ticket).map(ref => {
      const bytes = depot.get({digest: ref.digest, bytes: ref.bytes});
      check(bytes.length === ref.bytes && digest(bytes) === ref.digest);
      return {...ref, content: textBytes(bytes)};
    })};
}
