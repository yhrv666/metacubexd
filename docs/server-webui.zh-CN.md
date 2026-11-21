# MetaCubeXD 服务器版操作说明

每台服务器独立运行一套面板。通过 `ssh -N -L 18443:127.0.0.1:18443 beijing-daji` 建立隧道，在浏览器打开 `http://127.0.0.1:18443`。使用独立的面板管理员密码。目标控制器启用认证时，使用 `-controller-secret-file` 指向权限为 `0600` 的文件。

## 准备写入模式

1. 确认面板控制器地址、启动 YAML 和探针指向同一个 Mihomo 实例。备份 YAML、相关资源和原有托管状态，暂停旧脚本和其他写入入口。
2. 确认面板服务账户能读取 YAML，并在其父目录创建文件和执行原子重命名。父目录应专用于这套配置。默认 unit 使用 `ProtectHome` 和 `PrivateTmp`，宿主机的用户家目录和临时目录不可用，可选用 `/etc/mihomo` 或 `/srv/mihomo` 之类的服务目录。替换后的 YAML 由面板账户以 `0600` 创建，因此 Mihomo 需要以 root 或能读取该文件的同一 UID 运行。Docker 用户和用户命名空间按实际映射后的宿主机 UID 核对。
3. 创建运行态探针 JSON，并以 `0600` 安装到 `/etc/metacubexd-server/probes.json`，令面板服务账户可读。`Proxy` 是现有 HTTP 或 mixed 代理监听器的 `IP:端口`。使用回环地址或宿主机直接连接的私网地址。`Target` 是受控 CONNECT 测试目标的 `域名:端口`。`BaselinePolicy` 是当前规则对该目标的预期策略。以下地址和策略仅为示例。准备覆盖已托管流量与原有流量的探针。

```json
{
  "Proxy": "127.0.0.1:7890",
  "Probes": [{ "Target": "probe.example.com:443", "BaselinePolicy": "DIRECT" }]
}
```

