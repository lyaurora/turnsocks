#!/usr/bin/env sh
set -eu

APP_NAME=turnsocks
SCRIPT_FROM_STDIN=0
case "$0" in
  sh|-sh|bash|-bash|dash|-dash)
    SCRIPT_FROM_STDIN=1
    SCRIPT_DIR=$(pwd)
    ;;
  *)
    SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
    ;;
esac
SOURCE_CHECKOUT=0
if [ "$SCRIPT_FROM_STDIN" = "0" ] && [ -f "$SCRIPT_DIR/go.mod" ] && [ -f "$SCRIPT_DIR/main.go" ]; then
  SOURCE_CHECKOUT=1
fi
if [ -z "${INSTALL_DIR:-}" ]; then
  if [ "$SOURCE_CHECKOUT" = "1" ]; then
    INSTALL_DIR=$SCRIPT_DIR
  else
    INSTALL_DIR=/opt/turn-proxy
  fi
fi
if [ -z "${RUN_USER:-}" ]; then
  RUN_USER=${SUDO_USER:-$(id -un)}
fi
PANEL_LISTEN=${PANEL_LISTEN:-127.0.0.1:10808}
CONFIG_FILE=${CONFIG_FILE:-$INSTALL_DIR/config.env}
SYSTEMCTL=$(command -v systemctl || true)
GO_CMD=${GO_CMD:-go}
NPM_CMD=${NPM_CMD:-npm}
if [ -z "${SOURCE_CONFIG:-}" ] && [ "$SOURCE_CHECKOUT" = "1" ]; then
  SOURCE_CONFIG=$SCRIPT_DIR/config.env
fi
RELEASE_REPO=${RELEASE_REPO:-lyaurora/turnsocks}
RELEASE_TAG=${RELEASE_TAG:-latest}
BUILD_FROM_SOURCE=${BUILD_FROM_SOURCE:-0}

SUDO=
if [ "$(id -u)" -ne 0 ]; then
  SUDO=${SUDO:-sudo}
fi

if [ "$(id -u)" -ne 0 ] && ! command -v sudo >/dev/null 2>&1; then
  echo "Missing required command: sudo" >&2
  exit 1
fi

if [ -z "$SYSTEMCTL" ]; then
  echo "Missing required command: systemctl" >&2
  exit 1
fi

abs_path() {
  case "$1" in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s\n' "$(pwd)/$1" ;;
  esac
}

run_root() {
  $SUDO "$@"
}

INSTALL_DIR=$(abs_path "$INSTALL_DIR")
CONFIG_FILE=$(abs_path "$CONFIG_FILE")
if [ -n "${SOURCE_CONFIG:-}" ]; then
  SOURCE_CONFIG=$(abs_path "$SOURCE_CONFIG")
fi

target_platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
  esac
  printf '%s-%s\n' "$os" "$arch"
}

ensure_run_user() {
  if id "$RUN_USER" >/dev/null 2>&1; then
    return
  fi
  if ! command -v useradd >/dev/null 2>&1; then
    echo "User $RUN_USER does not exist and useradd is unavailable." >&2
    exit 1
  fi
  run_root useradd --system --no-create-home --home-dir "$INSTALL_DIR" --shell /usr/sbin/nologin "$RUN_USER"
}

ensure_install_dir() {
  if [ -d "$INSTALL_DIR" ]; then
    return
  fi
  if mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    return
  fi
  run_root mkdir -p "$INSTALL_DIR"
}

set_runtime_owner() {
  run_root chown "$RUN_USER" "$1"
}

write_config_values() {
  listen_addr=$1
  turn_servers=$2
  doh_url=$3
  panel_username=$4
  panel_password=$5
  tmp_config=$(mktemp)
  cat > "$tmp_config" <<EOF
LISTEN=$listen_addr
TURN_SERVERS=$turn_servers
DOH=$doh_url
PANEL_USERNAME=$panel_username
PANEL_PASSWORD=$panel_password
EOF
  if install -m 0600 "$tmp_config" "$CONFIG_FILE" 2>/dev/null; then
    :
  else
    run_root install -m 0600 "$tmp_config" "$CONFIG_FILE"
  fi
  rm -f "$tmp_config"
  set_runtime_owner "$CONFIG_FILE"
}

generate_panel_password() {
  if [ -n "${PANEL_PASSWORD:-}" ]; then
    printf '%s\n' "$PANEL_PASSWORD"
    return
  fi
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 12
    return
  fi
  if [ -r /dev/urandom ] && command -v od >/dev/null 2>&1; then
    od -An -N12 -tx1 /dev/urandom | tr -d ' \n'
    printf '\n'
    return
  fi
  date +%s%N
}

