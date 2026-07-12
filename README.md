# turnsocks

`turnsocks` 在本地提供 SOCKS5 TCP/UDP 入口，并通过 TURN 服务器转发出站流量。

```text
客户端 -> SOCKS5 -> turnsocks -> TURN 服务器 -> 目标服务
```

## 功能

- TCP 流量通过 TURN TCP relay 转发。
- UDP 流量优先通过 UDP 连接 TURN 服务器；该连接不可用时，改用 TURN-over-TCP 承载 UDP 转发。
- 域名目标通过 DoH 解析为 IPv4，并按 DNS TTL 缓存。
- 支持多节点故障切换，并提供 Web 面板管理配置和测速结果。

## 安装

安装最新 Release，并创建 `turnsocks` 与 `turnsocks-panel` systemd 服务：

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

首次安装会生成空的 TURN 节点列表和随机面板密码。服务会保持运行并等待配置；登录面板添加首个 TURN 节点后即可使用。

安装时指定节点：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env TURN_SERVERS="user:password@turn.example.com:3478,backup.example.com:3478" sh
```

## 面板

面板默认仅监听本地环回地址。远程访问可使用 SSH 端口转发：

```sh
ssh -L 10808:127.0.0.1:10808 user@your-vps
```

浏览器访问 `http://127.0.0.1:10808`。

面板用于添加、删除、切换和测试 TURN 节点，也可修改 SOCKS5 监听地址、DoH 与面板登录凭据。测速结果会保存到本地。

添加或删除节点时，节点池会热加载配置。切换节点以及修改监听地址或 DoH 时，代理服务会重启。测速使用 Cloudflare 下载源，结果用于比较节点基础质量，不代表所有目标站点的速度。

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
| `DOH` | `application/dns-message` DoH 接口 |
| `PANEL_USERNAME` | 面板用户名 |
| `PANEL_PASSWORD` | 面板密码 |

同时设置 `PANEL_USERNAME` 和 `PANEL_PASSWORD` 时启用面板登录；留空时禁用。

TURN 节点格式：

```text
无鉴权  host:port
有鉴权  user:password@host:port
```

列表中的首个节点为默认出口。运行期间发生故障切换时，当前节点记录在 `turnsocks.state`；通过面板切换节点会将所选节点移至列表首位。

面板会在保存新的 DoH 地址前检查其连通性。

## 更新与运维

重新执行安装命令可更新二进制并重启服务。`config.env`、`turnsocks.state` 和 `turnsocks.tests.json` 会保留。

常用命令：

```sh
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks turnsocks-panel
sudo journalctl -u turnsocks -f
```

## 技术标准

- TURN `ALLOCATE`、`REFRESH`、`CREATE-PERMISSION` 和 `SEND/DATA` 基于 [RFC 8656](https://www.rfc-editor.org/rfc/rfc8656)。
- TCP relay 基于 [RFC 6062](https://www.rfc-editor.org/rfc/rfc6062)，通过 `CONNECT` 和 `CONNECTION-BIND` 建立数据连接。
- 域名解析使用 [RFC 8484](https://www.rfc-editor.org/rfc/rfc8484) 定义的 DNS over HTTPS 线格式。
- turnsocks 与 TURN 服务器之间的 UDP 传输不可用时，UDP allocation 可通过 TCP 传输承载；TURN 服务器到目标端仍转发 UDP 数据报。

## 开发

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
make check
make release
```

构建环境由 `go.mod` 和 `panel/ui/package.json` 定义。构建过程会先生成 React 前端，再将静态文件嵌入面板二进制。

从源码安装：

```sh
BUILD_FROM_SOURCE=1 INSTALL_DIR="$HOME/turnsocks" ./install.sh
```

## 限制

- 仅支持 IPv4 目标地址，尚未实现 [RFC 6156](https://www.rfc-editor.org/rfc/rfc6156) IPv6 relay。
- SOCKS5 UDP 仅接受 `FRAG=0`，不支持 UDP 分片。

## 思路来源

[ToiCF/CF-Workers-TURN](https://github.com/ToiCF/CF-Workers-TURN)

## 开源许可

[GPL-3.0](LICENSE)
