# 北京大机服务器版试运行

试点为 `beijing-daji` 一台服务器。已知目标是 Linux amd64、Mihomo `v1.19.34`、容器入口 `/mihomo` 和整目录绑定挂载。试装前再次核对镜像 ID、实际启动文件、映射端口及 `127.0.0.1:18443` 可用性。

## 发布候选和只读试装

1. 在 fork 合入通过的 PR。核对 fork 上 `main` 的同一 SHA 的 Server CI 成功，确认 systemd、Docker、迁移、恢复及 30 分钟负载记录。候选 tag 使用 `git tag -a server-v0.1.0-rc.1 <SHA> -m "Server v0.1.0 candidate 1"`，随后只推送这个 tag。修订时递增候选号。
2. 从预发布下载 `metacubexd-server-linux-amd64.tar.gz`、`SHA256SUMS` 和 `build-manifest.json`。核对校验文件中的 amd64 包和清单，以及清单中的源码 SHA。在服务器使用受限权限目录再次校验传入的文件。使用独立版本目录 `/opt/metacubexd-server/<候选版本>`，由 root 持有二进制和 `current` 版本链接。
3. 建立专用 systemd 用户 `metacubexd`。将独立管理员密码写入 `/etc/metacubexd-server/admin`，归专用用户所有，权限 `0600`。如果目标配置了控制器密钥，同样在服务器本地建立 `/etc/metacubexd-server/controller`，并给面板添加 `-controller-secret-file`。以 unit 模板启动，按实际控制端口修改 `-controller` 为可从主机到达的控制器地址。面板监听 `127.0.0.1:18443`。通过 `ssh -N -L 18443:127.0.0.1:18443 beijing-daji` 访问。只读阶段省略 `-state-dir`。
4. 检查连接、规则和日志更新。分别由 Magpie 的 OpenAI 请求、Antigravity Tool 的目标请求及一条非白名单请求建立基线。记录请求结果、命中规则、内核报告的链路。通过相同代理路径访问独立出口探针，只在服务器本地比较预期家宽出口。验收记录写“匹配”或“不匹配”。

## 隔离演练

用已确认的同版本内核与镜像，创建独立的配置目录和测试端口。使用合成配置验证发布、运行态、回滚、内核与面板重启持久性。生产 Mihomo 保持运行。用 `-migration-check` 逐个检查旧脚本，并核实定时任务和其他写入入口。原样备份生产启动文件、挂载目录中必要的资源、托管状态和脚本启用状态。配置、状态、报告原件和完整日志留在服务器受限目录。

宿主内核的校验服务使用固定绝对路径 `-kernel-binary` 和配置数据目录 `-kernel-home`。将校验 unit 的 `User` 改为目标内核用户，令校验目录和 socket 可供面板用户按组访问。Docker 校验 unit 的 `VALIDATOR_ARGS` 指定 `-kernel-home`、`-docker-binary /usr/bin/docker` 和 `-docker-image <sha256:镜像ID>`；Mihomo 以 `/mihomo -t` 在无网络的临时容器中校验。使用 Unix socket `/run/metacubexd-validator/validate.sock`。面板账户需要读取和原子替换该唯一启动文件的目录权限及自己的状态目录；面板 unit 用 `ReadWritePaths` 限定写入目录。现场确认启动文件权限适用于目标内核账户。面板账户不加入 Docker 组。

运行态探针设置用权限受限的 JSON。`Proxy` 是目标实例已有的本机 HTTP 或 mixed 监听器。Docker 转发会改写源地址或端口时，使用宿主机直连网段内该 Mihomo 容器的固定私网 IP 和容器监听端口。发布前核对容器地址；探针按目标域名、源地址和源端口识别自己的连接。`Target` 是纵向对照要访问的受控域名及端口。`BaselinePolicy` 是现行配置的预期策略，托管规则命中时服务会要求实际连接进入指定家宽链路。分别登记 OpenAI、Antigravity 和非白名单目标，使用能接受 CONNECT 的服务。先在隔离环境验证探针可观察到连接。

```json
{
  "Proxy": "127.0.0.1:17890",
  "Probes": [{ "Target": "outside.example:443", "BaselinePolicy": "DIRECT" }]
}
```

## 单次生产发布

1. 持续检查原有调用方及每个入口，至少每秒采样一次。核对备份后暂停旧脚本和其他已发现的写入入口。根据实际目录和 probe 设置给面板添加 `-base-config`、`-state-dir`、`-validator-socket` 和 `-probe-file`。启动后检查显示的基准版本。
2. 从网页发布一份与现行行为等价的配置。验证新旧监控、Magpie OpenAI、Antigravity Tool、非白名单及调用方连通性。计算最长连续失败时间。任一入口连续失败超过 5 秒、链路错走或回滚未验证时立即停止晋级并恢复上一版。生产故障注入不在试装范围内。
3. 连续观察 24 小时。检查配置漂移、代理错误、订阅覆盖、内存曲线和调用方连通性。完成后停止并持久禁用旧写入入口，保留回退备份。将同一个源码 SHA 打稳定 tag `server-v0.1.0`。稳定工作流晋级候选的同一批文件。对比两次 Release 的 SHA-256。

## 回退

- 如果网页仍允许写入，从“配置历史”选择上一个版本，点“回滚并验证”。核查启动文件校验值、运行态监听器、provider、基线请求和现有调用方。
- 发现冻结或运行态无法验证时，先停止面板的写入服务，保全当前状态与备份。以原有部署账户将已核验的备份在同一配置目录中原子替换为启动文件。使用原控制器的受控 `PUT /configs` 重新加载，并检查磁盘校验值和基线路由。恢复旧入口的启用状态，将新面板改回复用只读 unit。保留冻结事务供排查。
- 汇总候选 tag、源码 SHA、包 SHA-256、各探针结果、最长连续失败时间、开始和结束时间、入口和写入脚本启用状态。对无法执行的检查标明未验证。源码、构建输入或包变化后重新发布候选并重跑验收。
