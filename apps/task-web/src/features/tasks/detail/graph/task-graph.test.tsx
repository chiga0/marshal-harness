import {act, render, screen, waitFor, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {afterEach, describe, expect, it, vi} from 'vitest';
import {QueryClient, QueryClientProvider, onlineManager} from '@tanstack/react-query';
import {MemoryRouter} from 'react-router-dom';
import {ApiError, parseGraph, type GraphRecord, type GraphNodeStatus} from '@/lib/transport/types';
import {GraphView, graphPositions, TaskGraph} from './task-graph';
import {makeFakeTransport, makePlan, makeWorker, TASK_ID} from '../shared/test-fakes';
import {taskKeys} from '../query-keys';

const graph = (status: GraphNodeStatus = 'running', planRevision = 3): GraphRecord => ({taskId: TASK_ID, planRevision,
  nodes: [{id: 'east', role: 'author', status, workerIds: ['worker-0001']}, {id: 'west', role: 'author', status: 'completed', workerIds: []}, {id: 'verify', role: 'verifier', status: 'pending', workerIds: []}],
  edges: [{from: 'east', to: 'verify'}, {from: 'west', to: 'verify'}]});

describe('Graph 响应合同', () => {
  it.each(['pending', 'ready', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'unknown'] as const)('保留真实节点状态 %s', status => {
    expect(parseGraph(graph(status), TASK_ID).nodes[0]!.status).toBe(status);
  });
  const invalid: Array<[string, (g: Record<string, any>) => void]> = [
    ['跨Task', g => {g.taskId = 'other';}],
    ['未知顶层字段', g => {g.revision = 9;}],
    ['缺字段', g => {delete g.planRevision;}],
    ['非整数版本', g => {g.planRevision = 1.5;}],
    ['零版本', g => {g.planRevision = 0;}],
    ['超安全版本', g => {g.planRevision = Number.MAX_SAFE_INTEGER + 1;}],
    ['非法ID', g => {g.nodes[0].id = '../east';}],
    ['未知角色', g => {g.nodes[0].role = 'publisher';}],
    ['未知状态', g => {g.nodes[0].status = 'accepted';}],
    ['数组角色不强转', g => {g.nodes[0].role = ['author'];}],
    ['节点字段缺失', g => {delete g.nodes[0].workerIds;}],
    ['未知节点字段', g => {g.nodes[0].goal = 'fake';}],
    ['Worker超限', g => {g.nodes[0].workerIds = Array.from({length: 101}, (_, i) => `w${i}`);}],
    ['坏WorkerID', g => {g.nodes[0].workerIds = ['x/y'];}],
    ['节点超限', g => {g.nodes = Array.from({length: 65}, (_, i) => ({...g.nodes[0], id: `n${i}`}));}],
    ['重复节点', g => {g.nodes.push(g.nodes[0]);}],
    ['悬空边', g => {g.edges[0].from = 'absent';}],
    ['自环', g => {g.edges[0].from = 'verify';}],
    ['循环', g => {g.edges.push({from: 'verify', to: 'east'});}],
    ['重复边', g => {g.edges.push(g.edges[0]);}],
    ['未知边字段', g => {g.edges[0].status = 'done';}],
    ['边超限', g => {g.edges = Array.from({length: 257}, () => g.edges[0]);}],
  ];
  it.each(invalid)('拒绝 %s，不生成替代图', (_name, mutate) => {
    const value = structuredClone(graph()); mutate(value);
    expect(() => parseGraph(value, TASK_ID)).toThrow('任务图响应不符合合同');
  });
  it('允许合同空图；布局只按拓扑而非节点状态', () => {
    expect(parseGraph({...graph(), nodes: [], edges: []}, TASK_ID).nodes).toEqual([]);
    const positions = graphPositions(graph());
    expect(positions[0]!.x).toBe(positions[1]!.x);
    expect(positions[2]!.x).toBeGreaterThan(positions[0]!.x);
    expect(graphPositions(graph('failed')).map(({x,y}) => [x,y])).toEqual(positions.map(({x,y}) => [x,y]));
  });
});

describe('真实任务图展示', () => {
  it('节点可键盘选择，完整目标/依赖与原Worker路由可达；执行完成不称验收', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><GraphView graph={graph()} plan={makePlan()} workers={[makeWorker()]} /></MemoryRouter>);
    const button = screen.getByRole('button', {name: '查看节点 east，作者，执行中'});
    button.focus(); await user.keyboard('{Enter}');
    const details = screen.getByRole('region', {name: '节点详情'});
    expect(details).toHaveTextContent('报告东侧已付款流水');
    expect(details).toHaveTextContent('后继节点：verify');
    expect(within(details).getByRole('link', {name: 'Worker worker-0001'})).toHaveAttribute('href', `/tasks/${TASK_ID}/team/worker-0001`);
    expect(details).toHaveTextContent('不是验收结果');
    await user.click(screen.getByRole('button', {name: '节点列表'}));
    expect(screen.getByRole('list', {name: '任务节点列表'})).toHaveTextContent('依赖：east、west');
    expect(screen.queryByLabelText('任务依赖图画布')).toBeNull();
  });
  it('计划版本不匹配不拼接目标；Worker跨Task不拼接状态', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><GraphView graph={graph('unknown')} plan={makePlan({revision: 4})} workers={[makeWorker({taskId: 'other', status: 'completed'})]} /></MemoryRouter>);
    expect(screen.queryByText('报告东侧已付款流水')).toBeNull();
    await user.click(screen.getByRole('button', {name: '查看节点 east，作者，状态未知'}));
    expect(screen.getByRole('region', {name: '节点详情'})).toHaveTextContent('详情未加载或不匹配');
    expect(screen.getByRole('region', {name: '节点详情'})).not.toHaveTextContent('completed');
  });
  it('空图不渲染假节点', () => {
    render(<MemoryRouter><GraphView graph={{...graph(), nodes: [], edges: []}} plan={null} workers={null} /></MemoryRouter>);
    expect(screen.getByText('服务端图投影没有节点。')).toBeInTheDocument();
    expect(screen.queryByLabelText('任务依赖图画布')).toBeNull();
  });
});

