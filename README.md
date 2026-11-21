# MetaCubeXD 服务器版

这是 [MetaCubeXD](https://github.com/MetaCubeX/metacubexd) 的服务器版 fork。它用一个嵌入网页的 Go 程序连接现有 Mihomo，在 Linux 主机上通过 SSH 隧道管理。桌面端、静态面板和 All-in-One 安装见[上游项目](https://github.com/MetaCubeX/metacubexd)。

## 已实现的功能

- 实时查看流量、连接、规则和 Mihomo 日志。右上角显示内核版本和已部署的服务器 Release tag。
- 导入 HTTPS Clash YAML 订阅，粘贴添加 SOCKS5 节点，刷新订阅时保留手动节点。
- 按入口到出口选择 2 到 8 个节点并调整顺序，按域名、来源 CIDR 或 OpenAI、Antigravity 预设分流。
- 配置发布支持脱敏预览、隔离校验、热加载、运行态验证、失败回滚、历史和审计。
- GitHub Actions 发布 Linux amd64、arm64 二进制包和镜像。主机可用 systemd 定时拉取、校验、切换和回滚面板版本。

首次仅指定密码和控制器地址时会先以只读启动。完成一次[写入模式](#写入模式)配置后，面板默认保持可写，后续重启和 CI/CD 自动更新都会自动沿用可写状态，需要临时停用写入时可执行 `--mode read-only`。

## 快速启动

从[本仓库 Releases](https://github.com/yhrv666/metacubexd/releases)选一个 `server-v*`，下载对应架构的 `metacubexd-server-linux-amd64.tar.gz` 或 `metacubexd-server-linux-arm64.tar.gz`，以及 `SHA256SUMS`。以下以 amd64 为例。请在下载目录执行，并使用面板所在主机能访问的控制器地址。

```bash
archive=metacubexd-server-linux-amd64.tar.gz
awk -v name="$archive" '$2 == name' SHA256SUMS | sha256sum --strict -c - || exit 1
tar -xzf "$archive"

umask 077
read -rsp '面板密码（至少 16 字符）: ' panel_password
echo
printf '%s\n' "$panel_password" > admin
chmod 0600 admin
unset panel_password
./metacubexd-server -admin-file "$PWD/admin" -controller http://127.0.0.1:9090
```

如果 Mihomo 控制器配置了 `secret`，把它存到权限为 `0600` 的文件，并添加 `-controller-secret-file /绝对路径/controller`。从自己的电脑建立 SSH 隧道，然后打开 `http://127.0.0.1:18443`。

```bash
ssh -N -L 18443:127.0.0.1:18443 user@your-server
```

从源码启动时，先在仓库根目录构建，然后使用相同的密码文件和启动参数。工具版本见 [Server CI](.github/workflows/server-ci.yml)。

```bash
pnpm install --frozen-lockfile --filter @metacubexd/ui...
node apps/server-webui/build-ui.mjs
go -C apps/server-webui build -o metacubexd-server .
# 二进制在 apps/server-webui/metacubexd-server
```

## 用 systemd 常驻

在已校验的发布包解压目录执行。把 `tag` 改成实际下载的 tag。下面的命令用于首次安装，请先退出前台进程以释放端口。

```bash
tag=server-v0.1.0-rc.N
units=apps/server-webui/units
root=/opt/metacubexd-server

getent passwd metacubexd >/dev/null ||
  sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin metacubexd
sudo install -d -m 0750 -o root -g metacubexd /etc/metacubexd-server
sudo install -m 0600 -o metacubexd -g metacubexd admin /etc/metacubexd-server/admin
sudo install -D -m 0755 metacubexd-server "$root/$tag/metacubexd-server"
sudo ln -sT "$root/$tag" "$root/current"
sudo install -m 0644 "$units/metacubexd-server.service" "$units/metacubexd-validator.service" /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now metacubexd-server.service
systemctl status metacubexd-server.service
```

默认控制器为 `http://127.0.0.1:9090`。如果需要密钥，像安装 `admin` 一样安装 `controller` 文件。通过 `sudo systemctl edit metacubexd-server.service` 修改连接。无需密钥的实例可省略最后一个参数。保存后执行 `sudo systemctl restart metacubexd-server.service`。

```ini
[Service]
ExecStart=
ExecStart=/opt/metacubexd-server/current/metacubexd-server -listen 127.0.0.1:18443 -admin-file /etc/metacubexd-server/admin -controller http://127.0.0.1:9090 -controller-secret-file /etc/metacubexd-server/controller
```

## 写入模式

首次启用写入时需要启动配置、内核资源目录和运行态探针。按[配置写入指南](docs/server-webui.zh-CN.md#准备写入模式)准备权限、探针和备份，并暂停其他配置写入入口。使用包含此功能的面板和更新脚本即可切换模式，定时拉取可独立启用。

裸金属 Mihomo 使用实际安装的可执行文件、启动配置和资源目录。

```bash
sudo /usr/local/libexec/metacubexd-image-update.py --mode writable \
  --kernel-binary /usr/local/bin/mihomo \
  --config /etc/mihomo/config.yaml --kernel-home /etc/mihomo \
  --probe-file /etc/metacubexd-server/probes.json
```

Docker 中的 Mihomo 使用容器名和容器内路径。脚本读取运行中容器的镜像 ID、入口和挂载关系，并将容器内的配置路径映射到宿主机。配置需在可写的宿主机**目录绑定挂载**内，才能原子替换。容器采用启动包装器时可传 `--docker-entrypoint /容器内/mihomo`。

```bash
sudo /usr/local/libexec/metacubexd-image-update.py --mode writable \
  --container mihomo \
  --config /root/.config/mihomo/config.yaml --kernel-home /root/.config/mihomo \
  --probe-file /etc/metacubexd-server/probes.json
```

首次成功开启写入后，可以复用已保存的设置。切回只读会关闭网页发布入口，已生效的 Mihomo 配置继续运行。脚本会重启面板，浏览器可能需要重新登录。

```bash
sudo /usr/local/libexec/metacubexd-image-update.py --mode status
sudo /usr/local/libexec/metacubexd-image-update.py --mode read-only
sudo /usr/local/libexec/metacubexd-image-update.py --mode writable
```

## 安装 CD 脚本

在发布包解压目录执行。脚本需要 Linux、Python 3.10+ 和 systemd。拉取镜像需要主机可用的 Docker 服务。切换写入还会用到 `curl` 和 `runuser`。

```bash
units=apps/server-webui/units
sudo install -D -m 0755 "$units/metacubexd-image-update.py" /usr/local/libexec/metacubexd-image-update.py
sudo install -m 0644 "$units/metacubexd-image-update.service" "$units/metacubexd-image-update.timer" /etc/systemd/system/
sudo systemctl daemon-reload

sudo /usr/local/libexec/metacubexd-image-update.py --check
sudo systemctl enable --now metacubexd-image-update.timer
systemctl list-timers metacubexd-image-update.timer
sudo /usr/local/libexec/metacubexd-image-update.py --logs
```

定时器约每 10 分钟检查一次。更新器按摘要拉取 GHCR 镜像，失败后下载并校验 Release 镜像归档。它提取面板二进制并切换 `current`。面板登录、控制器或写入模式检查失败时会回退。立即检查并应用更新可用 `sudo systemctl start metacubexd-image-update.service`。暂停定时检查用 `sudo systemctl disable --now metacubexd-image-update.timer`。

先确认 `main` 上对应提交的 Server CI 和打包产物可用，再推送带注释的 tag。以下为候选版示例，请替换实际提交和编号。

```bash
tag=server-v0.1.0-rc.N
sha=REPLACE_WITH_TESTED_MAIN_SHA
git tag -a "$tag" "$sha" -m "Server release $tag"
git push origin "refs/tags/$tag"
```

工作流和镜像地址绑定到 `yhrv666/metacubexd`。其他 fork 需同步修改 [Server CI](.github/workflows/server-ci.yml)、[Server Release](.github/workflows/server-release.yml) 和[更新器](apps/server-webui/units/metacubexd-image-update.py)的仓库设置。代理和生产验收见[北京试运行手册](docs/server-pilot.zh-CN.md)。

维护者可用 `bash apps/server-webui/package.sh /绝对路径/输出目录` 打包已生成网页的工作树。`release-images.sh <已校验产物目录>` 由 Server Release 调用，使用工作流提供的 `GH_REPO`、`SHA`、`TAG`、`GH_TOKEN` 和 `GITHUB_ACTOR` 打包并推送镜像。

### CD 脚本参数

| 参数                       | 用途                                                                             |
| -------------------------- | -------------------------------------------------------------------------------- |
| 无参数                     | 检查并应用可用的更新，通常由 systemd 调用。                                      |
| `--check`                  | 检查 Release 和镜像清单。                                                        |
| `--recover`                | 恢复中断的镜像切换。                                                             |
| `--logs`                   | 查看最近 50 条匹配日志。隐藏例行检查，保留更新、故障和回滚。                     |
| `--all-logs`               | 查看未过滤的最近 50 条日志。                                                     |
| `--mode status`            | 查询面板的实际写入状态。                                                         |
| `--mode read-only`         | 切换到只读，保留写入设置供下次使用。                                             |
| `--mode writable`          | 使用首次指定或已保存的设置启用写入。                                             |
| `--config PATH`            | 启动 YAML。裸机为宿主机路径，Docker 为容器内路径。                               |
| `--kernel-home PATH`       | 内核资源目录，与 `--config` 使用同一侧的路径。                                   |
| `--probe-file PATH`        | 宿主机上运行态探针 JSON 的绝对路径。                                             |
| `--kernel-binary PATH`     | 裸机 Mihomo 可执行文件绝对路径，与 `--container` 二选一。                        |
| `--container NAME`         | 正在运行的 Mihomo 容器名或 ID。                                                  |
| `--docker-entrypoint PATH` | 可选的容器内 Mihomo 二进制绝对路径。                                             |
| `--state-dir PATH`         | 可选的现有私有状态目录。默认 `/var/lib/metacubexd`。迁移时指定原目录可保留历史。 |
| `--help`                   | 显示命令行帮助。                                                                 |

更多用法见[网页操作、探针和 Go 参数](docs/server-webui.zh-CN.md)，生产操作见[试运行和回退手册](docs/server-pilot.zh-CN.md)。

[MIT 许可证](LICENSE)
