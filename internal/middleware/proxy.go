package middleware

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/httpx"
	"warden/internal/store"
)

// resolveHost 解析 ?host= 参数。
// 返回 (nil, true) 表示本机请求，应继续走本地 handler；
// 返回 (host, true) 表示远程主机，应代理；
// 返回 (nil, false) 表示已写入错误响应，调用方必须中止。
func resolveHost(reg *fleet.Registry, c *gin.Context) (*store.Host, bool) {
	raw := c.Query("host")
	if raw == "" || raw == "local" || raw == "0" {
		return nil, true
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		httpx.BadRequest(c, "主机参数不合法")
		return nil, false
	}
	h, err := reg.Get(uint(id))
	if err != nil {
		httpx.NotFound(c, "主机不存在或已被删除")
		return nil, false
	}
	if !h.Enabled {
		httpx.Fail(c, http.StatusForbidden, "主机【"+h.Name+"】已停用")
		return nil, false
	}
	return h, true
}

// HostProxy 把 /api 请求按 ?host= 转发到目标主机的 Agent。
// 本机请求原样放行，因此本地逻辑完全不受影响。
func HostProxy(reg *fleet.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		h, ok := resolveHost(reg, c)
		if !ok {
			return
		}
		if h == nil || reg.IsLocal(h) {
			c.Next()
			return
		}
		proxy, err := reg.Proxy(h)
		if err != nil {
			httpx.ServerError(c, "无法创建到主机的连接："+err.Error())
			return
		}
		// 审计日志标注操作发生在哪台主机上
		SetAuditDetail(c, "主机["+h.Name+"] "+h.Address)
		// ReverseProxy 同时处理普通 HTTP 与 WebSocket 升级
		proxy.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

// WorkloadOnly 拒绝对远程主机执行「仅控制端支持」的操作（如网站监控）。
func WorkloadOnly(reg *fleet.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		h, ok := resolveHost(reg, c)
		if !ok {
			return
		}
		if h != nil && !reg.IsLocal(h) {
			httpx.BadRequest(c, "该功能仅支持本机，暂不支持远程主机")
			return
		}
		c.Next()
	}
}