4. 用 [README 中的脚本命令](../README.md#写入模式)启用写入。脚本会检查文件权限，启动独立校验服务，用当前配置做一次离线校验，再重启面板并确认写入状态。失败时恢复原 systemd 设置。面板启动时如发现上次尚未完成的配置事务，会按原流程尝试恢复。首次网页发布和生产复测见[试运行手册](server-pilot.zh-CN.md)。

### 如何找到 Mihomo 的启动路径

- **裸机**可从实际服务的 `systemctl cat <Mihomo-unit>` 查看启动命令和 `-f`、`-d`。实际二进制也可通过 `readlink /proc/<PID>/exe` 查找。资源目录包含 Geo 数据和 provider 等校验所需内容。
- **Docker**可在主机查看容器执行入口和目录挂载。脚本接受容器内的 `-f` 和 `-d` 路径，根据正在运行的容器映射到宿主机。看到文件级绑定、命名卷或容器内部文件时，先配置宿主机目录绑定，再开启写入。Mihomo 升级镜像或更换布局后，按当前容器重跑写入配置。

```bash
docker inspect --format '{{.Path}} {{json .Args}}' mihomo
docker inspect --format '{{range .Mounts}}{{println .Type .Source .Destination .RW}}{{end}}' mihomo
```

只读模式使用面板主机能访问的控制器地址，例如容器通过回环端口映射提供 API 时使用宿主机映射地址。启用写入后，若端口映射会改写探针连接的来源地址或端口，使用宿主机直连网段内可访问的容器私网 IP 和内部 HTTP 或 mixed 端口。确保该地址稳定。校验器使用指定 image ID 的一次性无网络容器，面板进程只通过 Unix socket 调用校验器。

### Go 二进制参数

| 参数                              | 用途                                                                                      |
| --------------------------------- | ----------------------------------------------------------------------------------------- |
| `-listen`                         | 面板回环监听地址，默认 `127.0.0.1:18443`。systemd 更新器的健康检查使用该默认地址。        |
| `-controller`                     | 从面板可到达的 Mihomo 控制器 HTTP(S) 源地址，默认 `http://127.0.0.1:9090`。               |
| `-admin-file`                     | 面板管理员密码文件。更新器和模式脚本读取 `/etc/metacubexd-server/admin`。                 |
| `-controller-secret-file`         | 可选的控制器密钥文件。                                                                    |
| `-base-config`                    | Host 上的启动 YAML 路径，可用于只读预览和迁移报告。                                       |
| `-state-dir`                      | 手工启用写入时的私有状态目录。搭配 `-base-config`、`-probe-file` 和 `-validator-socket`。 |
| `-probe-file`                     | 手工启用写入时的探针 JSON 绝对路径。                                                      |
| `-validator-socket`               | 校验器监听或面板访问的 Unix socket 绝对路径。                                             |
| `-validator-service`              | 启动独立校验进程。                                                                        |
| `-kernel-home`                    | 校验进程读取的 Host 资源目录。                                                            |
| `-kernel-binary`                  | 裸机校验用 Mihomo 绝对路径。                                                              |
| `-docker-binary`、`-docker-image` | 容器校验用 Docker CLI 绝对路径和正在运行的镜像 ID。与 `-kernel-binary` 二选一。           |
| `-docker-entrypoint`              | 容器校验的 Mihomo 二进制绝对路径，默认 `/mihomo`。                                        |
| `-migration-check`                | 输出启动配置的脱敏迁移报告，搭配 `-base-config`。                                         |
| `-legacy-script`                  | 迁移预检时可选的旧脚本路径。                                                              |
| `-h`                              | 显示参数帮助。                                                                            |

## 托管配置

1. 打开「链式代理」，点「添加链路」，从服务器配置或已导入订阅中按入口到出口的顺序选择 2 到 8 个节点。用上下箭头调整顺序。「手动录入出口」也支持代理字符串和分字段表单。URI 支持 `socks5`、`socks5h`、`http` 和 `https`，保留字符用百分号编码。
2. 为链路添加 OpenAI 或 Antigravity 域名预设，也可填写自定义域名后缀和来源 CIDR。OpenAI 预设按后缀匹配，Antigravity 按旧脚本的主机名清单精确匹配。一行同时有域名和来源时，必须同时命中。只填来源时，该网段的流量均进入指定链路。托管匹配项优先于原规则。
3. 点“脱敏预览”核对链路，再在五分钟内点“发布预览”。预览之后页面会清除录入字段。已保存链路可保留现有凭据或一次性替换。发布成功后以连接页的规则和链路为准。
4. 如果有冲突，重新读取托管状态并确认最近一次发布。如果页面报告外部改写或冻结，先按试运行手册恢复磁盘与运行态的一致性。

## 订阅与迁移

- 打开「订阅与节点」，粘贴 HTTPS Clash YAML 链接并点「导入订阅」。下载成功后即可在链路中选择其中的节点。导入使用 YAML 中的 `proxies`。
- 点订阅旁的「编辑节点」，粘贴 `socks5://user:password@host:port`、`host:port:user:password` 或 `host:port`，再点「添加节点」。刷新只更新远程节点，手动加入的 SOCKS5 和有效的托管规则随版本保留。订阅链接可在「订阅设置」中替换。
- 在服务器本机运行 `metacubexd-server -migration-check -base-config /绝对路径/config.yaml -legacy-script /绝对路径/旧脚本.sh`。脚本参数可省略。预检统计代理、规则和写入迹象，输出不含脚本文本、配置内容或路径。逐个核查 systemd timers、cron、容器及控制器写入入口。动态路径需要人工核查。
- 服务端保留最近 10 个配置版本，审计查询覆盖最近 30 天。历史页可发起回滚。凭据随配置历史保存在私有状态目录，历史页只显示时间和版本标识。连接和内核日志用于实时监控。
