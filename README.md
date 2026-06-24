# turnsocks

`turnsocks` 是一个利用 TURN 服务器转发代理流量的小工具。它会在本机启动 SOCKS5 入口，客户端连接本地 SOCKS5 后，实际出站流量通过配置的 TURN 节点中转出去。

项目重点是 TURN 转发，SOCKS5 只是本地接入层。

## 工作方式

- 本地默认监听 `127.0.0.1:1080`，提供 SOCKS5 TCP/UDP 入口。
- TCP 流量会通过 TURN 节点建立中转连接。
- UDP 流量优先用 UDP 连接 TURN 服务器；这段不通时，会用 TCP 连接 TURN 服务器继续转发 UDP。
- 域名目标会通过 DoH 解析为 IPv4，并做短暂缓存。
- 支持多个 TURN 节点，面板可添加、删除、切换、测试节点，并保存最近一次测试结果。

实现细节：

- TCP 流量通过 TURN TCP allocation、`CONNECT`、`CONNECTION-BIND` 建立中转连接。
- UDP 流量通过 TURN allocation、`CREATE-PERMISSION`、`SEND/DATA` 转发。

## 安装

普通 VPS 推荐一行安装，不需要 Go，也不需要克隆源码：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

默认安装到 `/opt/turn-proxy`，服务会用 `sudo` 前的用户运行：

- SOCKS5：`127.0.0.1:1080`
- 面板：`127.0.0.1:10808`
- 配置：`/opt/turn-proxy/config.env`

首次安装会写入占位节点 `127.0.0.1:3478`，只用于让服务和面板先启动。安装完成后进入面板，添加真实 TURN 节点并切换到该节点。

如果要安装时直接写入节点：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env TURN_SERVERS="user:password@turn.example.com:3478,backup.example.com:3478" sh
```

如需指定安装目录或面板监听：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo env INSTALL_DIR="$HOME/turn-proxy" PANEL_LISTEN=127.0.0.1:10808 sh
```

## 面板

面板默认只监听本机。远程访问建议使用 SSH 转发：

```sh
ssh -L 10808:127.0.0.1:10808 user@your-vps
```

然后在浏览器打开 `http://127.0.0.1:10808`。

面板支持浏览器弹窗登录。首次安装创建 `config.env` 时会生成：

```env
PANEL_USERNAME=admin
PANEL_PASSWORD=随机密码
```

两个值都有内容时启用认证；留空则不启用。

面板支持：

- 添加、删除、切换 TURN 节点。
- 查看默认节点和当前运行节点。
- 测试节点 TCP 连接延迟、UDP 转发、单线程带宽、多线程带宽。
- 保存每个节点最近一次测试结果，再次测试会覆盖旧结果。

## 更新

重新运行安装命令即可：

```sh
curl -fsSL https://raw.githubusercontent.com/lyaurora/turnsocks/main/install.sh | sudo sh
```

脚本会下载 GitHub `latest` Release 二进制，覆盖本地 `turnsocks` 和 `turnsocks-panel`，然后重启服务。`config.env`、`turnsocks.state`、`turnsocks.tests.json` 会保留在本机。

## 配置

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
PANEL_USERNAME=admin
PANEL_PASSWORD=your-panel-password
```

`TURN_SERVERS` 用英文逗号分隔。节点格式为 `host:port` 或 `user:password@host:port`。第一个节点是默认节点；面板切换节点时会把选中的节点移动到第一位并重启代理。

## 常用命令

```sh
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks turnsocks-panel
sudo journalctl -u turnsocks -f
```

## 开发

源码只用于开发修改和自动构建。普通 VPS 使用 Release 二进制即可。

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
make check
make release
```

如果要从当前源码安装到本机运行目录：

```sh
BUILD_FROM_SOURCE=1 INSTALL_DIR="$HOME/turn-proxy" ./install.sh
```

推送到 `main` 后，GitHub Actions 会刷新固定的 `latest` Release，并生成 Linux amd64、Linux arm64 静态二进制。

## 限制

- 暂不支持 IPv6 目标地址。
- 暂不处理 SOCKS5 UDP 请求头里的 `FRAG` 分片字段，只接受 `FRAG=0` 的普通 UDP 包。常见 DNS、QUIC 和应用 UDP 流量通常不受影响。
- 面板建议通过 SSH 转发、Nginx 反代或私有隧道访问，不要直接暴露到公网。
