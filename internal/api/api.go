package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"warden/internal/auth"
	"warden/internal/config"
	"warden/internal/filemgr"
	"warden/internal/fleet"
	"warden/internal/metrics"
	"warden/internal/middleware"
	"warden/internal/monitor"
	"warden/internal/platform/docker"
	"warden/internal/store"
	"warden/internal/system"
	"warden/web"
)

type Deps struct {
	Cfg     *config.Config
	Store   *store.Store
	Auth    *auth.Manager
	Metrics *metrics.Collector
	Docker  *docker.Service
	Files   *filemgr.Service
	FW      *system.Firewall
	Monitor *monitor.Service
	Notify  *monitor.Notifier
	Fleet   *fleet.Registry
	Version string
}

// NewRouter 装配全部路由。
// 带 ?host= 的请求会被转发到目标主机 Agent，本机请求走原有逻辑。
func NewRouter(d *Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.Recovery(), requestLogger(), gin.Recovery())
	r.MaxMultipartMemory = 8 << 20

	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": d.Version})
	})

	pub := r.Group("/api/auth")
	{
		pub.GET("/status", d.authStatus)
		pub.POST("/bootstrap", d.bootstrap)
		pub.POST("/login", middleware.LoginRateLimit(), d.login)
	}

	// 控制端自身的接口：账号、主机台账、监控、审计、告警配置
	api := r.Group("/api")
	api.Use(middleware.Auth(d.Auth), middleware.Audit(d.Store))
	{
		api.GET("/auth/me", d.me)
		api.POST("/auth/logout", d.logout)
		api.POST("/auth/password", d.changePassword)
		api.POST("/auth/totp/setup", d.totpSetup)
		api.POST("/auth/totp/enable", d.totpEnable)
		api.POST("/auth/totp/disable", d.totpDisable)

		api.GET("/hosts", d.listHosts)
		api.POST("/hosts", middleware.RequireRole(auth.RoleOperator), d.createHost)
		api.PUT("/hosts/:id", middleware.RequireRole(auth.RoleOperator), d.updateHost)
		api.DELETE("/hosts/:id", middleware.RequireRole(auth.RoleAdmin), d.deleteHost)
		api.POST("/hosts/:id/test", d.testHost)
		api.POST("/hosts/:id/token", middleware.RequireRole(auth.RoleAdmin), d.rotateHostToken)

		api.GET("/ssh/hosts", d.listSSHHosts)
		api.POST("/ssh/hosts", middleware.RequireRole(auth.RoleOperator), d.saveSSHHost)
		api.POST("/ssh/hosts/:id/test", middleware.RequireRole(auth.RoleOperator), d.testSSHHost)
		api.DELETE("/ssh/hosts/:id", middleware.RequireRole(auth.RoleAdmin), d.deleteSSHHost)

		api.GET("/monitors", d.listMonitors)
		api.POST("/monitors", middleware.RequireRole(auth.RoleOperator), d.saveMonitor)
		api.PUT("/monitors/:id", middleware.RequireRole(auth.RoleOperator), d.saveMonitor)
		api.DELETE("/monitors/:id", middleware.RequireRole(auth.RoleOperator), d.deleteMonitor)
		api.POST("/monitors/:id/check", d.checkMonitor)
		api.POST("/monitors/:id/toggle", middleware.RequireRole(auth.RoleOperator), d.toggleMonitor)
		api.GET("/monitors/:id/history", d.monitorHistory)
		api.GET("/monitors/summary", d.monitorSummary)
		api.GET("/settings/alert", d.getAlertSetting)
		api.PUT("/settings/alert", middleware.RequireRole(auth.RoleAdmin), d.saveAlertSetting)

		api.GET("/audit", d.listAudit)

		// Nacos 连接配置由控制端保存（按主机隔离），密码加密落库
		api.GET("/nacos/conn", d.getNacosConn)
		api.PUT("/nacos/conn", middleware.RequireRole(auth.RoleOperator), d.saveNacosConn)

		// Redis 连接配置同理：控制端落库，Agent 只通过请求头拿参数
		api.GET("/redis/conn", d.getRedisConn)
		api.PUT("/redis/conn", middleware.RequireRole(auth.RoleOperator), d.saveRedisConn)

		// MySQL 连接配置同样由控制端保存（按主机隔离），密码加密落库
		api.GET("/mysql/conn", d.getMySQLConn)
		api.PUT("/mysql/conn", middleware.RequireRole(auth.RoleOperator), d.saveMySQLConn)
	}

	// 配置内容格式校验：纯本地解析，不访问 Nacos，是只读操作，
	// 所以单独成组（不挂审计中间件），避免每次输入都往审计日志里写一条。
	va := r.Group("/api")
	va.Use(middleware.Auth(d.Auth))
	{
		va.POST("/nacos/config/validate", d.nacosValidateConfig)
	}

	// 文件收藏夹：个人偏好数据，只落控制端本地库。
	// 不挂 HostProxy（不能被转发到 Agent），也不挂审计（避免频繁收藏刷日志）。
	fav := r.Group("/api")
	fav.Use(middleware.Auth(d.Auth))
	{
		fav.GET("/files/favorites", d.listFileFavorites)
		fav.POST("/files/favorites", d.addFileFavorite)
		fav.DELETE("/files/favorites/:id", d.deleteFileFavorite)
	}

	// 主机相关接口：按 ?host= 分发到本机或远程 Agent
	proxyMW := middleware.HostProxy(d.Fleet)
	ha := r.Group("/api")
	ha.Use(middleware.Auth(d.Auth), middleware.Audit(d.Store), proxyMW)
	{
		ha.GET("/dashboard/overview", d.overview)
		ha.GET("/dashboard/processes", d.processes)

		ha.GET("/docker/status", d.dockerStatus)
		ha.GET("/containers", d.listContainers)
		ha.GET("/containers/stats", d.containerStatsBatch)
		ha.GET("/containers/:id", d.inspectContainer)
		ha.POST("/containers", middleware.RequireRole(auth.RoleOperator), d.createContainer)
		ha.POST("/containers/:id/:action", middleware.RequireRole(auth.RoleOperator), d.containerAction)
		ha.DELETE("/containers/:id", middleware.RequireRole(auth.RoleOperator), d.removeContainer)
		ha.GET("/containers/:id/stats", d.containerStats)
		ha.GET("/containers/:id/logs", d.containerLogs)

		ha.GET("/compose/stacks", d.listComposeStacks)
		ha.GET("/compose/file", d.getComposeFile)
		ha.PUT("/compose/file", middleware.RequireRole(auth.RoleOperator), d.saveComposeFile)
		ha.POST("/compose/up", middleware.RequireRole(auth.RoleOperator), d.composeUp)

		ha.GET("/images", d.listImages)
		ha.POST("/images/pull", middleware.RequireRole(auth.RoleOperator), d.pullImage)
		ha.DELETE("/images/:id", middleware.RequireRole(auth.RoleOperator), d.removeImage)
		ha.GET("/volumes", d.listVolumes)
		ha.GET("/networks", d.listNetworks)

		ha.GET("/files", d.listFiles)
		ha.GET("/files/download", d.downloadFile)
		ha.GET("/files/content", d.readFile)
		ha.PUT("/files/content", middleware.RequireRole(auth.RoleOperator), d.writeFile)
		ha.POST("/files/mkdir", middleware.RequireRole(auth.RoleOperator), d.mkdir)
		ha.POST("/files/touch", middleware.RequireRole(auth.RoleOperator), d.touch)
		ha.POST("/files/rename", middleware.RequireRole(auth.RoleOperator), d.rename)
		ha.POST("/files/copy", middleware.RequireRole(auth.RoleOperator), d.copyPath)
		ha.POST("/files/chmod", middleware.RequireRole(auth.RoleOperator), d.chmod)
		ha.POST("/files/compress", middleware.RequireRole(auth.RoleOperator), d.compress)
		ha.POST("/files/extract", middleware.RequireRole(auth.RoleOperator), d.extract)
		ha.POST("/files/upload", middleware.RequireRole(auth.RoleOperator), d.upload)
		ha.DELETE("/files", middleware.RequireRole(auth.RoleOperator), d.deleteFile)

		ha.GET("/system/info", d.systemInfo)
		ha.GET("/system/processes", d.processList)
		ha.POST("/system/processes/kill", middleware.RequireRole(auth.RoleOperator), d.processKill)
		ha.GET("/system/firewall", d.firewallStatus)
		ha.POST("/system/firewall/rules", middleware.RequireRole(auth.RoleOperator), d.addFirewallRule)
		ha.DELETE("/system/firewall/rules/:id", middleware.RequireRole(auth.RoleOperator), d.deleteFirewallRule)
		ha.POST("/system/firewall/toggle", middleware.RequireRole(auth.RoleOperator), d.toggleFirewall)
		ha.POST("/system/firewall/confirm", middleware.RequireRole(auth.RoleOperator), d.confirmFirewall)
	}

	// Nacos：连接参数按主机注入请求头，本机直接访问、远程交给 Agent 访问
	na := r.Group("/api")
	na.Use(middleware.Auth(d.Auth), middleware.Audit(d.Store),
		middleware.NacosConn(d.Fleet, d.Store, d.Cfg.SecretKey()), proxyMW)
	{
		na.GET("/nacos/status", d.nacosStatus)
		na.GET("/nacos/namespaces", d.nacosNamespaces)
		na.GET("/nacos/services", d.nacosServices)
		na.GET("/nacos/instances", d.nacosInstances)
		na.GET("/nacos/configs", d.nacosConfigs)
		na.GET("/nacos/config", d.nacosConfigDetail)
		na.GET("/nacos/diagnose", d.nacosDiagnose)
		na.PUT("/nacos/config", middleware.RequireRole(auth.RoleOperator), d.nacosPublishConfig)
		na.DELETE("/nacos/config", middleware.RequireRole(auth.RoleOperator), d.nacosDeleteConfig)
	}

	// Redis：连接参数同样按主机注入请求头，本机直连、远程交给 Agent 连接
	ra := r.Group("/api")
	ra.Use(middleware.Auth(d.Auth), middleware.Audit(d.Store),
		middleware.RedisConn(d.Fleet, d.Store, d.Cfg.SecretKey()), proxyMW)
	{
		ra.GET("/redis/status", d.redisStatus)
		ra.GET("/redis/info", d.redisInfo)
		ra.GET("/redis/dbs", d.redisDBs)
		ra.GET("/redis/keys", d.redisKeys)
		ra.GET("/redis/key", d.redisKeyDetail)
		ra.PUT("/redis/key", middleware.RequireRole(auth.RoleOperator), d.redisSetKey)
		ra.DELETE("/redis/key", middleware.RequireRole(auth.RoleOperator), d.redisDeleteKey)
		ra.POST("/redis/key/ttl", middleware.RequireRole(auth.RoleOperator), d.redisExpireKey)
		ra.POST("/redis/command", middleware.RequireRole(auth.RoleOperator), d.redisCommand)
	}

	// MySQL：连接参数同样按主机注入请求头，本机直连、远程交给 Agent 连接
	ma := r.Group("/api")
	ma.Use(middleware.Auth(d.Auth), middleware.Audit(d.Store),
		middleware.MySQLConn(d.Fleet, d.Store, d.Cfg.SecretKey()), proxyMW)
	{
		ma.GET("/mysql/overview", d.mysqlOverview)
		ma.GET("/mysql/databases", d.mysqlDatabases)
		ma.GET("/mysql/tables", d.mysqlTables)
		ma.GET("/mysql/table", d.mysqlTable)
		ma.GET("/mysql/rows", d.mysqlRows)
		ma.GET("/mysql/processes", d.mysqlSessions)
		ma.GET("/mysql/variables", d.mysqlVariables)
		ma.GET("/mysql/slow", d.mysqlSlow)
		ma.POST("/mysql/query", middleware.RequireRole(auth.RoleOperator), d.mysqlQuery)
		ma.POST("/mysql/processes/kill", middleware.RequireRole(auth.RoleOperator), d.mysqlKillSession)
		// 导入导出、建表改表都会改数据或把数据带出面板，统一要求 operator
		ma.POST("/mysql/export", middleware.RequireRole(auth.RoleOperator), d.mysqlExport)
		ma.POST("/mysql/import", middleware.RequireRole(auth.RoleOperator), d.mysqlImport)
		ma.POST("/mysql/table/create", middleware.RequireRole(auth.RoleOperator), d.mysqlCreateTable)
		ma.POST("/mysql/table/alter", middleware.RequireRole(auth.RoleOperator), d.mysqlAlterTable)
		// 单个字段修改与单行数据修改同样属于写操作
		ma.POST("/mysql/column/alter", middleware.RequireRole(auth.RoleOperator), d.mysqlAlterColumn)
		ma.POST("/mysql/row/update", middleware.RequireRole(auth.RoleOperator), d.mysqlUpdateRow)
		ma.POST("/mysql/slow/explain", middleware.RequireRole(auth.RoleOperator), d.mysqlSlowExplain)
	}

	// WebSocket：浏览器无法自定义 Header，token 通过 query 传递
	ws := r.Group("/ws")
	ws.Use(middleware.Auth(d.Auth))
	{
		ws.GET("/metrics", proxyMW, d.wsMetrics)
		ws.GET("/containers/:id/logs", proxyMW, d.wsContainerLogs)
		ws.GET("/containers/:id/exec", middleware.RequireRole(auth.RoleOperator), proxyMW, d.wsContainerExec)
		ws.GET("/terminal/local", middleware.RequireRole(auth.RoleOperator), proxyMW, d.wsLocalTerminal)
		// SSH 跳板会话由控制端发起，不跟随主机切换
		ws.GET("/terminal/ssh", middleware.RequireRole(auth.RoleOperator), d.wsSSHTerminal)
	}

	if dist, err := web.Dist(); err == nil {
		r.NoRoute(spaHandler(dist))
	}
	return r
}

func spaHandler(dist fs.FS) gin.HandlerFunc {
	fileServer := http.FileServer(http.FS(dist))
	return func(c *gin.Context) {
		p := strings.TrimPrefix(c.Request.URL.Path, "/")
		if strings.HasPrefix(p, "api/") || strings.HasPrefix(p, "ws/") {
			// 附上真实的方法与路径，便于排查「路由未注册 / 反代前缀不一致」类问题
			c.JSON(http.StatusNotFound, gin.H{
				"code": 404,
				"msg":  "接口不存在",
				"path": c.Request.Method + " " + c.Request.URL.Path,
			})
			return
		}
		if p != "" {
			if st, err := fs.Stat(dist, p); err == nil && !st.IsDir() {
				c.Header("Cache-Control", "public, max-age=31536000, immutable")
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			c.String(http.StatusNotFound, "前端资源未构建，请执行 make web")
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	}
}
