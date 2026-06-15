# turnsocks

`turnsocks` 是一个利用 TURN 服务器转发代理流量的小工具。它会在本机启动一个 SOCKS5 入口，浏览器、系统代理或其他客户端连到这个本地 SOCKS5 后，实际出站流量会通过配置的 TURN 节点中转出去。

SOCKS5 只是本地接入层，项目主体是 TURN 转发：

- 本地监听：默认启动 `127.0.0.1:1080` SOCKS5 服务。
- DoH 解析：收到域名目标时，先通过配置的 DoH 服务器解析为 IPv4，并在本地短暂缓存；同一个域名的并发查询会合并，避免瞬时重复请求。
- TCP 代理：SOCKS5 `CONNECT` 会解析目标地址，然后通过 TURN TCP allocation、`CONNECT` 和 `CONNECTION-BIND` 建立中转连接。
- UDP 代理：SOCKS5 `UDP ASSOCIATE` 会创建本地 UDP 端口，解析目标地址后通过 TURN allocation、`CREATE-PERMISSION` 和 `SEND/DATA` indication 转发 UDP 数据。
- TURN 传输：UDP 代理会优先用 UDP 连接 TURN 服务器；如果该节点的 UDP 不通或不支持，会自动改用 TCP 连接 TURN 服务器继续承载 UDP 转发。这里的 UDP/TCP 指 `turnsocks -> TURN 服务器` 这一段，不是最终访问目标的流量类型。
- 节点池：支持配置多个 TURN 节点，运行时优先使用最近成功的节点；失败节点和 UDP 传输失败会短暂冷却，后续请求自动尝试其他节点。
- 面板：本地 Web 面板可添加、删除、切换 TURN 节点，并查看默认节点、运行中节点和服务状态。

推送到 `main` 后，GitHub Actions 会自动刷新固定的 `latest` Release，生成 Linux amd64 和 Linux arm64 静态二进制。

真实的 `config.env` 已被 Git 忽略。每台 VPS 只保留自己的本地配置，不要提交 TURN 账号、密码或节点地址。

## 部署

```sh
git clone https://github.com/lyaurora/turnsocks.git
cd turnsocks
cp config.example.env config.env
chmod 600 config.env
vi config.env
./install.sh
```

`install.sh` 会从 GitHub `latest` Release 下载当前平台对应的二进制并校验 `SHA256SUMS`。如果下载不到，才会尝试用 Go 从源码构建。

仓库公开后，新 VPS 可以直接 clone 并下载 `latest` Release，不需要额外配置 GitHub Token。

默认会安装到当前目录。也可以指定安装目录和运行用户：

```sh
INSTALL_DIR=/opt/turn-proxy RUN_USER=turnsocks ./install.sh
```

脚本会自动创建缺失的安装目录和运行用户，并写入：

- `/etc/systemd/system/turnsocks.service`
- `/etc/systemd/system/turnsocks-panel.service`
- `/etc/sudoers.d/turnsocks-panel`

## 配置

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
```

`TURN_SERVERS` 用英文逗号分隔多个节点。每个节点可以写成 `host:port` 或 `user:password@host:port`。第一个节点是默认节点；实际运行中如果某个备用节点最近成功转发，程序会优先继续使用它。面板切换节点时会把选中的节点移动到第一位并重启代理。

## 常用命令

```sh
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks
sudo journalctl -u turnsocks -f
```

已有 VPS 更新：

```sh
git pull
./install.sh
```

本地开发检查：

```sh
make check
make release
```

## 限制

- 暂不支持 IPv6 目标地址，目标需要是 IPv4，或域名解析到 IPv4。
- 暂不处理 SOCKS5 协议自己的 UDP 分片。这里指 SOCKS5 UDP 请求头里的 `FRAG` 字段，不是系统网络层的 IP 分片；当前只接受普通 UDP 包，也就是 `FRAG=0`。常见 DNS、QUIC 和应用 UDP 流量通常不受影响；如果某个客户端发送 `FRAG>0` 的 SOCKS5 UDP 分片包，当前会拒绝处理。

面板默认监听 `127.0.0.1:10808`。建议通过 SSH 转发、Nginx 反代或私有隧道访问，不要直接暴露到公网。
