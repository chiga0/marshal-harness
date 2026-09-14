import {describe,it,expect,vi} from 'vitest';
import {render,screen,waitFor} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import contract from '../../../../../packages/task-api/openapi.json';
import type {TaskAuditRecord} from '@/lib/transport/types';
import {WorkerPrompt} from './worker-prompt';
import {sha256Hex} from '../artifacts/downloader';
import {makeArtifact,makeFakeTransport,makeWorker,TASK_ID} from '../tasks/detail/testing/fixtures';
async function setup(changeTask=false,corrupt=false) {
  const worker=makeWorker(), text='实际输入\n'+'x'.repeat(3000),preview=text.slice(0,100);
  const snapshot=makeArtifact({id:'input-snapshot',name:worker.id+'.input.txt',taskId:changeTask?'other-task':TASK_ID,kind:'evidence',status:'ready',mediaType:'text/plain',bytes:new TextEncoder().encode(text).length,digest:'sha256:'+await sha256Hex(new Blob([text]))});
  const value={...contract.components.schemas.Audit.examples[0],taskId:TASK_ID,workers:[worker],prompts:[{workerId:worker.id,text:preview,source:'handed-off-redacted',contextRefs:[],observation:{stage:'handed-off',promptDigest:snapshot.digest,promptBytes:snapshot.bytes,inputDigest:snapshot.digest,reservationDigest:snapshot.digest,preparedAt:worker.startedAt,handedOffAt:worker.startedAt,coverage:'policy-redacted',policy:{id:'retention',version:'1'},snapshot,previewTruncated:true}}]};
  const getArtifactContent=vi.fn(async()=>new Blob([corrupt?'wrong':text]));
  const {transport}=makeFakeTransport({getArtifactContent});
  render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><WorkerPrompt worker={worker} audit={value as unknown as TaskAuditRecord} transport={transport}/></QueryClientProvider>);
  return {text,preview,getArtifactContent};
}
describe('成员实际提示词',()=>{
  it('正文懒展开，完整字节验证后取代明确节选',async()=>{
    const f=await setup(),user=userEvent.setup();
    expect(f.getArtifactContent).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('worker-prompt-expand'));
    expect(screen.getByTestId('worker-prompt-text')).toBeVisible();
    expect(screen.getByTestId('worker-prompt-text').textContent).toBe(f.preview);
    await user.click(screen.getByRole('button',{name:'展开完整留存输入（当前为节选）'}));
    await waitFor(()=>expect(screen.getByTestId('worker-prompt-text').textContent).toBe(f.text));
    expect(f.getArtifactContent).toHaveBeenCalledTimes(1);
  });
  it('跨任务快照拒绝读取，目标不冒充实际提示词',async()=>{
    const f=await setup(true);
    expect(screen.getByText(/输入审计不可验证/)).toBeVisible();
    expect(f.getArtifactContent).not.toHaveBeenCalled();
  });
  it('损坏正文不替代原节选',async()=>{
    const f=await setup(false,true),user=userEvent.setup();
    await user.click(screen.getByTestId('worker-prompt-expand'));
    await user.click(screen.getByRole('button',{name:'展开完整留存输入（当前为节选）'}));
    await screen.findByRole('alert');
    expect(screen.getByTestId('worker-prompt-text')).toBeVisible();
    expect(screen.getByTestId('worker-prompt-text').textContent).toBe(f.preview);
    expect(screen.queryByText('wrong')).toBeNull();
  });
});
