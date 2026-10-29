# Warden 项目长期记忆

> 项目名 **Warden（看守）**，v1.2 由 GoServerMgr 全量更名而来。
> 命名取舍：Shepherd / Bastion / NodePilot / Vantage 都因撞名被否；Warden「看守」语义最直白。
> 更名覆盖：module `warden`、二进制 `warden`、环境变量前缀 `WARDEN_`、systemd `warden.service` /
> `warden-agent.service`、安装目录 `/opt/warden`、令牌前缀 `warden_`、cookie 与 localStorage `warden_*`、
> SQLite 文件 `data/warden.db`。**不要再出现 gsm / GoServerMgr 字样。**

## 项目定位
Go + Gin 实现的 Linux 服务器管理面板，单二进制部署，前端 `go:embed`。
v1.1 起支持多主机：同一份二进制既可当控制端（`-mode server`）也可当被管端（`-mode agent`）。
模块：登录鉴权、数据看板、Docker 容器、文件管理、系统管理（防火墙/终端/SSH）、网站健康监控、审计、主机纳管。

## 多主机架构（改动时务必遵守）
- 控制端不直连远程机器，只把 `/api`、`/ws` 按 `?host=<id>` 反向代理到 Agent 的 `/agent/...`
- `internal/fleet.Registry` 构造 `httputil.ReverseProxy`：加 `/agent` 前缀、注入 `X-Agent-Token`、
  剥离 `host`/`token` 参数、**改写 `Origin`**（不改 Agent 的 CheckOrigin 同源校验）
- 路由分两组：控制端组（auth/hosts/ssh/monitors/audit/alert）与主机组（dashboard/docker/files/system，挂 `HostProxy`）
- Agent 端 `NewAgentRouter` 直接复用 server 的 handler；`Deps.Store` 在 agent 模式下为 nil，用到的 handler 必须判空
- 新增「按主机隔离」的接口：注册到 `ha` 组，并把前缀加进前端 `api.ts` 的 `HOST_SCOPED`
- 全局能力（网站监控、审计、告警、账号）不分主机，不要接进代理组

## 技术栈（不要随意更换）
- Go 1.26 + Gin + GORM + SQLite（`glebarez/sqlite`，**纯 Go，禁 CGO**，否则无法交叉编译）
- Docker：官方 `docker/docker` Client SDK（走 Engine API，不 exec docker 命令行）
- 指标：gopsutil/v4；终端：x/crypto/ssh + creack/pty + gorilla/websocket
- 前端：Vue3 + TypeScript + Vite + Element Plus + ECharts + @xterm/xterm

## 构建
- 改了前端必须 `cd web && npx vite build`，否则 Go 嵌入的是旧产物
- Windows 上无 make，交叉编译直接（**必须带 `-s -w`**，否则多出约 13 MB 调试符号）：
  `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/warden-linux-amd64 ./cmd/server`
  → 约 31.4 MB（不带 `-s -w` 是 44.3 MB）。`Makefile` 与 `build.ps1` 本来就有 `-s -w`，只有手敲命令容易漏
- 本机联调起双实例：`./bin/warden.exe -mode agent -agent-listen :8090 -agent-token <token>`
  加 `WARDEN_DATA_DIR=./tmpdata ./bin/warden.exe`（**不要** `rm -rf data`，工作区安全策略会拦截）

## 编码约定
- 接口响应统一 `{code,msg,data}`；错误用 `httpx.Fail(c, status, msg)`
- 角色：admin / operator / viewer；写操作挂 `middleware.RequireRole(auth.RoleOperator)`
- 平台相关代码用 build tag 拆分（`pty_unix.go` / `pty_windows.go`），运行时统一接口 `system.Term`
- WebSocket 鉴权走 query `?token=`；控制类提示用 `\x00NOTICE:` 前缀
- 前端不引入 axios，统一用 `web/src/api.ts` 的 fetch 封装；手写 URL（下载/上传）要拼 `hostQuery()`
- 复制到剪贴板统一用 `web/src/utils.ts` 的 `copyText()`，**别直接用 `navigator.clipboard`**
  ——它只在 HTTPS / localhost 下存在，内网 `http://IP:端口` 访问时是 undefined；`copyText` 已带
  textarea + `execCommand` 兜底
- 主机令牌（Host.Token）AES 加密落库、json 标签为 `-`，只在创建/重置时明文返回一次

