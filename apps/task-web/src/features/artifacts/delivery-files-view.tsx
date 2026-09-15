import {useState} from 'react';
import {Button} from '@/components/ui/button';
import type {ArtifactRecord, Transport} from '@/lib/transport/types';
import {readDeliveryFiles, validSaveName, type DeliveryFile} from './delivery-files';
import {saveBlob} from './downloader';

export function DeliveryFilesView({artifact, transport}: {artifact: ArtifactRecord; transport: Transport}) {
  const [files, setFiles] = useState<DeliveryFile[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(false);
  async function load() {
    setBusy(true); setError(false);
    try {setFiles(await readDeliveryFiles(transport, {artifactId: artifact.id, fileName: artifact.name,
      expectedBytes: artifact.bytes, expectedDigest: artifact.digest}));}
    catch {setError(true);}
    finally {setBusy(false);}
  }
  return <section aria-label="包内交付文件" className="space-y-3 border-t border-border pt-3">
    <h3 className="text-sm font-medium">逐文件下载</h3>
    <p className="text-xs text-text-secondary">仅支持已核验的通用文件包。保留原始路径与内容；不在页面执行或预览文件，文件核验不代表程序功能或外部发布通过。</p>
    {!files && <Button size="sm" variant="outline" disabled={busy} onClick={() => void load()}>{busy ? '正在核验文件包…' : '查看包内文件'}</Button>}
    {error && <p role="alert" className="text-sm text-danger">不支持或无法核验此文件包。请使用上方原包下载；未保存包内文件。</p>}
    {files?.map(file => <DeliveryFileRow key={file.path} file={file} />)}
  </section>;
}

function DeliveryFileRow({file}: {file: DeliveryFile}) {
  const [name, setName] = useState(file.path.slice('results/'.length));
  const [saved, setSaved] = useState(false);
  const [failed, setFailed] = useState(false);
  const valid = validSaveName(name);
  return <div className="min-w-0 space-y-2 rounded-md border border-border p-3">
    <p className="break-all text-sm">原始路径：<code>{file.path}</code> · {file.bytes} 字节</p>
    <details className="text-xs text-text-secondary"><summary className="cursor-pointer py-2">校验摘要</summary><code className="break-all">{file.digest}</code></details>
    <details className="text-sm"><summary className="min-h-11 cursor-pointer py-3">更改保存文件名</summary><label className="block space-y-1 text-sm">另存文件名
      <input className="block w-full rounded-md border border-border bg-surface px-3 py-2" value={name}
        aria-invalid={!valid} onChange={event => {setName(event.target.value); setSaved(false);}} />
    </label>
    <p className="text-xs text-text-secondary">默认保留原名。可自行指定扩展名（如 todo.html）；仅更改本地保存名，不修改原始字节。打开下载文件前请自行检查其内容。</p></details>
    {!valid && <p role="alert" className="text-xs text-danger">请输入普通文件名；不允许目录、控制字符或系统保留名。</p>}
    <Button size="sm" disabled={!valid} onClick={() => {
      try {saveBlob(new Blob([new TextEncoder().encode(file.content)], {type: 'application/octet-stream'}), name); setSaved(true); setFailed(false);}
      catch {setFailed(true);}
    }}>下载此文件</Button>
    {saved && <p role="status" className="text-xs">已发起保存：{name}（原始内容未修改）</p>}
    {failed && <p role="alert" className="text-xs text-danger">浏览器保存失败，请重试。</p>}
  </div>;
}
