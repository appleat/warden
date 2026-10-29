package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"

	"warden/internal/httpx"
	"warden/internal/store"
)

// Audit 自动记录所有写操作（POST/PUT/DELETE），审计日志异步落库。
func Audit(st *store.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()

		result := "success"
		if c.Writer.Status() >= 400 {
			result = "failed"
		}
		entry := &store.AuditLog{
			UserID:   UID(c),
			Username: Username(c),
			Action:   method + " " + c.FullPath(),
			Target:   c.Request.URL.Path,
			IP:       c.ClientIP(),
			Result:   result,
			Detail:   c.GetString("audit_detail"),
		}
		cost := time.Since(start).Milliseconds()
		go func() {
			if err := st.DB.Create(entry).Error; err != nil {
				slog.Error("写入审计日志失败", "err", err)
				return
			}
			slog.Debug("audit", "action", entry.Action, "user", entry.Username, "cost_ms", cost, "result", result)
		}()
	}
}

// SetAuditDetail 供 handler 补充审计细节。
func SetAuditDetail(c *gin.Context, detail string) {
	c.Set("audit_detail", detail)
}

// Recovery 捕获 panic 并返回统一 JSON，避免连接被直接断开。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic recovered", "err", r, "path", c.Request.URL.Path,
					"stack", string(debug.Stack()))
				httpx.ServerError(c, "服务器内部错误")
			}
		}()
		c.Next()
	}
}
