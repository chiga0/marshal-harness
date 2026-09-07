# Linux 远端验证执行机

更新：2026-09-07。用途是解除开发机执行新编译测试程序需人工授权的阻塞，不是部署完成或 Linux production authority。

## 当前配置与边界

- 通过用户已有 SSH 认证连接用户授权机器；保持 host key 检查，不复制密码、私钥、Agent 登录或发布凭据。
- 在现有普通账号的独立 `marshal-runner/` 目录配置工具链、缓存和逐次 job。未创建独立 OS 用户，目录隔离不等于与该账号其他进程安全隔离；只运行可信维护者代码，不接公共 PR 自动执行。
- 不使用 root、不改全局 PATH、不安装常驻 runner、不开放监听端口、不修改宿主安全策略。机器现有其他进程不归本任务所有。
- 当前资源为 Linux x86_64、8 核、约 15 GB 内存；初始 `GOMAXPROCS=2`、`go test -p 2`。扩容前复查负载和其他业务占用。
- 工具链固定 Go 1.26.6；官方 Linux amd64 tar.gz 的 SHA-256 为 `708effb774be8237570d0add163225abbdfaf4fca28b2611df167beba4feef89`，安装前与 Go 官方下载元数据核对。
- 官方模块代理本次超时，job 显式使用 `GOPROXY=https://goproxy.cn`，保持 `GOSUMDB=sum.golang.org` 和仓库 `go.sum`；不设置 `GOSUMDB=off`，不修改系统级 Go 配置。模块下载后运行 `go mod verify`。

## 每次验证

1. 固定 sourceHead；远端只获取该 SHA 的源码，不把移动分支名作为最终测试身份。使用源码压缩包时记录包 SHA-256，核对本地同一公开来源取得的摘要。
2. 每次创建独立 job，拒绝覆盖已有目录；不传 `.marshal`、`.git` 凭据、缓存或用户配置。尚未发布的修改须先独立扫描并记录补丁/包摘要，不能用旧基线测试冒充新修改通过。
3. 在 job 设置独立 GOPATH/GOCACHE，GOTOOLCHAIN=local；测试注入精确 sourceHead 的 buildinfo.commit，编译参数与仓库约定一致。
4. 为命令设置总超时和 Go test 超时，保留退出码、日志、源码/工具链身份、测试选择及跳过项。结构性网络/配置失败不原样循环重试。
5. 先验证所改模块，再按风险补充 race、vet/staticcheck、Schema、架构/secret/diff 检查。公共网络源码只走 HTTPS，不关闭 TLS 校验。

## 平台证据不能混用

Linux 编译通过不代表 Darwin 代码被编译；`[no tests to run]` 不能记为该模块测试通过。Darwin 专属 runtime/进程身份/恢复测试由已有 macOS CI 承接。未在本机重跑的新代码不能沿用旧测试结果。

SSH 验证执行机与产品 SandboxProvider 无直接等价关系。产品在 Linux 启动真实 Agent、结果接纳、取消、恢复与部署仍须独立完成对应支持矩阵；本机常驻服务的企业信任/签名也不因远端测试可用而自动解决。

## 已观察问题

- 本机 `/usr/bin/scp` 首次传输退出 137，原因未确认，未反复重试或放宽安全策略。
- 远端 Git smart HTTP clone 超时，但固定 SHA 的 GitHub codeload HTTPS 下载可用，因此基线验证使用源码包；失败目录保留，不把它当成功 checkout。
- 官方 Go 下载页/模块代理与文件下载站的可达性不同，不能仅凭一个首页 HTTP 200 判断完整工具链可用。
- 旧基线 `68e5c8b` 的 CLI renderer 测试把 `/usr/bin/python3` 写死，而这台机器该路径为旧 Python，缺少 `datetime.fromisoformat`。账号中的 Python 3.14 可用，但修正 PATH 不能覆盖绝对路径。保留两次失败证据，不继续原样复跑、不替换系统 Python；该项暂留受支持 macOS/Ubuntu CI。此机器目前只能声明实际通过的 Linux 测试子集，不能声明全仓 CI 环境兼容。

上述路径不承诺公网 GitHub 故障时仍可自动运行；后续需要时接受控缓存/CI，不因此新增自研 runner 平台。
