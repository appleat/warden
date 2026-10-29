#!/usr/bin/env bash
# Warden Agent 一键安装脚本（在「被管理」的服务器上执行）
#
# 用法：
#   sudo ./install-agent.sh ./warden-linux-amd64 <令牌> [主机名]
#
# 令牌在控制端「主机管理 → 添加主机」时生成，只显示一次。
set -euo pipefail

BIN_SRC="${1:-./warden-linux-amd64}"
TOKEN="${2:-}"
NAME="${3:-$(hostname)}"
DIR=/opt/warden-agent
SERVICE=/etc/systemd/system/warden-agent.service
PORT="${AGENT_PORT:-8090}"

if [[ -z "$TOKEN" ]]; then
  echo "缺少访问令牌。"
  echo "用法：sudo ./install-agent.sh <二进制路径> <令牌> [主机名]"
  exit 1
fi
if [[ ! -f "$BIN_SRC" ]]; then
  echo "找不到二进制：$BIN_SRC"
  exit 1
fi

echo "==> 安装目录 $DIR"
mkdir -p "$DIR"

echo "==> 安装二进制"
install -m 0755 "$BIN_SRC" "$DIR/warden"

echo "==> 生成 Agent 配置"
cat > "$DIR/config.yaml" <<YAML
mode: agent
agent_listen: ":${PORT}"
agent_token: "${TOKEN}"
agent_name: "${NAME}"
file_root: "/"
log_level: "info"
docker_host: ""
shell: ""
YAML
chmod 600 "$DIR/config.yaml"

echo "==> 安装 systemd 服务"
cat > "$SERVICE" <<UNIT
[Unit]
Description=Warden Agent
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
# 需要管理 Docker / 防火墙，故以 root 运行
User=root
WorkingDirectory=$DIR
ExecStart=$DIR/warden -config $DIR/config.yaml
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable warden-agent
systemctl restart warden-agent

sleep 1
systemctl --no-pager --lines=5 status warden-agent || true

cat <<TIP

Agent 已启动，端口 ${PORT}。

请在控制端点击该主机的「探测」验证连通性。若不通，检查：
  1. 云厂商安全组 / 本机防火墙是否放行 ${PORT}
  2. 控制端到本机的网络是否可达
  3. 令牌是否与控制端登记的一致（不一致可在控制端「重置令牌」）

常用命令：
  systemctl status warden-agent
  journalctl -u warden-agent -f
  systemctl restart warden-agent
TIP