## 已知约束
- 本地 PTY 终端仅 Linux/macOS 可用；Windows 会返回提示浮层
- 防火墙自动回滚目前只实现了 ufw 的完整快照恢复
- 端口/来源地址等全部走正则白名单校验，禁止拼 shell 字符串
- Agent 无 TLS 时监听 :8090 为明文，公网暴露需配 cert 或限制来源 IP

## 更名注意（v1.2）
- 前端 `api.ts` 里有一段 `migrateLegacyKeys()`，把旧的 `gsm_token` / `gsm_host` 搬到 `warden_*`，
  保证升级不踢人；这段是唯一的旧名残留，属于有意保留
- 数据库文件从 `goservermgr.db` 改为 `warden.db`，**没有做自动迁移**（旧库不会被读取）

## Nacos 模块（v1.3）
- 导航：运维组「服务管理」为二级菜单，子项 `/containers`（原容器管理）与 `/nacos`
- 客户端在 `internal/nacos`（纯标准库），只做 OpenAPI，别引第三方 SDK
- 连接配置存 `store.NacosConf`（按 host_id 唯一，密码 AES 加密），UI 在页面「连接设置」
- 连接参数经 `middleware.NacosConn` 注入 `X-Warden-Nacos-*` 请求头（base64），
  本机 handler 与 Agent handler 同一份代码，**Agent 不落库**；中间件会先清空同名头防伪造
- `namespace` 空串即 public，后端不做默认值兜底；前端用 `PUBLIC_NS='__public__'` 哨兵绕开 el-select 空值
- **多版本兼容三条硬规则**（1.x/2.x/3.x 差异踩过坑，改 `internal/nacos` 必读）：
  1. 配置接口 `/v1/cs/configs` 只认 `tenant`，命名接口才认 `namespaceId` →
     统一走 `setConfigScope()` 两个参数都发（1.x 缺 tenant 会静默退回 public）
  2. **结构体字段别写死 bool/int，一律 `any` + `toInt/toFloat/truthy/toStr` 转换**。
     实测该服务端把 `triggerFlag` 返回成字符串 `"false"`，`json.Unmarshal` 类型不匹配会
     让**整个数组**解析失败（接口有数据、页面全空、无任何报错）；健康数键名还是
     `healthyInstanceCount` 而非 `healthyCount`。服务列表和实例列表都已改成 any。
  2b. 列表解析走 `parseServiceList()` 宽容模式：先 `unwrapData()` 剥 `{code,message,data}` 信封
     （**不剥会一条都匹配不到，表现为接口有返回但页面空白**），再按
     `serviceList`/`pageItems`/`services`/`doms` 找键，元素支持对象数组与字符串数组，
     都找不到就扫描第一个数组字段；「形状不认识 → 回落下一接口」，不能只看 HTTP 状态码，
     **且返回 0 条时也要回落**（1.x 常见「200 + 形状正常 + 空列表」）
  2b. 空的过滤参数（`groupName=""`、`selector=""`）**不要发送**，部分版本会当字面量过滤条件查不到东西
  2c. 参数键名以 Nacos 控制台实际请求为准，跟命名服务直觉不一样：
      catalog 分组过滤是 **`groupNameParam`**（不是 `groupName`，发错了服务端静默忽略、过滤无效），
      关键字是 `serviceNameParam`，另带 `hasIpCount=true` 才有实例数
  3. 多端点依次尝试（如 namespaces 的 v1 → v3）时，**鉴权/权限错误要直接 break**，
     不能被后续端点的 404 覆盖成「接口不存在」
- 排障入口：`GET /api/nacos/diagnose` + 页面「诊断」按钮（`Client.Diagnose` 返回每个接口的状态码与返回片段）

