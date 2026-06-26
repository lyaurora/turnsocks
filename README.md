# turnsocks

`turnsocks` 是一个利用 TURN 服务器转发代理流量的小工具。它会在本机启动 SOCKS5 入口，客户端连接本地 SOCKS5 后，实际出站流量通过配置的 TURN 节点中转出去。

项目重点是 **TURN 转发代理**。SOCKS5 只是本地接入层，方便浏览器、系统代理、sing-box 等客户端接入。

## 功能

- 本地 SOCKS5 TCP/UDP 入口，默认监听 `127.0.0.1:1080`。
- TCP 流量通过 TURN TCP relay 转发。
- UDP 流量优先走 `turnsocks -> TURN 服务器` 的 UDP 连接；如果这段 UDP 不可用，会用 TCP 连接 TURN 服务器承载 UDP 转发。
- 域名目标通过标准 DoH 解析为 IPv4，并做短暂缓存。
- 支持多个 TURN 节点，面板可添加、删除、切换、测试节点。
- 面板保存节点最近一次测试结果，方便之后判断节点质量。

## 安装

使用安装脚本下载 Release 二进制并创建 systemd 服务：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

默认安装内容：

```text
安装目录：/opt/turnsocks
SOCKS5：  127.0.0.1:1080
面板：    127.0.0.1:10808
配置：    /opt/turnsocks/config.env
```

首次安装会写入占位节点 `127.0.0.1:3478`，只用于让服务和面板先启动。安装完成后进入面板，添加真实 TURN 节点并切换到该节点。

如需在安装时写入节点：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env TURN_SERVERS="user:password@turn.example.com:3478,backup.example.com:3478" sh
```

如需指定安装目录或面板监听地址：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env INSTALL_DIR="$HOME/turnsocks" PANEL_LISTEN=127.0.0.1:10808 sh
```

## 面板

面板默认只监听本机。远程访问建议使用 SSH 转发：

```sh
ssh -L 10808:127.0.0.1:10808 user@your-vps
```

然后在浏览器打开：

```text
http://127.0.0.1:10808
```

面板支持：

- 添加、删除、切换 TURN 节点。
- 查看默认节点和当前运行节点。
- 测试 TCP 延迟、UDP 转发、单线程带宽、多线程带宽。
- 修改 SOCKS5 监听、DoH、面板账号密码。
- 保存每个节点最近一次测试结果，再次测试会覆盖旧结果。

添加和删除节点只写入配置，`turnsocks` 会热加载节点池，不会重启当前代理。切换节点、修改 SOCKS5 或 DoH 会重启 `turnsocks` 让配置生效。

节点测速使用 Cloudflare 下载源。它更适合作为节点基础质量测试；特殊网站的实际速度仍然取决于该 TURN 出口到目标站的线路。

## 登录

首次安装创建 `config.env` 时会生成：

```env
PANEL_USERNAME=admin
PANEL_PASSWORD=随机密码
```

两个值都有内容时启用网页登录；留空则不启用。面板里也可以修改账号密码，保存后自动生效。

## 配置

配置文件采用环境变量文件格式：

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
PANEL_USERNAME=admin
PANEL_PASSWORD=your-panel-password
```

`TURN_SERVERS` 用英文逗号分隔。节点格式：

```text
无鉴权：host:port
有鉴权：user:password@host:port
```

第一个节点是默认节点；面板切换节点时会把选中的节点移动到第一位并重启代理。

DoH 使用标准 `application/dns-message` 接口。推荐：

```text
Cloudflare: https://cloudflare-dns.com/dns-query
Google:     https://dns.google/dns-query
```

如果面板里还保留旧的 `https://dns.google/resolve`，会自动规范为 `https://dns.google/dns-query`。面板保存新的 DoH 前会先做一次解析测试，失败时不会写入配置。

## 更新

重新运行安装脚本即可：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

脚本会下载 GitHub `latest` Release 二进制，覆盖本地 `turnsocks` 和 `turnsocks-panel`，然后重启服务。

这些本机文件会保留：

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
客户端
  -> 本机 SOCKS5
  -> turnsocks
  -> TURN 服务器
  -> 目标网站
```

技术细节：

- 基础 TURN 流程基于 RFC 5766，使用 allocation、permission 和 indication 转发 UDP。
- TCP relay 基于 RFC 6062，使用 TCP allocation、`CONNECT`、`CONNECTION-BIND` 建立中转连接。
- UDP 转发使用 `CREATE-PERMISSION`、`SEND` indication 和 `DATA` indication。
- IPv6 relay 对应 RFC 6156；项目目前只实现 IPv4 目标地址。

## 开发

源码用于开发修改和自动构建：

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
make check
make release
```

开发构建需要 Go、Node.js 和 npm。`make check` / `make release` 会先构建 `panel/ui` 里的 React 前端，再把构建产物嵌入 `turnsocks-panel`。安装脚本使用 Release 二进制，不需要本地构建。

如果要从当前源码安装到本机运行目录：

```sh
BUILD_FROM_SOURCE=1 INSTALL_DIR="$HOME/turnsocks" ./install.sh
```

推送到 `main` 后，GitHub Actions 会刷新固定的 `latest` Release，并生成 Linux amd64、Linux arm64 静态二进制。

## 限制

- 暂不支持 IPv6 目标地址。
- 暂不处理 SOCKS5 UDP 请求头里的 `FRAG` 分片字段，只接受 `FRAG=0` 的普通 UDP 包。常见 DNS、QUIC 和应用 UDP 流量通常不受影响。
- 面板建议通过 SSH 转发、Nginx 反代或私有隧道访问，不要直接暴露到公网。
