# Warden

> **守护者** —— 一个二进制，看住你所有的 Linux 服务器。

Linux 服务器管理面板。单二进制交付：前端 `go:embed` 进 Go 程序，目标机零依赖。支持单机直管，也支持用一个控制端纳管多台服务器。

```
控制端（Web + 账号 + 主机台账）
   │  按 ?host= 转发 /api 与 /ws
   ├── 本机 ── 直接调用内部服务
   └── 远程主机 ── HTTP/WS ──> Agent（同一份二进制，-mode agent）
```

## 功能

| 模块 | 能力 |
|---|---|
| 认证 | 首次启动引导建号、bcrypt、JWT、三级角色（admin/operator/viewer）、登录失败锁定、TOTP、操作审计 |
| 数据看板 | CPU/负载/内存/Swap/磁盘/网络实时卡片，2s WebSocket 推送，趋势曲线，Top 进程 |
| 容器管理 | 列表/搜索、启停重启暂停、删除、创建、实时 stats、日志流与下载、exec 交互终端、镜像/卷/网络 |
| Nacos 服务 | 连接服务端（支持鉴权与命名空间）、服务列表与健康实例、实例详情、配置查询/发布/删除 |
| Redis 服务 | 连接本机或远程 Redis（密码/ACL/TLS/多库）、运行指标、键浏览（模糊子串检索 / `*` `?` 通配符、按分隔符树形分层）、键内容预览与增删改、过期时间管理、命令台 |
| 文件管理 | 浏览、上传（流式）、下载、在线编辑、重命名/复制/删除、chmod、zip/tar 压缩解压，路径沙箱防穿越 |
| 系统管理 | 防火墙规则 CRUD（ufw/firewalld/iptables 自动探测）、端口放行封禁、危险操作 30s 未确认自动回滚、进程管理（实时采样、多字段排序、结束/强杀）、本地 PTY 终端、SSH 跳板终端 |
| 网站监控 | HTTP/HTTPS/TCP/关键字/证书到期探测，可用率与响应时间曲线，企业微信/钉钉/飞书/邮件告警 |
| 多主机 | 主机台账、Agent 令牌、在线探测、一键切换视角，切换后所有页面跟随目标主机 |

## 快速开始（单机）

```bash
# 1. 构建（需要 Go 1.22+ / Node 18+）
make build          # 产出 bin/warden
make linux          # 交叉编译 linux/amd64 与 linux/arm64

# Windows 上用 build.bat，一键产出全平台二进制到 dist\
build.bat           # windows/linux/darwin × amd64/arm64
build.bat linux     # 只要 Linux

# 2. 部署到服务器
sudo ./deploy/install.sh ./bin/warden-linux-amd64

# 3. 打开浏览器
#    http://<服务器IP>:8080  首次访问会引导创建管理员
```

## 纳管多台服务器

### 1. 控制端添加主机

登录后进入「主机管理 → 添加主机」，填写 Agent 地址（如 `10.0.0.12:8090`）。保存后会**一次性**显示：

- 访问令牌（`warden_...`）
- 启动命令
- 可直接写入的 `config.yaml` 片段

### 2. 目标机部署 Agent

```bash
# 把同一份二进制传到目标机，然后：
sudo ./deploy/install-agent.sh ./warden-linux-amd64 warden_xxxxxxxx 生产环境-01
```

脚本会创建 `/opt/warden-agent`、写入配置并注册 systemd 服务 `warden-agent`。

手工启动等价于：

```bash
sudo ./warden -mode agent -agent-listen :8090 -agent-token warden_xxxxxxxx
```

### 3. 验证

回到「主机管理」，状态列会显示**在线 / 延迟**。点「探测」可立即重试。

之后顶部主机的下拉框切换视角，数据看板、容器、文件、进程、防火墙、终端全部跟随目标主机；网站监控、审计日志、账号设置属于控制端全局配置，不随主机切换。

> Agent 默认监听 `:8090`，需放行控制端到该端口的访问。若通过公网暴露，建议给 Agent 配置 TLS（勾选「TLS」），或只允许控制端 IP 访问。

## 构建与测试

```bash
make web     # 仅构建前端（改了前端必须重新构建，否则 Go 嵌入的是旧产物）
make build   # 前端 + 当前平台二进制
make linux   # 前端 + linux/amd64 + linux/arm64
go test ./...  # 单元测试：路径穿越、zip slip、AES、TOTP、防火墙规则校验、回滚守卫
```

### Windows 一键交叉编译（`build.bat`）

