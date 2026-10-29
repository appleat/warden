package api

import (
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func goVersion() string { return runtime.Version() }

// requestLogger 只记录 API 与 WebSocket 请求，静态资源不刷日志。
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api") && !strings.HasPrefix(path, "/ws") {
			c.Next()
			return
		}
		start := time.Now()
		c.Next()
		cost := time.Since(start)
		status := c.Writer.Status()
		args := []any{
			"method", c.Request.Method, "path", path, "status", status,
			"cost", cost.String(), "ip", c.ClientIP(),
		}
		switch {
		case status >= 500:
			slog.Error("request", args...)
		case status >= 400:
			slog.Warn("request", args...)
		case cost > 2*time.Second:
			slog.Warn("slow request", args...)
		default:
			slog.Debug("request", args...)
		}
	}
}
