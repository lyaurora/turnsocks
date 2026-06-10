# turnsocks

`turnsocks` 用 TURN 服务器作为中转通道，把本机代理流量转发出去。SOCKS5 只是本地入口，方便浏览器、系统代理或其他客户端接入；项目重点是复用 TURN 节点完成代理转发。

项目带一个本地 Web 面板，用来添加、删除、切换 TURN 节点，并重启代理服务。推送到 `main` 后，GitHub Actions 会自动刷新固定的 `latest` Release，生成 Linux amd64 和 Linux arm64 静态二进制。

真实的 `config.env` 已被 Git 忽略。每台 VPS 只保留自己的本地配置，不要提交 TURN 账号、密码或节点地址。

## 部署

```sh
git clone git@github.com:lyaurora/turnsocks.git
cd turnsocks
cp config.example.env config.env
chmod 600 config.env
vi config.env
./install.sh
```

`install.sh` 会从 GitHub `latest` Release 下载当前平台对应的二进制并校验 `SHA256SUMS`。如果下载不到，才会尝试用 Go 从源码构建。

仓库是私有的，新 VPS 下载 Release 需要满足其中一种条件：

- 已安装并登录 `gh`
- 环境变量里有可读仓库的 `GITHUB_TOKEN` 或 `GH_TOKEN`

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

`TURN_SERVERS` 用英文逗号分隔多个节点。每个节点可以写成 `host:port` 或 `user:password@host:port`。第一个节点是默认当前节点，面板切换节点时会把选中的节点移动到第一位并重启代理。

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

面板默认监听 `127.0.0.1:10808`。建议通过 SSH 转发、Nginx 反代或私有隧道访问，不要直接暴露到公网。
