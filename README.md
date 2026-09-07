# turnsocks

`turnsocks` 在本机提供 SOCKS5 入口，通过 TURN 节点转发 TCP / UDP 流量。

```text
客户端 -> SOCKS5（turnsocks）-> TURN 节点 -> 目标服务
```

## 功能

- TCP 转发使用 [RFC 6062](https://www.rfc-editor.org/rfc/rfc6062)，需要 TURN 节点支持 TCP relay。
- UDP 转发使用 [RFC 8656](https://www.rfc-editor.org/rfc/rfc8656)，优先通过 UDP 连接 TURN 节点，不可用时尝试通过 TCP 承载；节点到目标端仍发送 UDP 数据报。
- 域名目标通过 [DoH](https://www.rfc-editor.org/rfc/rfc8484) 解析为 IPv4，并按 DNS TTL 缓存。
- 支持多节点故障切换，提供 Web 面板管理节点、配置和检测结果。

## 安装

预编译安装支持使用 systemd 的 Linux，提供 amd64 和 arm64 两种架构。以下命令下载最新 Release，并创建、启动 `turnsocks` 与 `turnsocks-panel` 服务：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

默认路径：

```text
安装目录  /opt/turnsocks
SOCKS5    127.0.0.1:1080
面板      127.0.0.1:10808
配置文件  /opt/turnsocks/config.env
```

首次安装时，默认用户名为 `admin`，随机密码保存在 `/opt/turnsocks/config.env` 的 `PANEL_PASSWORD` 字段中。TURN 节点列表初始为空，服务会等待配置；登录面板添加首个节点后即可使用。

也可以在首次安装时指定节点：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env TURN_SERVERS="user:password@turn.example.com:3478,backup.example.com:3478" sh
```

已有的 `config.env` 会保留；更新时通过环境变量传入节点不会覆盖已有列表。

## 面板

浏览器需要 Chrome / Edge 111+、Firefox 128+ 或 Safari 16.4+。

面板默认仅监听本机。在自己的电脑上执行 SSH 端口转发，并保持连接：

```sh
ssh -N -L 10808:127.0.0.1:10808 user@your-vps
```

然后在浏览器访问 [http://127.0.0.1:10808](http://127.0.0.1:10808)，使用上述凭据登录。

面板支持添加、删除、切换和备注节点，也可修改 SOCKS5 监听地址、DoH 与面板登录设置。

### 检查与测速

| 操作 | 测试内容 | 流量 |
| --- | --- | --- |
| 检查 | 到 TURN 节点的 TCP 建连延迟，以及实际 TCP / UDP 转发 | 小型 HTTP 204 请求和 UDP DNS 查询 |
| 测速 | 单线程和四线程下载带宽 | Cloudflare 下载源，每个节点约 112 MiB |

TCP 延迟测量的是 turnsocks 所在机器到 TURN 节点的建连时间；带宽结果用于比较节点在该下载源上的表现。

两类结果分别保存，在同一组指标中展示：检查只更新延迟与连通性，测速只更新带宽。只做过检查时，两列带宽显示“未测速”；已有测速结果会保留。下载中断但已收到数据时，会保留实测速度并标记“未完成”，同时显示错误原因。

检测使用独立的临时代理，批量检测逐个执行。“停止检测”会取消当前请求和本轮剩余节点，保留已完成的结果。测速会占用节点带宽。

“最近事件”记录失败阶段、原因和节点切换；可通过“检查”确认节点当前状态。

### 配置何时生效

| 操作 | 生效方式 |
| --- | --- |
| 添加、删除节点 | 节点池自动加载新列表，不重启代理 |
| 修改备注、面板登录设置 | 保存后生效，不重启代理 |
| 手动切换节点、修改监听地址或 DoH | 自动重启代理，会中断已有连接 |

保存新的 DoH 地址前，面板会先验证其能否正常解析域名。

## 配置

`config.env` 使用以下字段：

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
PANEL_USERNAME=admin
PANEL_PASSWORD=your-panel-password
```

| 字段 | 说明 |
| --- | --- |
| `LISTEN` | SOCKS5 TCP 监听地址 |
| `TURN_SERVERS` | 以英文逗号分隔的 TURN 节点列表 |
| `TURN_SERVER_NOTES` | 面板维护的节点备注，无需手动编辑 |
| `DOH` | `application/dns-message` DoH 接口 |
| `PANEL_USERNAME` | 面板用户名，首次安装默认为 `admin` |
| `PANEL_PASSWORD` | 面板密码，首次安装默认随机生成 |

`PANEL_USERNAME` 和 `PANEL_PASSWORD` 都非空时启用面板登录，否则关闭。这些凭据只用于面板，SOCKS5 入口不提供用户名密码认证。

TURN 节点格式：

```text
无鉴权  host:port
有鉴权  user:password@host:port
```

节点之间用英文逗号分隔，端口必填。列表中的首个节点为默认出口；运行期间的当前节点记录在 `turnsocks.state`。通过面板切换节点会将所选节点移至列表首位。

通过网页修改监听地址或 DoH 时，面板会自动重启代理。直接编辑 `config.env` 文件中的 `LISTEN` 或 `DOH` 后，需要执行 `sudo systemctl restart turnsocks` 使其生效。

每次 SOCKS5 建连的总时限默认为 20 秒，覆盖握手、DNS、TURN 分配和重试，可在代理启动命令中通过 `-timeout` 调整。连接建立后，TCP / UDP 转发不受该建连时限限制。

## 更新与运维

重新执行安装命令会更新二进制并重启两个服务，期间代理连接会中断。使用自定义安装参数（如 `INSTALL_DIR`、`PANEL_LISTEN`、`RUN_USER`）时，更新也需沿用这些参数。

安装目录中的配置、运行状态和检测结果会保留：

| 文件 | 内容 |
| --- | --- |
| `config.env` | 节点、备注、监听地址、DoH 和面板登录设置 |
| `turnsocks.state` | 当前节点与最近事件 |
| `turnsocks.tests.json` | 带宽测速结果 |
| `turnsocks.checks.json` | 轻量检查结果 |

常用命令：

```sh
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks turnsocks-panel
sudo journalctl -u turnsocks -f
sudo journalctl -u turnsocks-panel -f
```

## 开发

构建需要 [go.mod](go.mod) 指定版本的 Go、Node.js 24（含 npm）和 `make`。

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
make check
make release
```

`make check` 会构建前端、运行前后端检查，并联网执行 npm 安全审计（包含开发依赖）和 Go 漏洞扫描；npm 高危及以上漏洞或 Go 可达漏洞会阻止检查通过和 CI 发布。`make release` 会在 `dist/` 生成 Linux amd64 / arm64 二进制和 `SHA256SUMS`。前端静态文件嵌入面板二进制，部署后由面板直接提供页面。

从源码安装：

```sh
BUILD_FROM_SOURCE=1 INSTALL_DIR=/opt/turnsocks ./install.sh
```

## 限制

- 仅支持 IPv4 目标地址，尚未实现 [RFC 6156](https://www.rfc-editor.org/rfc/rfc6156) IPv6 relay。
- SOCKS5 UDP 仅接受 `FRAG=0`，不支持 UDP 分片。

## 思路来源

[ToiCF/CF-Workers-TURN](https://github.com/ToiCF/CF-Workers-TURN)

## 开源许可

[GPL-3.0](LICENSE)
