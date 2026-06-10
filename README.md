# turnsocks

`turnsocks` 是一个本地 SOCKS5 代理，会通过一个或多个 TURN 节点转发流量。项目里还带了一个本地 Web 面板，可以切换 TURN 节点、删除节点、添加节点，并重启代理服务。

推送到 `main` 后，GitHub Actions 会自动刷新一个固定的 `latest` Release，并构建 Linux amd64 和 Linux arm64 静态二进制。普通 VPS 部署时不需要安装 Go。只有你要本地改源码或手动构建时，才需要 Go。

真实的 `config.env` 已经被 Git 忽略。每台 VPS 都应该只保留自己的本地配置，不要把 TURN 账号、密码、节点地址或其他凭据提交到仓库。

## 文件说明

- `main.go`：SOCKS5 代理主程序。
- `panel/main.go`：本地管理面板。
- `config.example.env`：安全的配置模板。
- `install.sh`：安装脚本，会安装程序、写入 systemd 服务并启动。
- `Makefile`：本地构建、检查和生成 Release 资产。
- `.github/workflows/latest-release.yml`：自动构建并刷新 `latest` Release。

## 新 VPS 部署

```sh
git clone git@github.com:lyaurora/turnsocks.git
cd turnsocks
cp config.example.env config.env
chmod 600 config.env
vi config.env
./install.sh
```

`install.sh` 会优先使用本地 `bin/` 里的预编译二进制。如果本地没有，就会从 GitHub `latest` Release 下载当前平台对应的二进制并校验 `SHA256SUMS`。如果下载不到，才会尝试用 Go 从源码构建。

仓库是私有的，新 VPS 从 Release 下载时需要满足其中一种条件：

- 已安装并登录 `gh`
- 环境变量里有可读仓库的 `GITHUB_TOKEN` 或 `GH_TOKEN`

默认情况下，脚本会把当前目录作为安装目录。也可以手动指定安装目录和运行用户：

```sh
INSTALL_DIR=/opt/turn-proxy RUN_USER=turnsocks ./install.sh
```

如果指定的 `RUN_USER` 不存在，脚本会自动创建一个系统用户。指定 `/opt/turn-proxy` 这类目录时，脚本也会自动创建安装目录，并优先复制当前目录里已经填好的 `config.env`。

脚本需要 `systemctl`。如果不是 root 用户运行，还需要 `sudo`。安装时会生成：

- `/etc/systemd/system/turnsocks.service`
- `/etc/systemd/system/turnsocks-panel.service`
- `/etc/sudoers.d/turnsocks-panel`

其中 sudoers 规则只允许面板免密重启 `turnsocks` 服务。

## 配置说明

```env
LISTEN=127.0.0.1:1080
TURN_SERVERS=user:password@turn.example.com:3478,backup.example.com:3478
DOH=https://cloudflare-dns.com/dns-query
```

`TURN_SERVERS` 用英文逗号分隔多个节点。每个节点可以写成：

- `host:port`
- `user:password@host:port`

第一个节点会作为默认当前节点。面板里切换节点时，会把被选中的节点移动到第一位并重启代理。

## 常用命令

```sh
make release
make check
GO=/home/lyaurora/go/bin/go make check
sudo systemctl status turnsocks turnsocks-panel
sudo systemctl restart turnsocks
sudo journalctl -u turnsocks -f
```

已有 VPS 更新时，进入安装目录后拉取最新版本并重新安装即可：

```sh
git pull
./install.sh
```

发布包会自动维护在 GitHub 的 `latest` Release，仓库本身不再提交二进制产物。

面板默认监听 `127.0.0.1:10808`。建议通过 SSH 转发、Nginx 反代或私有隧道访问，不要直接暴露到公网。
