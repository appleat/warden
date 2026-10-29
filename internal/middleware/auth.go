package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/auth"
	"warden/internal/httpx"
)

const (
	CtxUID      = "ctx_uid"
	CtxUsername = "ctx_username"
	CtxRole     = "ctx_role"
)

// extractToken 支持 Header、Cookie 与 query（WebSocket 无法自定义 Header）。
func extractToken(c *gin.Context) string {
	if h := c.GetHeader("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(h[7:])
		}
		return strings.TrimSpace(h)
	}
	if v := c.Query("token"); v != "" {
		return v
	}
	if ck, err := c.Cookie("warden_token"); err == nil {
		return ck
	}
	return ""
}

func Auth(m *auth.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractToken(c)
		if token == "" {
			httpx.Unauthorized(c, "未登录")
			return
		}
		claims, err := m.Parse(token)
		if err != nil {
			httpx.Unauthorized(c, err.Error())
			return
		}
		c.Set(CtxUID, claims.UserID)
		c.Set(CtxUsername, claims.Username)
		c.Set(CtxRole, claims.Role)
		c.Next()
	}
}

// RequireRole 要求当前用户角色不低于 min。
func RequireRole(min string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := Role(c)
		if !auth.RoleAtLeast(role, min) {
			httpx.Forbidden(c, "当前角色无权执行此操作")
			return
		}
		c.Next()
	}
}

func UID(c *gin.Context) uint {
	if v, ok := c.Get(CtxUID); ok {
		if u, ok := v.(uint); ok {
			return u
		}
	}
	return 0
}

func Username(c *gin.Context) string {
	if v, ok := c.Get(CtxUsername); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func Role(c *gin.Context) string {
	if v, ok := c.Get(CtxRole); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ---------- 登录接口简单限流 ----------

var (
	limitMu sync.Mutex
	counts  = map[string]int{}
)

// LoginRateLimit 限制单 IP 每分钟最多 20 次登录尝试。
func LoginRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		window := time.Now().Unix() / 60
		key := ip + ":" + strconv.FormatInt(window, 10)

		limitMu.Lock()
		counts[key]++
		n := counts[key]
		if len(counts) > 2048 {
			counts = map[string]int{key: n}
		}
		limitMu.Unlock()

		if n > 20 {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, httpx.Resp{Code: 429, Msg: "尝试过于频繁，请稍后再试"})
			return
		}
		c.Next()
	}
}
