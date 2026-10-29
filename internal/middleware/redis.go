package middleware

import (
	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/rediscli"
	"warden/internal/secure"
	"warden/internal/store"
)

// RedisConn 与 NacosConn 同一思路：按 ?host= 取出该主机已保存的 Redis 连接参数，
// 写进请求头。本机请求由本地 handler 直接读取；远程请求带着这些头经反向代理
// 送到 Agent，因此 Agent 端无需落库即可访问目标机上的 Redis。
// 注意：必须先清空同名请求头，避免客户端伪造后让 Agent 连到任意地址。
func RedisConn(reg *fleet.Registry, st *store.Store, key []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range rediscli.HeaderKeys {
			c.Request.Header.Del(h)
		}

		h, ok := resolveHost(reg, c)
		if !ok {
			return
		}
		var hostID uint
		if h != nil {
			hostID = h.ID
		} else if local := reg.Local(); local != nil {
			hostID = local.ID
		}
		if hostID == 0 || st == nil {
			c.Next()
			return
		}

		var conf store.RedisConf
		if err := st.DB.Where("host_id = ?", hostID).First(&conf).Error; err != nil {
			c.Next()
			return
		}
		pass, _ := secure.Decrypt(key, conf.Password)
		rediscli.SetHeaders(c.Request.Header, rediscli.Config{
			Addr:     conf.Addr,
			Username: conf.Username,
			Password: pass,
			DB:       conf.DB,
			TLS:      conf.TLS,
			Insecure: conf.Insecure,
		})
		c.Next()
	}
}