双击或命令行运行均可，产物统一在 `dist\`，文件名形如 `warden-linux-amd64`。
同一份二进制既是控制端也是被管端，靠 `-mode` 区分。

| 命令 | 说明 |
|---|---|
| `build.bat` | 构建全部平台：windows / linux / darwin × amd64 + arm64 |
| `build.bat linux` | 只构建 linux/amd64 与 linux/arm64 |
| `build.bat linux/arm64` | 只构建单个目标，可写 `windows/amd64`、`darwin/arm64` 等 |
| `build.bat -Pack` | 额外打 zip（二进制 + `config.yaml` + `deploy/`） |
| `build.bat -Version 1.2.0` | 注入版本号，`warden -version` 可查看 |
| `build.bat -SkipWeb` | 跳过前端构建，复用现有 `web/dist` |
| `build.bat -Clean` | 构建前清空 `dist\` |
| `build.bat -h` | 查看帮助 |

说明：

- `build.bat` 是入口壳，实际逻辑在同目录的 `build.ps1`。cmd 无法解析 UTF-8 中文批处理，
  所以 `.bat` 保持纯 ASCII，中文提示由 PowerShell 输出（脚本已写 BOM，PS 5.1 可正常读取）。
- 构建固定 `CGO_ENABLED=0`（SQLite 走纯 Go 实现），否则无法交叉编译。
- 交叉编译产物自带前端资源，无需在目标机安装 Node。

## 配置

优先级：命令行参数 > 环境变量（`WARDEN_` 前缀）> `config.yaml` > 默认值。

| 字段 | 环境变量 | 说明 |
|---|---|---|
| `mode` | `WARDEN_MODE` | `server` / `agent` |
| `listen` | `WARDEN_LISTEN` | 控制端监听地址 |
| `data_dir` | `WARDEN_DATA_DIR` | 数据目录（SQLite + 密钥） |
| `file_root` | `WARDEN_FILE_ROOT` | 文件管理根目录，建议收窄到 `/data` |
| `agent_listen` | `WARDEN_AGENT_LISTEN` | Agent 监听地址，默认 `:8090` |
| `agent_token` | `WARDEN_AGENT_TOKEN` | Agent 令牌，必须与控制端登记一致 |
| `cert_file` / `key_file` | `WARDEN_CERT_FILE` / `WARDEN_KEY_FILE` | 启用 HTTPS |

## 结构

```
cmd/server/main.go            入口：server / agent 双模式分流
internal/config               配置加载
internal/store                SQLite 模型（用户、审计、监控、SSH 主机、主机台账、Nacos/Redis 连接）
internal/auth                 密码、JWT、RBAC、TOTP
internal/middleware           JWT、审计、角色、登录限流、主机代理、Nacos/Redis 连接注入
internal/fleet                主机注册表：代理构造、令牌加解密、在线探测
internal/metrics              gopsutil 采样与历史
internal/platform/docker      Docker Engine API 封装
internal/nacos                Nacos OpenAPI 客户端（服务/实例/配置）
internal/rediscli             Redis 客户端（纯标准库 RESP2，支持 TLS/ACL/多库、Pipeline、INFO 解析）
internal/filemgr              文件沙箱与压缩解压
internal/system               防火墙三驱动、进程采样与结束、PTY、SSH
internal/monitor              健康检查调度与告警
internal/api                  HTTP/WS 接口与 Agent 路由
web/                          Vue3 + Element Plus + ECharts + xterm
deploy/                       systemd unit 与安装脚本
```

## 安全说明
- 本程序仅供测试使用，安全问题本程序作者概不负责。
- 所有写接口都经 RBAC 校验，并落审计日志；远程主机的操作会在审计详情里标注主机名。
- 文件访问被限制在 `file_root` 内，路径穿越与 zip slip 均有测试覆盖。
- 主机令牌用 AES 加密后落库，接口不会回显；重置后旧令牌立即失效。
- Nacos 密码同样 AES 加密落库；连接参数按主机注入请求头后转发，Agent 端不落盘，
  且中间件会先清空同名请求头，防止客户端伪造地址让 Agent 连接任意目标。
- Redis 连接参数走同一套机制（AES 落库 + 请求头注入 + 清空伪造头）；
  命令台会直接拦下 SHUTDOWN / FLUSHALL / FLUSHDB / CONFIG SET / ACL 变更等破坏性命令。
- 防火墙规则、端口、来源地址全部走正则白名单，禁止拼接 shell 字符串。
- 进程结束接口只接受运营者及以上角色，并硬性拒绝 PID ≤ 1 与 Warden 自身进程；
  界面上这两类进程的操作按钮也会置灰，避免误操作把自己锁在服务器外面。
- 默认监听 `:8080` 无 TLS，公网部署请配 `cert_file`/`key_file` 或置于 Nginx 之后。
