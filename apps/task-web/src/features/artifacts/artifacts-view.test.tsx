import {afterEach, beforeAll, describe, expect, it, vi} from 'vitest';
import {render, screen} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ApiError, type DeliveryFile, type Transport} from '@/lib/transport/types';
import {ArtifactsView} from './artifacts-view';
import {sha256Hex} from './downloader';
import {makeDelivery, makeDetail, makeFakeTransport, makePublication} from '../tasks/detail/testing/fixtures';

const CONTENT = 'report-body';
let FILE: DeliveryFile;

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
  FILE = {path: 'out/final-report.md', bytes: CONTENT.length, digest: `sha256:${await sha256Hex(new Blob([CONTENT]))}`};
});

afterEach(() => {
  delete (URL as unknown as {createObjectURL?: unknown}).createObjectURL;
  delete (URL as unknown as {revokeObjectURL?: unknown}).revokeObjectURL;
  vi.restoreAllMocks();
});

describe('成果页（P09 / E15 / E16）', () => {
  it('最终交付文件下载成功并标注下载不等于发布', async () => {
    const clicked = stubSaving();
    const {transport} = makeFakeTransport({getArtifactBearer: async () => new Blob([CONTENT])});
    const detail = makeDetail({latestDelivery: makeDelivery([FILE]), acceptance: {status: 'passed', digest: 'sha256:eee', evidenceIds: ['ev-1']}});
    const user = userEvent.setup();
    render(<ArtifactsView detail={detail} publications={[]} transport={transport} />);

    expect(screen.getByTestId('delivery-file-row')).toHaveTextContent(FILE.digest);
    await user.click(screen.getByTestId('download-button'));
    expect(await screen.findByTestId('download-success')).toHaveTextContent('已保存 final-report.md');
    expect(screen.getByTestId('download-success')).toHaveTextContent('下载不等于发布');
    expect(clicked).toEqual(['final-report.md']);
  });

  it('摘要遭到篡改时拒绝保存且不宣称成功（E17）', async () => {
    const clicked = stubSaving();
    const {transport} = makeFakeTransport({getArtifactBearer: async () => new Blob(['tampered-bytes'])});
    const detail = makeDetail({latestDelivery: makeDelivery([FILE])});
    const user = userEvent.setup();
    render(<ArtifactsView detail={detail} publications={[]} transport={transport} />);
    await user.click(screen.getByTestId('download-button'));
    expect(await screen.findByTestId('download-rejected')).toHaveTextContent('已拒绝保存');
    expect(clicked).toEqual([]);
    expect(screen.queryByTestId('download-success')).toBeNull();
  });

  it('下载服务错误显示 code 与 requestId', async () => {
    stubSaving();
    const {transport} = makeFakeTransport({
      getArtifactBearer: vi.fn(async () => {
        throw new ApiError(503, 'artifact_not_ready', '成果未就绪', 'req-dl-1');
      }) as Transport['getArtifactBearer'],
    });
    const detail = makeDetail({latestDelivery: makeDelivery([FILE])});
    const user = userEvent.setup();
    render(<ArtifactsView detail={detail} publications={[]} transport={transport} />);
    await user.click(screen.getByTestId('download-button'));
    const notice = await screen.findByTestId('error-notice');
    expect(notice).toHaveTextContent('成果未就绪');
    expect(screen.getByTestId('error-request-id')).toHaveTextContent('req-dl-1');
  });

  it('任务失败时不显示整体成功；候选/部分成果缺清单接口如实说明', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({status: 'failed', failureCode: 'worker_failed', latestDelivery: null});
    render(<ArtifactsView detail={detail} publications={[]} transport={transport as Transport} />);
    expect(screen.getByTestId('delivery-empty')).toHaveTextContent('任务失败，无交付成果');
    expect(screen.getByText(/不伪造候选或部分成果列表/)).toBeInTheDocument();
    expect(screen.getByText(/执行结束不代表验收通过/)).toBeInTheDocument();
  });

  it('发布记录只读展示，含后验失败状态；下载与发布明确分开', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({latestDelivery: makeDelivery([])});
    render(<ArtifactsView detail={detail} publications={[makePublication({status: 'postverify-failed'})]} transport={transport} />);
    expect(screen.getByTestId('publication-row')).toHaveTextContent('后验失败');
    const note = screen.getByTestId('download-not-publication');
    expect(note).toHaveTextContent('下载只是将文件保存到本机浏览器目录，不触发、不代表任何发布');
    expect(note).toHaveTextContent('后验失败');
  });

  it('验收读数通过状态展示（E05/E15 区分候选/最终的事实层）', () => {
    const {transport} = makeFakeTransport();
    const detail = makeDetail({acceptance: {status: 'passed', digest: 'sha256:abc', evidenceIds: ['e1', 'e2']}, latestReview: {passed: 2, total: 3, pending: 1}});
    render(<ArtifactsView detail={detail} publications={[]} transport={transport} />);
    const panel = screen.getByTestId('acceptance-panel');
    expect(panel).toHaveTextContent('独立验收通过');
    expect(panel).toHaveTextContent('passed');
    expect(panel).toHaveTextContent('共 3 项，通过 2 项，待定 1 项');
  });
});
