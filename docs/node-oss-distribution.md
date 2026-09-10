# Node 发行包 OSS 镜像配置

OSS 是现有签名发行资产的运输镜像，不是新的发行批准者。该流程不构建、不签名、不改变版本或信任公钥，不配置 Agent，不向内网 ECS 安装 GitHub runner。旧 Go RC1 `release.yml` 不受影响。

## GitHub 配置

在仓库 Settings → Environments 新建 `oss-release`；建议设置维护者审批和 main/release tag 保护。上传凭据仅进入同步步骤，不进入下载准备、安装用户或 Agent 上下文。

在 `oss-release` 的 **Secrets** 配置：

| 名称 | 内容 |
| --- | --- |
| `MARSHAL_OSS_ACCESS_KEY_ID` | 专用 RAM 用户 AccessKey ID |
| `MARSHAL_OSS_ACCESS_KEY_SECRET` | 同一用户 AccessKey Secret |

在仓库 Settings → Secrets and variables → Actions → **Variables** 配置：

| 名称 | 示例/要求 |
| --- | --- |
| `MARSHAL_OSS_ENABLED` | `true`，配置完成后再打开；未开启不上传 |
| `MARSHAL_OSS_BUCKET` | `your-release-bucket` |
| `MARSHAL_OSS_ENDPOINT` | `https://oss-cn-hangzhou.aliyuncs.com`，GitHub runner 可达的标准外网 Endpoint |
| `MARSHAL_OSS_PREFIX` | `marshal`，不带首尾斜杠；版本由安装器追加 |

本实现使用专用 AK，不要求提供到聊天。尚未实现 OIDC/STS 自动换证；不要把短期凭据误当长期 AK 配置。Region 从标准 Endpoint 得到，不需要另配。下载端使用 Bucket HTTPS 地址或可信自定义 HTTPS 域名，不需要 AK。

## 最小 RAM 权限及 Bucket

以下替换 `your-release-bucket` 与前缀后授予专用 RAM 用户。不给删除、ACL 修改、列表或整个账号管理权限。上传程序不修改 Bucket/对象公开权限。

```json
{
  "Version": "1",
  "Statement": [
    {"Effect": "Allow", "Action": ["oss:PutObject", "oss:GetObject"],
     "Resource": ["acs:oss:*:*:your-release-bucket/marshal/*"]},
    {"Effect": "Allow", "Action": ["oss:GetBucketVersioning"],
     "Resource": ["acs:oss:*:*:your-release-bucket"]}
  ]
}
```

使用未开启版本控制的专用 Bucket；开启或暂停版本控制会被拒绝，避免覆盖保护语义不一致。每个对象上传使用 `x-oss-forbid-overwrite`，存在时仅接受完全相同字节，不静默覆盖。分发目录应禁止其他写入者；这里不是 WORM 存储。需要修改已同步的安装器时使用新发布版本/新前缀，不覆盖已有版本目录。

若要匿名一键下载，由管理员只开放发行前缀的 `GetObject`（不要开放写入或包含私密文件的整个 Bucket）。当前镜像 URL 模式只支持无需鉴权的 HTTPS 目录，不接受带 query 的签名目录 URL。私有分发可由可信人员下载六个文件，再在目标机用离线模式安装。无浏览器调用，不需要 CORS；不要求 CDN。

## 触发和验证

合入 main 后，Actions → **Node OSS mirror** → Run workflow → main → `v1.0.1` 可补同步已发布版本。未来 Release `published` 自动触发；只有与安装器固定版本一致的公开非 prerelease 版本可同步。更新版本时需同时维护安装器 pins、`sync-node-oss.py` 的版本准入及相关测试。若上游用 `GITHUB_TOKEN` 创建 Release，事件可能不触发其他 workflow，应显式手动触发，不能把未运行当同步成功。

工作流从 GitHub 获取原资产，验证固定签名、公钥、ZIP/manifest/helper 摘要后，复制到 `<prefix>/<version>/`。已有不同字节立即拒绝；每项上传后回读验证，安装器最后上传。失败可以重跑相同字节补齐，不更新 `latest`，不删除失败现场。不需要 minisign 私钥；GitHub 和 OSS 下载都消费原发行字节。

同步内容：ZIP、`SHA256SUMS`、`SHA256SUMS.minisig`、`manifest.json`、`distribution.mjs`、`install-node.sh`。本次没有打包新的离线 ZIP，六文件同目录即可完全离线安装。

## 用户安装

将示例地址换成实际公开下载目录：

```bash
(
  set -eu
  mirror=https://your-release-bucket.oss-cn-hangzhou.aliyuncs.com/marshal/v1.0.1
  setup_dir="$(mktemp -d)"
  curl -q -fL --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 120 --retry 2 --retry-all-errors \
    "$mirror/install-node.sh" -o "$setup_dir/install-node.sh"
  bash "$setup_dir/install-node.sh" --base-url "$mirror"
)
```

镜像模式所有文件均从该目录获取，不回退 GitHub，不跳过签名验证。安装器本身仍是部署者信任的代码，应只从自己受控的发布入口取得。

完全离线时，把上述六个文件原样上传到一个目录：

```bash
bash /mnt/workspace/marshal-offline/install-node.sh \
  --offline-dir /mnt/workspace/marshal-offline
```

仍需 Node >=22、Python3、minisign；不自动安装依赖、覆盖旧安装或启动服务。签名/摘要错误不是网络错误，不允许用重试或关闭验证消除。

实现完成不等于 OSS 部署完成：配置凭据后必须运行一次真实同步，再从目标 sandbox 安装，验证其实际网络与读取权限。当前测试使用模拟 OSS，不声称已完成真实 Bucket 验收。

参考：[OSS 官方防覆盖示例](https://github.com/aliyun/aliyun-oss-python-sdk/blob/master/examples/object_forbid_overwrite.py)、[GitHub workflow 事件触发限制](https://docs.github.com/en/actions/how-tos/writing-workflows/choosing-when-your-workflow-runs/triggering-a-workflow)。