describe('任务图读取/恢复', () => {
  afterEach(() => {onlineManager.setOnline(true); vi.useRealTimers();});
  function mount(getGraph: ReturnType<typeof makeFakeTransport>['transport']['getGraph']) {
    const client = new QueryClient({defaultOptions: {queries: {retry: false, gcTime: 0}}});
    const {transport} = makeFakeTransport({getGraph});
    const view = render(<QueryClientProvider client={client}><MemoryRouter><TaskGraph taskId={TASK_ID} plan={makePlan()} workers={[]} transport={transport} /></MemoryRouter></QueryClientProvider>);
    return {client, ...view};
  }
  it('无计划409明确展示，不以Plan构造图', async () => {
    mount(async () => {throw new ApiError(409, 'plan_conflict', '未冻结', null);});
    expect(await screen.findByRole('alert')).toHaveTextContent('尚无冻结计划');
    expect(screen.queryByRole('button', {name: /查看节点/})).toBeNull();
  });
  it('离线保留已有图并明确暂停/陈旧，不请求新数据', async () => {
    const read = vi.fn().mockResolvedValue(graph());
    const {client} = mount(read);
    await screen.findByRole('button', {name: '查看节点 east，作者，执行中'});
    act(() => {onlineManager.setOnline(false); void client.invalidateQueries({queryKey: taskKeys.graph(TASK_ID)});});
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('网络离线'));
    expect(screen.getByRole('status')).toHaveTextContent('图读数陈旧');
    expect(read).toHaveBeenCalledTimes(1);
  });
  it('读取失败保留旧投影并明确陈旧，手动刷新可恢复', async () => {
    const user = userEvent.setup();
    const read = vi.fn().mockResolvedValueOnce(graph()).mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce(graph('completed'));
    mount(read);
    await screen.findByRole('button', {name: '查看节点 east，作者，执行中'});
    await user.click(screen.getByRole('button', {name: '刷新任务图'}));
    expect(await screen.findByRole('alert')).toHaveTextContent('保留上次图投影');
    expect(screen.getByRole('status')).toHaveTextContent('图读数陈旧');
    await user.click(screen.getByRole('button', {name: '刷新任务图'}));
    await screen.findByRole('button', {name: '查看节点 east，作者，执行完成'});
    expect(screen.queryByRole('alert')).toBeNull();
  });
  it('拒绝较低planRevision而保留已见新图', async () => {
    const user = userEvent.setup();
    mount(vi.fn().mockResolvedValueOnce(graph('running', 4)).mockResolvedValueOnce(graph('pending', 3)));
    await screen.findByRole('button', {name: '查看节点 east，作者，执行中'});
    await user.click(screen.getByRole('button', {name: '刷新任务图'}));
    expect(await screen.findByRole('alert')).toHaveTextContent('stale_graph_response');
    expect(screen.getByText(/计划版本 4/)).toBeInTheDocument();
  });
  it('取消旧读取后迟到响应不能覆盖新状态；卸载也取消请求', async () => {
    let oldFinish!: (value: GraphRecord) => void;
    let oldSignal: AbortSignal | undefined;
    const read = vi.fn().mockResolvedValueOnce(graph('pending')).mockImplementationOnce((_id, options) => {
      oldSignal = options.signal; return new Promise(resolve => {oldFinish = resolve;});
    }).mockResolvedValueOnce(graph('completed'));
    const {client, unmount} = mount(read);
    await screen.findByRole('button', {name: '查看节点 east，作者，待调度'});
    act(() => {void client.invalidateQueries({queryKey: taskKeys.graph(TASK_ID)});});
    await waitFor(() => expect(read).toHaveBeenCalledTimes(2));
    await act(async () => {await client.invalidateQueries({queryKey: taskKeys.graph(TASK_ID)});});
    expect(oldSignal?.aborted).toBe(true);
    await screen.findByRole('button', {name: '查看节点 east，作者，执行完成'});
    await act(async () => {oldFinish(graph('running'));});
    expect(screen.queryByRole('button', {name: '查看节点 east，作者，执行中'})).toBeNull();
    read.mockImplementationOnce((_id, options) => {oldSignal = options.signal; return new Promise(() => {});});
    act(() => {void client.invalidateQueries({queryKey: taskKeys.graph(TASK_ID)});});
    await waitFor(() => expect(read).toHaveBeenCalledTimes(4));
    unmount(); expect(oldSignal?.aborted).toBe(true);
  });
});
