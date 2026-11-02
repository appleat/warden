package middleware

import (
	"github.com/gin-gonic/gin"

	"warden/internal/fleet"
	"warden/internal/mysqldb"
	"warden/internal/secure"
	"warden/internal/store"
)

// MySQLConn 与 RedisConn / NacosConn 同一套路：按 ?host= 取出该主机已保存的 MySQL
// 连接参数写进请求头。本机请求由本地 handler 直接读取；远程请求带着这些头经反向代理
// 送到 Agent，因此 Agent 端无需落库即可连到目标机能访问的数据库。
// 注意：必须先清空同名请求头，避免客户端伪造后让 Agent 连到任意地址。
func MySQLConn(reg *fleet.Registry, st *store.Store, key []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range mysqldb.HeaderKeys {
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

		var conf store.MySQLConf
		if err := st.DB.Where("host_id = ?", hostID).First(&conf).Error; err != nil {
			c.Next()
			return
		}
		pass, _ := secure.Decrypt(key, conf.Password)
		mysqldb.SetHeaders(c.Request.Header, mysqldb.Config{
			Host:     conf.Host,
			Port:     conf.Port,
			Username: conf.Username,
			Password: pass,
			Database: conf.Database,
			Charset:  conf.Charset,
			TLS:      conf.TLS,
			Insecure: conf.Insecure,
			ReadOnly: conf.ReadOnly,
		})
		c.Next()
	}
}
