package middleware

import (
	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/nacos"
	"warden/internal/secure"
	"warden/internal/store"
)

// NacosConn 按 ?host= 取出该主机已保存的 Nacos 连接参数，写进请求头。
// 本机请求由本地 handler 直接读取；远程请求会带着这些头经反向代理送到 Agent，
// 因此 Agent 端无需落库即可访问目标机上的 Nacos。
// 注意：必须先清空同名请求头，避免客户端伪造后让 Agent 连到任意地址。
func NacosConn(reg *fleet.Registry, st *store.Store, key []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range nacos.HeaderKeys {
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

		var conf store.NacosConf
		if err := st.DB.Where("host_id = ?", hostID).First(&conf).Error; err != nil {
			c.Next()
			return
		}
		pass, _ := secure.Decrypt(key, conf.Password)
		nacos.SetHeaders(c.Request.Header, nacos.Config{
			Addr:      conf.Addr,
			Username:  conf.Username,
			Password:  pass,
			Namespace: conf.Namespace,
			Insecure:  conf.Insecure,
		})
		c.Next()
	}
}
