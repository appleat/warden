package api

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/httpx"
	"warden/internal/middleware"
)

// agentAuth 校验控制端下发的静态令牌。未配置令牌时拒绝全部请求（失败关闭）。
func agentAuth(token string) gin.HandlerFunc {
	sum := []byte(token)
	return func(c *gin.Context) {
		if token == "" {
			httpx.Fail(c, http.StatusServiceUnavailable,
				"Agent 未配置访问令牌，请使用 -agent-token 或配置文件 agent_token 指定")
			return
		}
		got := c.GetHeader("X-Agent-Token")
		if got == "" {
			got = c.Query("agent_token")
		}
		if subtle.ConstantTimeCompare([]byte(got), sum) != 1 {
			httpx.Unauthorized(c, "Agent 令牌无效")
			return
		}
		c.Next()
	}
}

// agentInfo 供控制端探测主机概况。
func (d *Deps) agentInfo(c *gin.Context) {
	snap, _ := d.Metrics.Snapshot()

	info := fleet.AgentInfo{
		Version:  d.Version,
		Hostname: snap.Host.Hostname,
		Platform: snap.Host.Platform,
		Kernel:   snap.Host.Kernel,
		Arch:     snap.Host.Arch,
		CPUCores: snap.Host.CPUCores,
		TotalMem: snap.Host.TotalMem,
		Uptime:   snap.Host.Uptime,
	}
	if d.Docker != nil {
		info.DockerOK = d.Docker.Status(c.Request.Context()).Available
	}
	if d.FW != nil {
		info.Firewall = d.FW.Status(c.Request.Context()).Driver
	}
	if d.Files != nil {
		info.FileRoot = d.Files.RootDisplay()
	}
	httpx.OK(c, info)
}

// NewAgentRouter 是被管端的路由：只暴露运维能力，不含账号、审计、网站监控。
func NewAgentRouter(d *Deps, token string) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(middleware.Recovery(), requestLogger(), agentAuth(token))

	a := r.Group("/agent")
	{
		a.GET("/info", d.agentInfo)
		a.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "pong"}) })

		api := a.Group("/api")
		{
			api.GET("/dashboard/overview", d.overview)
			api.GET("/dashboard/processes", d.processes)

			api.GET("/docker/status", d.dockerStatus)
			api.GET("/containers", d.listContainers)
			api.GET("/containers/stats", d.containerStatsBatch)
			api.GET("/containers/:id", d.inspectContainer)
			api.POST("/containers", d.createContainer)
			api.POST("/containers/:id/:action", d.containerAction)
			api.DELETE("/containers/:id", d.removeContainer)
			api.GET("/containers/:id/stats", d.containerStats)
			api.GET("/containers/:id/logs", d.containerLogs)

			api.GET("/compose/stacks", d.listComposeStacks)
			api.GET("/compose/file", d.getComposeFile)
			api.PUT("/compose/file", d.saveComposeFile)
			api.POST("/compose/up", d.composeUp)

			api.GET("/images", d.listImages)
			api.POST("/images/pull", d.pullImage)
			api.DELETE("/images/:id", d.removeImage)
			api.GET("/volumes", d.listVolumes)
			api.GET("/networks", d.listNetworks)

			api.GET("/files", d.listFiles)
			api.GET("/files/download", d.downloadFile)
			api.GET("/files/content", d.readFile)
			api.PUT("/files/content", d.writeFile)
			api.POST("/files/mkdir", d.mkdir)
			api.POST("/files/touch", d.touch)
			api.POST("/files/rename", d.rename)
			api.POST("/files/copy", d.copyPath)
			api.POST("/files/chmod", d.chmod)
			api.POST("/files/compress", d.compress)
			api.POST("/files/extract", d.extract)
			api.POST("/files/upload", d.upload)
			api.DELETE("/files", d.deleteFile)

			api.GET("/system/info", d.systemInfo)
			api.GET("/system/processes", d.processList)
			api.POST("/system/processes/kill", d.processKill)
			api.GET("/system/firewall", d.firewallStatus)
			api.POST("/system/firewall/rules", d.addFirewallRule)
			api.DELETE("/system/firewall/rules/:id", d.deleteFirewallRule)
			api.POST("/system/firewall/toggle", d.toggleFirewall)
			api.POST("/system/firewall/confirm", d.confirmFirewall)

			// Nacos 连接参数由控制端通过请求头下发，Agent 不落库
			api.GET("/nacos/status", d.nacosStatus)
			api.GET("/nacos/namespaces", d.nacosNamespaces)
			api.GET("/nacos/services", d.nacosServices)
			api.GET("/nacos/instances", d.nacosInstances)
			api.GET("/nacos/configs", d.nacosConfigs)
			api.GET("/nacos/config", d.nacosConfigDetail)
			api.GET("/nacos/diagnose", d.nacosDiagnose)
			api.PUT("/nacos/config", d.nacosPublishConfig)
			api.DELETE("/nacos/config", d.nacosDeleteConfig)

			// Redis 连接参数同样由控制端通过请求头下发，Agent 不落库
			api.GET("/redis/status", d.redisStatus)
			api.GET("/redis/info", d.redisInfo)
			api.GET("/redis/dbs", d.redisDBs)
			api.GET("/redis/keys", d.redisKeys)
			api.GET("/redis/key", d.redisKeyDetail)
			api.PUT("/redis/key", d.redisSetKey)
			api.DELETE("/redis/key", d.redisDeleteKey)
			api.POST("/redis/key/ttl", d.redisExpireKey)
			api.POST("/redis/command", d.redisCommand)
		}

		ws := a.Group("/ws")
		{
			ws.GET("/metrics", d.wsMetrics)
			ws.GET("/containers/:id/logs", d.wsContainerLogs)
			ws.GET("/containers/:id/exec", d.wsContainerExec)
			ws.GET("/terminal/local", d.wsLocalTerminal)
		}
	}
	return r
}
