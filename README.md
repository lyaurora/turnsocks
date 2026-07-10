# turnsocks

`turnsocks` 是一款基于 TURN 协议的轻量级流量转发工具。程序在本地提供 SOCKS5 代理入口，将客户端的出站流量通过配置的 TURN 节点进行中转。

## 功能

- **TURN 转发**：支持通过 TURN TCP relay 转发 TCP 流量；优先通过 UDP 连接转发 UDP 流量，不可用时自动回退为基于 TURN-over-TCP 的 UDP 转发。
- **SOCKS5 入口**：提供本地 SOCKS5 TCP/UDP 接入层（默认 `127.0.0.1:1080`），兼容浏览器及各类代理客户端。
- **内置 DoH**：所有域名目标均通过标准的 DNS over HTTPS 解析为 IPv4 并缓存。
- **节点管理**：支持配置多个 TURN 节点。
- **Web 面板**：内置控制面板，支持节点测速、切换，及服务配置的热加载。

## 安装

使用安装脚本下载最新 Release 二进制文件，并创建 systemd 服务：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

默认安装及配置路径：

```text
安装目录：/opt/turnsocks
SOCKS5：  127.0.0.1:1080
面板：    127.0.0.1:10808
配置：    /opt/turnsocks/config.env
```

*注：首次安装会写入默认节点 `127.0.0.1:3478` 用于初始化服务。安装完成后，请登录面板添加真实的 TURN 节点并切换启用。*

**自定义节点安装：**

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env TURN_SERVERS="user:password@turn.example.com:3478,backup.example.com:3478" sh
```

**自定义安装目录及面板监听地址：**

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env INSTALL_DIR="$HOME/turnsocks" PANEL_LISTEN=127.0.0.1:10808 sh
```

## 面板

面板默认仅监听本地环回地址。远程管理建议通过 SSH 端口转发安全接入：

```sh
ssh -L 10808:127.0.0.1:10808 user@your-vps
```

随后通过浏览器访问：

```text
http://127.0.0.1:10808
```

**主要功能：**
- **节点管理**：添加、删除、切换 TURN 节点，并保存历史测速结果。
- **测速诊断**：测试节点的 TCP 延迟、UDP 转发可用性、单/多线程带宽。
- **配置修改**：修改 SOCKS5 监听端口、DoH 地址及面板登录账号。
- **访问认证**：首次安装会在 `config.env` 生成随机密码，当 `PANEL_USERNAME` 和 `PANEL_PASSWORD` 均配置时即启用网页登录；留空则禁用。

*注：节点的添加与删除会热加载配置。切换默认节点或修改端口、DoH 地址将触发代理服务重启以应用变更。*
*测速诊断使用 Cloudflare 下载源，用于评估节点基础质量，实际访问速度由 TURN 出口至目标站点的网络状况决定。*


## 配置

配置文件采用标准的 `.env` 环境变量格式：

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
PANEL_USERNAME=admin
PANEL_PASSWORD=your-panel-password
```

`TURN_SERVERS` 支持多个节点，以英文逗号分隔：

```text
无鉴权：host:port
有鉴权：user:password@host:port
```

配置列表中的首个节点为默认出口。在面板中切换节点时，系统会自动将选中节点置顶并重启代理生效。

DoH 采用标准 `application/dns-message` 接口。推荐使用：

```text
Cloudflare: https://cloudflare-dns.com/dns-query
Google:     https://dns.google/dns-query
```

面板在保存 DoH 变更前会执行连通性测试，以防止错误配置导致解析中断。

## 更新

再次执行安装脚本即可覆盖更新：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

脚本会自动拉取 GitHub 最新 Release 的二进制文件，覆盖主程序及面板组件，并重启服务。

以下本地配置和数据将被保留：

```text
config.env
turnsocks.state
turnsocks.tests.json
```

## 常用命令

```sh
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks turnsocks-panel
sudo journalctl -u turnsocks -f
```

## 工作方式

整体链路：

```text
客户端 -> 本机 SOCKS5 -> turnsocks -> TURN 服务器 -> 目标网站
```

技术细节：

- 基础 TURN 流程基于 [RFC 5766](https://tools.ietf.org/html/rfc5766)；UDP 转发通过 `CREATE-PERMISSION`、`SEND/DATA indication` 实现。
- TCP relay 基于 [RFC 6062](https://tools.ietf.org/html/rfc6062)，使用 `CONNECT` 和 `CONNECTION-BIND` 建立中转连接。
- IPv6 relay（[RFC 6156](https://tools.ietf.org/html/rfc6156)）目前尚未实现，仅支持 IPv4 目标地址转发。

## 开发

本项目提供用于开发及构建的源码支持：

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
make check
make release
```

构建依赖于 Go 1.25.12、Node.js 和 npm。`make check` / `make release` 将自动检查并构建 `panel/ui` 的 React 前端，再嵌入二进制文件。普通用户建议直接使用安装脚本部署 Release 版本，无需本地构建。

从源码编译并安装到本地运行目录：

```sh
BUILD_FROM_SOURCE=1 INSTALL_DIR="$HOME/turnsocks" ./install.sh
```

## 限制

- 暂不支持 IPv6 目标地址。
- 暂未处理 SOCKS5 UDP `FRAG` 分片标识，仅接受 `FRAG=0` 的数据包（常见 DNS、QUIC 等协议不受影响）。

## 思路来源

本项目的思路来源于 [ToiCF/CF-Workers-TURN](https://github.com/ToiCF/CF-Workers-TURN)。

## 开源协议

本项目使用 GPL-3.0 License 开源，详见 [LICENSE](LICENSE)。
