import {afterEach, beforeAll, describe, expect, it, vi} from 'vitest';
import {render, screen, within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ApiError, type LeaderRecord, type TaskAuditRecord, type TaskRecord} from '@/lib/transport/types';
import {ArtifactsView} from './artifacts-view';
import {sha256Hex} from './downloader';
import type {ArtifactEntry} from './use-task-artifacts';
import {ARTIFACT_ID, makeArtifact, makeAudit, makeFakeTransport, makeLeader, makeTask, TASK_ID} from '../tasks/detail/testing/fixtures';

const CONTENT = 'report-body';
let DIGEST: string;

function stubSaving(): string[] {
  const clicked: string[] = [];
  Object.defineProperty(URL, 'createObjectURL', {configurable: true, value: vi.fn(() => 'blob:fake')});
  Object.defineProperty(URL, 'revokeObjectURL', {configurable: true, value: vi.fn()});
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push(this.download);
  });
  return clicked;
}

beforeAll(async () => {
  DIGEST = `sha256:${await sha256Hex(new Blob([CONTENT]))}`;
});

afterEach(() => {
  delete (URL as unknown as {createObjectURL?: unknown}).createObjectURL;
  delete (URL as unknown as {revokeObjectURL?: unknown}).revokeObjectURL;
  vi.restoreAllMocks();
});

function ok(artifact = makeArtifact()): ArtifactEntry {
  return {status: 'ok', artifact};
}

function renderView(options: {
  task?: TaskRecord;
  leader?: LeaderRecord | null;
  audit?: TaskAuditRecord | null;
  artifacts?: ArtifactEntry[] | null;
  transportOverrides?: Parameters<typeof makeFakeTransport>[0];
}) {
  const {transport} = makeFakeTransport(options.transportOverrides ?? {});
  return render(
    <ArtifactsView
      task={options.task ?? makeTask()}
      leader={options.leader === undefined ? makeLeader() : options.leader}
      audit={options.audit === undefined ? makeAudit() : options.audit}
      artifacts={options.artifacts === undefined ? [] : options.artifacts}
      transport={transport}
    />,
  );
}