create_default_config() {
  listen_addr=${LISTEN:-127.0.0.1:1080}
  doh_url=${DOH:-https://cloudflare-dns.com/dns-query}
  turn_servers=${TURN_SERVERS:-127.0.0.1:3478}
  panel_username=${PANEL_USERNAME:-admin}
  panel_password=$(generate_panel_password)

  write_config_values "$listen_addr" "$turn_servers" "$doh_url" "$panel_username" "$panel_password"
  echo "Created $CONFIG_FILE."
}

install_config() {
  if [ -f "$CONFIG_FILE" ]; then
    chmod 600 "$CONFIG_FILE" 2>/dev/null || run_root chmod 600 "$CONFIG_FILE"
    set_runtime_owner "$CONFIG_FILE"
    return 0
  fi

  if [ -n "${SOURCE_CONFIG:-}" ] && [ "$SOURCE_CONFIG" != "$CONFIG_FILE" ] && [ -f "$SOURCE_CONFIG" ]; then
    if install -m 0600 "$SOURCE_CONFIG" "$CONFIG_FILE" 2>/dev/null; then
      :
    else
      run_root install -m 0600 "$SOURCE_CONFIG" "$CONFIG_FILE"
    fi
  else
    create_default_config
  fi
  if [ -f "$CONFIG_FILE" ]; then
    :
  else
    echo "Failed to create $CONFIG_FILE." >&2
    exit 1
  fi
  set_runtime_owner "$CONFIG_FILE"
}

install_binary() {
  src=$1
  dst=$2
  tmp="$dst.tmp.$$"
  if install -m 0755 "$src" "$tmp" 2>/dev/null; then
    mv -f "$tmp" "$dst" 2>/dev/null || run_root mv -f "$tmp" "$dst"
  else
    run_root install -m 0755 "$src" "$tmp"
    run_root mv -f "$tmp" "$dst"
  fi
}

verify_checksum_entry() {
  checksum_file=$1
  rel=$2
  file=$3

  if [ ! -f "$checksum_file" ]; then
    return
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    echo "Missing required command for checksum verification: sha256sum" >&2
    exit 1
  fi

  expected=$(awk -v path="$rel" '$2 == path { print $1; found = 1 } END { if (!found) exit 1 }' "$checksum_file") || {
    echo "Checksum missing for $rel in $checksum_file" >&2
    exit 1
  }
  actual=$(sha256sum "$file" | awk '{ print $1 }')
  if [ "$actual" != "$expected" ]; then
    echo "Checksum mismatch for $rel" >&2
    exit 1
  fi
}

download_asset() {
  asset=$1
  dest=$2

  if command -v gh >/dev/null 2>&1 && gh auth status -h github.com >/dev/null 2>&1; then
    gh release download "$RELEASE_TAG" --repo "$RELEASE_REPO" --pattern "$asset" --dir "$dest" --clobber >/dev/null || return 1
    return 0
  fi

  if ! command -v curl >/dev/null 2>&1; then
    return 1
  fi

  token=${GITHUB_TOKEN:-${GH_TOKEN:-}}
  url="https://github.com/$RELEASE_REPO/releases/download/$RELEASE_TAG/$asset"
  if [ -n "$token" ]; then
    curl -fsSL -H "Authorization: Bearer $token" -H "Accept: application/octet-stream" "$url" -o "$dest/$asset"
  else
    curl -fsSL "$url" -o "$dest/$asset"
  fi
}

download_release_binaries() {
  dest=$1
  proxy_asset="turnsocks-$TARGET"
  panel_asset="turnsocks-panel-$TARGET"

  mkdir -p "$dest"
  download_asset "$proxy_asset" "$dest" || return 1
  download_asset "$panel_asset" "$dest" || return 1
  download_asset "SHA256SUMS" "$dest" || return 1

  verify_checksum_entry "$dest/SHA256SUMS" "$proxy_asset" "$dest/$proxy_asset"
  verify_checksum_entry "$dest/SHA256SUMS" "$panel_asset" "$dest/$panel_asset"
  chmod 755 "$dest/$proxy_asset" "$dest/$panel_asset"
}

ensure_run_user
ensure_install_dir
set_runtime_owner "$INSTALL_DIR"
install_config

if grep -Eq 'turn\.example\.com|user:password|CHANGE_ME|127\.0\.0\.1:3478' "$CONFIG_FILE"; then
  echo "$CONFIG_FILE contains placeholder TURN_SERVERS. Add a real TURN node in the panel after installation." >&2
fi

cd "$SCRIPT_DIR"
TARGET=${TARGET:-$(target_platform)}
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

if [ "$BUILD_FROM_SOURCE" = "1" ]; then
  if [ "$SOURCE_CHECKOUT" != "1" ]; then
    echo "BUILD_FROM_SOURCE=1 must be run from a source checkout." >&2
    exit 1
  fi
  if ! command -v "$GO_CMD" >/dev/null 2>&1; then
    if [ -x "/home/$RUN_USER/go/bin/go" ]; then
      GO_CMD="/home/$RUN_USER/go/bin/go"
    else
      echo "BUILD_FROM_SOURCE=1 requires Go, but Go is not installed." >&2
      exit 1
    fi
  fi
  if ! command -v "$NPM_CMD" >/dev/null 2>&1; then
    echo "BUILD_FROM_SOURCE=1 requires Node.js/npm to build the panel UI." >&2
    exit 1
  fi
  if [ ! -d "panel/ui/node_modules" ]; then
    "$NPM_CMD" --prefix panel/ui ci
  fi
  "$NPM_CMD" --prefix panel/ui run build
  CGO_ENABLED=0 "$GO_CMD" build -trimpath -ldflags "-s -w" -o "$tmp_dir/turnsocks" .
  CGO_ENABLED=0 "$GO_CMD" build -trimpath -ldflags "-s -w" -o "$tmp_dir/turnsocks-panel" ./panel
  installed_from="local source checkout"
  install_binary "$tmp_dir/turnsocks" "$INSTALL_DIR/turnsocks"
  install_binary "$tmp_dir/turnsocks-panel" "$INSTALL_DIR/turnsocks-panel"
elif download_release_binaries "$tmp_dir/release"; then
  installed_from="GitHub Release $RELEASE_TAG ($TARGET)"
  install_binary "$tmp_dir/release/turnsocks-$TARGET" "$INSTALL_DIR/turnsocks"
  install_binary "$tmp_dir/release/turnsocks-panel-$TARGET" "$INSTALL_DIR/turnsocks-panel"
else
  echo "No prebuilt binaries found for $TARGET in $RELEASE_REPO@$RELEASE_TAG." >&2
  echo "For development from a source checkout, rerun with BUILD_FROM_SOURCE=1." >&2
  exit 1
fi
run_root chmod 755 "$INSTALL_DIR/turnsocks" "$INSTALL_DIR/turnsocks-panel"
set_runtime_owner "$CONFIG_FILE"
run_root chmod 600 "$CONFIG_FILE"

tmp_proxy="$tmp_dir/turnsocks.service"
tmp_panel="$tmp_dir/turnsocks-panel.service"
tmp_sudoers="$tmp_dir/turnsocks-panel.sudoers"

cat > "$tmp_proxy" <<EOF
[Unit]
Description=TURN SOCKS5 Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$CONFIG_FILE
ExecStart=$INSTALL_DIR/turnsocks -config $CONFIG_FILE
Restart=on-failure
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

cat > "$tmp_panel" <<EOF
[Unit]
Description=TURN SOCKS5 Proxy Panel
After=network.target turnsocks.service
Wants=turnsocks.service

[Service]
Type=simple
User=$RUN_USER
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/turnsocks-panel -listen $PANEL_LISTEN -config $CONFIG_FILE
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat > "$tmp_sudoers" <<EOF
$RUN_USER ALL=(root) NOPASSWD: $SYSTEMCTL restart turnsocks
EOF

if command -v visudo >/dev/null 2>&1; then
  run_root visudo -cf "$tmp_sudoers" >/dev/null
fi

run_root install -m 0644 "$tmp_proxy" /etc/systemd/system/turnsocks.service
run_root install -m 0644 "$tmp_panel" /etc/systemd/system/turnsocks-panel.service
run_root install -m 0440 "$tmp_sudoers" /etc/sudoers.d/turnsocks-panel
run_root systemctl daemon-reload
run_root systemctl enable turnsocks.service turnsocks-panel.service
run_root systemctl restart turnsocks.service turnsocks-panel.service

echo "Installed $APP_NAME."
echo "Binaries: $installed_from"
echo "Proxy: $CONFIG_FILE"
echo "Panel: http://$PANEL_LISTEN"
