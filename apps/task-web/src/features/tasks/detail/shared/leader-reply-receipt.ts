import type {LeaderRequestDTO} from '@/lib/transport/types';
import {useLogicalActionMemory} from './logical-action';

/** 仅本连接已取得的 reply 受理证据；精确绑定请求正文，不改变服务器 pending 状态。
 * 不以 task revision 为键，避免旧 pending 投影随 CAS 更新后重新催用户回答。
 */
export function useLeaderReplyReceipt(taskId: string, request: LeaderRequestDTO | null) {
  const [accepted, setAccepted] = useLogicalActionMemory(
    [taskId, 'leader.reply.received', request?.id ?? null, request?.requestDigest ?? null], () => false,
  );
  return [request !== null && accepted, setAccepted] as const;
}