describe('成果页（P09 / E15–E19）', () => {
  it('最终交付成果下载成功并标注下载不等于发布', async () => {
    const clicked = stubSaving();
    const deliveryArtifact = makeArtifact({id: 'art-delivery', name: 'out/final-report.md', kind: 'delivery', status: 'ready', bytes: CONTENT.length, digest: DIGEST});
    renderView({
      task: makeTask({artifactIds: ['art-delivery']}),
      artifacts: [ok(deliveryArtifact)],
      transportOverrides: {getArtifactContent: async () => new Blob([CONTENT])},
    });
    const card = screen.getByTestId('final-delivery');
    expect(card).toHaveTextContent('final-report.md');
    expect(card).toHaveTextContent(DIGEST);
    const user = userEvent.setup();
    await user.click(within(card).getByTestId('download-button'));
    expect(await screen.findByTestId('download-success')).toHaveTextContent('已保存 final-report.md');
    expect(screen.getByTestId('download-success')).toHaveTextContent('下载不等于发布');
    expect(clicked).toEqual(['final-report.md']);
  });

  it('摘要遭到篡改时拒绝保存且不宣称成功（E17）', async () => {
    const clicked = stubSaving();
    const deliveryArtifact = makeArtifact({id: 'art-delivery', name: 'final-report.md', kind: 'delivery', status: 'ready', bytes: CONTENT.length, digest: DIGEST});
    renderView({
      artifacts: [ok(deliveryArtifact)],
      transportOverrides: {getArtifactContent: async () => new Blob(['tampered-bytes'])},
    });
    const user = userEvent.setup();
    await user.click(screen.getByTestId('download-button'));
    expect(await screen.findByTestId('download-rejected')).toHaveTextContent('已拒绝保存');
    expect(clicked).toEqual([]);
    expect(screen.queryByTestId('download-success')).toBeNull();
  });

  it('下载服务错误显示 code 与 requestId', async () => {
    stubSaving();
    const deliveryArtifact = makeArtifact({id: 'art-delivery', name: 'final-report.md', kind: 'delivery', status: 'ready', bytes: CONTENT.length, digest: DIGEST});
    renderView({
      artifacts: [ok(deliveryArtifact)],
      transportOverrides: {
        getArtifactContent: async () => {
          throw new ApiError(503, 'artifact_not_ready', '成果未就绪', 'req-dl-1');
        },
      },
    });
    const user = userEvent.setup();
    await user.click(screen.getByTestId('download-button'));
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('成果未就绪');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-dl-1');
  });

  it('任务失败且无交付：如实说明失败代码，不显示整体成功', () => {
    renderView({task: makeTask({status: 'failed', code: 'worker_failed'}), artifacts: []});
    expect(screen.getByTestId('delivery-empty')).toHaveTextContent('任务失败，无交付成果');
    expect(screen.getByTestId('final-delivery')).toHaveTextContent('worker_failed');
    expect(screen.getByText(/执行结束不代表验收通过/)).toBeInTheDocument();
    expect(screen.queryByTestId('download-button')).toBeNull();
  });

  it('产物按类别分组：部分候选带徽标，无内容时如实空态', () => {
    renderView({
      artifacts: [
        ok(makeArtifact({id: 'art-c1', name: 'region-a.csv', kind: 'candidate', status: 'partial'})),
        ok(makeArtifact({id: 'art-e1', name: 'verify-report.json', kind: 'evidence', status: 'ready'})),
      ],
    });
    const inventory = screen.getByTestId('artifact-inventory');
    expect(inventory).toHaveTextContent('候选成果');
    expect(inventory).toHaveTextContent('验收/后验证据');
    expect(inventory).toHaveTextContent('输入');
    const candidateRow = within(inventory).getAllByTestId('artifact-row')
      .find(row => row.getAttribute('data-artifact-id') === 'art-c1')!;
    expect(candidateRow).toBeDefined();
    expect(candidateRow).toHaveTextContent('部分');
    expect(inventory).toHaveTextContent('region-a.csv');
    expect(inventory).toHaveTextContent('verify-report.json');
    expect(screen.getByTestId('artifact-group-empty-input')).toHaveTextContent('暂无输入');
  });

  it('单个产物元数据加载失败：那条如实不可用，不静默丢弃', () => {
    renderView({
      artifacts: [
        ok(makeArtifact({id: 'art-ok', name: 'ok.json', kind: 'candidate'})),
        {status: 'failed', id: 'art-missing', error: new ApiError(404, 'artifact_not_found', '成果不存在', 'req-a-1')},
      ],
    });
    const unavailable = screen.getByTestId('artifact-unavailable-art-missing');
    expect(unavailable).toHaveTextContent('不可用');
    expect(unavailable).toHaveTextContent('artifact_not_found');
    expect(unavailable).toHaveTextContent('req-a-1');
    expect(screen.getByTestId('artifact-row')).toHaveTextContent('ok.json');
  });

  it('发布与后验状态来自 leader 投影（E18/E19），后验失败如实展示', () => {
    renderView({
      leader: makeLeader({
        publication: {actionId: 'act-pub-1', status: 'succeeded', authorizationDigest: `sha256:${'c'.repeat(64)}`, receiptArtifactId: 'art-receipt'},
        postverify: {actionId: 'act-pv-1', status: 'failed', evidenceArtifactId: 'art-evidence'},
      }),
    });
    const publication = screen.getByTestId('publication-line');
    expect(publication).toHaveTextContent('已成功');
    expect(publication).toHaveTextContent('act-pub-1');
    expect(publication).toHaveTextContent('art-receipt');
    const postverify = screen.getByTestId('postverify-line');
    expect(postverify).toHaveTextContent('发布后验');
    expect(postverify).toHaveTextContent('已失败');
    expect(postverify).toHaveTextContent('art-evidence');
    const note = screen.getByTestId('download-not-publication');
    expect(note).toHaveTextContent('不触发、不代表任何发布');
    expect(note).toHaveTextContent('不重复发布、不标整体成功');
  });

  it('leader 不可用：发布/后验与评审读数如实不可用，无发布记录不误报成功', () => {
    renderView({leader: null});
    expect(screen.getByTestId('publication-unavailable')).toHaveTextContent('Leader 投影不可用');
    expect(screen.getByTestId('verification-unavailable')).toHaveTextContent('Leader 投影不可用');
    expect(screen.queryByTestId('publication-line')).toBeNull();
  });

  it.each(['pending', 'failed', 'passed', 'unknown'] as const)('Leader 不可用仍显示独立验收 %s 的真实摘要与证据', status => {
    renderView({leader: null, audit: makeAudit({acceptance: {status, digest: 'sha256:acceptance-only', evidenceIds: ['acceptance-evidence']}})});
    const acceptance = screen.getByTestId('acceptance-readout');
    expect(within(acceptance).getByTestId('machine-state')).toHaveTextContent(status);
    expect(acceptance).toHaveTextContent('sha256:acceptance-only');
    expect(acceptance).toHaveTextContent('acceptance-evidence');
    expect(screen.getByTestId('verification-unavailable')).toBeInTheDocument();
  });

  it.each(['pending', 'failed', 'unknown', null] as const)('评审 accept 不会覆盖独立验收 %s', status => {
    const leader = makeLeader();
    renderView({
      leader: {...leader, review: {digest: 'sha256:review-only', verdict: 'accept', selectionDigest: 'sha256:selection', policyDigest: 'sha256:policy', workerId: 'reviewer', evidenceIds: ['review-evidence']}},
      audit: status === null ? null : makeAudit({acceptance: {status, digest: null, evidenceIds: []}}),
    });
    expect(screen.getByTestId('review-verdict')).toHaveTextContent('评审通过');
    const acceptance = screen.getByTestId('acceptance-readout');
    expect(acceptance).not.toHaveTextContent('review-evidence');
    expect(acceptance).not.toHaveTextContent('sha256:review-only');
    if (status === null) {
      expect(within(acceptance).getByTestId('acceptance-unloaded')).toHaveTextContent('audit 投影不可用');
      expect(within(acceptance).queryByTestId('machine-state')).toBeNull();
    } else {
      expect(within(acceptance).getByTestId('machine-state')).toHaveTextContent(status);
      expect(acceptance).toHaveTextContent('暂无摘要');
      expect(acceptance).toHaveTextContent('暂无数据');
    }
  });

  it('集中评审 verdict 如实展示；清单整体失败显示占位而非旁造清单', () => {
    renderView({
      leader: makeLeader({
        review: {
          digest: `sha256:${'b'.repeat(64)}`,
          verdict: 'rework',
          selectionDigest: `sha256:${'d'.repeat(64)}`,
          policyDigest: `sha256:${'e'.repeat(64)}`,
          workerId: 'worker-verifier-1',
          evidenceIds: ['ev-1', 'ev-2'],
        },
      }),
      artifacts: null,
    });
    expect(screen.getByTestId('review-verdict')).toHaveTextContent('返工');
    expect(screen.getByTestId('review-verdict')).toHaveTextContent('2 条');
    expect(screen.getByTestId('artifact-list-unavailable')).toBeInTheDocument();
  });
});

describe('夹具与样本一致性', () => {
  it('makeArtifact/makeTask 仍以 TASK_ID/ARTIFACT_ID 为默认', () => {
    expect(ARTIFACT_ID).toBe('artifact-test-0001');
    expect(TASK_ID).toContain('task-test');
    expect(makeArtifact().taskId).toBe(TASK_ID);
  });
});