## Redis 模块（v1.4）
- 导航：服务管理二级菜单新增 `Redis 服务`，路由 `/redis`，页面 `web/src/views/Redis.vue`
- 客户端在 `internal/rediscli`（**纯标准库 RESP2**，不引第三方驱动，保证 CGO_ENABLED=0 交叉编译）
  - 每次请求 Open 一条连接、用完即关：避开连接池里 AUTH/SELECT 状态不一致
  - `Conn.Do` 返回 string/int64/[]any/nil，服务端错误用 `*ReplyError` 返回且连接仍可用
  - `Conn.Pipeline` 用于键列表批量补 TYPE/TTL/MEMORY USAGE，几百次往返压成一次
  - `ListKeys` 已改为**全量 SCAN 到游标归零**再返回（最多 MaxKeysReturn=5000 条，MaxKeysScan=500000 个键封顶，超出置 `truncated`）；
    类型过滤仍在客户端做（SCAN 只支持 key 模式）
  - **模糊查找坑（v1.4.1 修过）**：早期实现在第一轮 SCAN 拿到任一匹配键就 `break`，导致散落的匹配键漏查
    （「有的能查到有的查不到」）。根因是 SCAN 命中键在键空间里是散落的，绝不能「一命中即停」。
    另外 `normalizePattern`：空串→`*`；含 glob 元字符(`*?[]`)→原样；否则当作子串前后补 `*` 实现模糊。
  - 前端 `Redis.vue` 键浏览支持「列表 / 树形」两种视图：树形按**可自定义分隔符**（默认 `:`）把键名分层，
    纯前端 `buildTree()` 从返回的扁平 `items` 构造嵌套树，文件夹在前、字典序排；分隔符为空时退化为单节点
  - 预览都有上限（string 4KB / 集合 100 条），避免误点百万元素的 hash 把面板拖死
  - RESP 细节：`SSCAN` 返回**扁平成员**，`HSCAN` 才是字段值成对；`scanPairs` 只给 HSCAN 用
- 连接配置 `store.RedisConf`（host_id 唯一，密码 AES 加密），中间件 `middleware.RedisConn`
  注入 `X-Warden-Redis-{Addr,User,Password,DB,TLS,Insecure}`（用户名密码 base64，头部只能 ASCII），
  先清空同名头防伪造；Agent 不落库。**完全照抄 NacosConn 的套路**，新增同类模块可照此复制
- 库切换不需要改连接配置：所有数据接口都支持 `?db=N` 覆盖（本机 handler 与 Agent 共用）
- 写入接口的 db 必须走 **query**（`?db=`），不要放在 JSON body 里——`redisClient()` 只读 query
- 命令台后端拦死 SHUTDOWN/FLUSHALL/FLUSHDB/SWAPDB/CONFIG SET/ACL 变更等（`redisBlockedCommands`）
- 测试：`internal/rediscli/client_test.go` 带假 RESP 服务端；`internal/api/redis_test.go`
  用 httptest 走完「中间件注入 → handler」链路，并验证按主机隔离与伪造头清除。
  注意 httptest.ResponseRecorder 不支持 CloseNotifier，测试路由里**不要挂 HostProxy**

## 进程管理（v1.5）
- 导航：系统管理 → 进程管理，路由 `/system/processes`，页面 `web/src/views/Processes.vue`
- 接口挂在**主机组 ha**（走 HostProxy，本机直采 / 远程由 Agent 采），Agent 端同步注册：
  `GET /api/system/processes?sort=&order=&limit=&q=`、`POST /api/system/processes/kill`
  （前端无需改 HOST_SCOPED，`/api/system/` 前缀已覆盖）
- 采样实现在 `internal/system/process.go`，跨平台；排序/过滤都在**截断前**做
- **坑一：不能用 gopsutil 的 `Process.CPUPercent()`**。Linux 上它返回「自进程启动以来的平均值」，
  对长期运行的进程毫无意义。必须自己两轮 `Times()` 求差。
- **坑二：CPU 换算窗口必须按每个进程实测的 Δt，不能用固定常量**。Windows 上整轮遍历可能耗时数秒，
  用常量会把 CPU 放大几十倍（实测 1869%，真实约 40%）。现在每个 seed 记 `at time.Time`，第二轮紧循环重读。
- **坑三：Windows 的 `Process.NumThreads()` 每次遍历全系统线程表（约 13ms/进程）**，
  548 个进程要 7.4 秒，是整个接口唯一的性能瓶颈。已改为 `proc_threads_windows.go` 里
  一次 `CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD)` 批量统计（`proc_threads_unix.go` 返回 nil 走逐进程）。
  优化后整接口 0.4~0.5 秒（含 300ms 采样窗口）。
- 重字段（Username / Cmdline / Exe）只对最终 top N 补取，不要在全量遍历里调
- 安全：`KillProcess` 硬性拒绝 PID ≤ 1 与 `os.Getpid()`；`force=true` 走 SIGKILL，否则 SIGTERM；
  EPERM 单独提示「权限不足」。前端对这两类进程置灰按钮
- 结束进程落审计（成功/失败都记），需要 operator 及以上角色
