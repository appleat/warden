#!/usr/bin/env bash
# Warden 一键安装脚本（在 Linux 服务器上执行）
set -euo pipefail

BIN_SRC="${1:-./warden-linux-amd64}"
DIR=/opt/warden
SERVICE=/etc/systemd/system/warden.service

if [[ ! -f "$BIN_SRC" ]]; then
  echo "找不到二进制：$BIN_SRC"
  echo "用法：sudo ./install.sh <二进制路径>"
  exit 1
fi

echo "==> 创建目录 $DIR"
mkdir -p "$DIR/data"

echo "==> 安装二进制"
install -m 0755 "$BIN_SRC" "$DIR/warden"

if [[ ! -f "$DIR/config.yaml" ]]; then
  echo "==> 生成默认配置"
  cat > "$DIR/config.yaml" <<'YAML'
listen: ":8080"
data_dir: "./data"
file_root: "/"
token_hours: 24
log_level: "info"
docker_host: ""
shell: ""
cert_file: ""
key_file: ""
YAML
fi

echo "==> 安装 systemd 服务"
install -m 0644 "$(dirname "$0")/warden.service" "$SERVICE"
systemctl daemon-reload
systemctl enable warden
systemctl restart warden

sleep 1
systemctl --no-pager --lines=5 status warden || true

cat <<TIP

安装完成：http://<服务器IP>:8080
首次访问会引导创建管理员账号。

常用命令：
  systemctl status warden
  journalctl -u warden -f
  systemctl restart warden

安全建议：只监听 127.0.0.1 并用 Nginx 反代 + HTTPS，或直接配置 cert_file/key_file。
TIP
