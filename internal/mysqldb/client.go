package mysqldb

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// 连接参数经 HTTP 头在控制端与 Agent 之间传递，因此头部名与编码都在这个包里集中定义，
// 与 internal/rediscli、internal/nacos 保持同一套约定。
const (
	HeaderHost     = "X-Warden-MySQL-Host"
	HeaderPort     = "X-Warden-MySQL-Port"
	HeaderUser     = "X-Warden-MySQL-User"
	HeaderPassword = "X-Warden-MySQL-Password"
	HeaderDB       = "X-Warden-MySQL-DB"
	HeaderCharset  = "X-Warden-MySQL-Charset"
	HeaderTLS      = "X-Warden-MySQL-TLS"
	HeaderInsecure = "X-Warden-MySQL-Insecure"
	HeaderReadOnly = "X-Warden-MySQL-ReadOnly"
)

// HeaderKeys 供中间件清空同名头，防止客户端伪造后让 Agent 连到任意数据库。
var HeaderKeys = []string{
	HeaderHost, HeaderPort, HeaderUser, HeaderPassword,
	HeaderDB, HeaderCharset, HeaderTLS, HeaderInsecure, HeaderReadOnly,
}

// DefaultPort 是 MySQL 默认端口。
const DefaultPort = 3306

// Config 是一份 MySQL 连接参数。
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"` // 默认库，可为空
	Charset  string `json:"charset"`
	TLS      bool   `json:"tls"`
	Insecure bool   `json:"insecure"` // TLS 自签证书时跳过校验
	ReadOnly bool   `json:"read_only"`
}

// Empty 判断是否尚未配置连接。
func (c Config) Empty() bool {
	return strings.TrimSpace(c.Host) == ""
}

// Addr 返回 host:port 形式，端口缺省时补 3306。
func (c Config) Addr() string {
	port := c.Port
	if port <= 0 || port > 65535 {
		port = DefaultPort
	}
	return net.JoinHostPort(strings.TrimSpace(c.Host), strconv.Itoa(port))
}

// effectiveCharset 统一默认字符集，避免个别服务端默认 latin1 导致中文乱码。
func (c Config) effectiveCharset() string {
	cs := strings.TrimSpace(c.Charset)
	if cs == "" {
		return "utf8mb4"
	}
	return cs
}

// SetHeaders 把连接参数写进请求头（字符串一律 base64，因为请求头只能是 ASCII）。
func SetHeaders(h http.Header, c Config) {
	set := func(k, v string) {
		if v == "" {
			h.Del(k)
			return
		}
		h.Set(k, base64.StdEncoding.EncodeToString([]byte(v)))
	}
	set(HeaderHost, strings.TrimSpace(c.Host))
	set(HeaderPort, strconv.Itoa(c.Port))
	set(HeaderUser, c.Username)
	set(HeaderPassword, c.Password)
	set(HeaderDB, c.Database)
	set(HeaderCharset, c.Charset)
	if c.TLS {
		h.Set(HeaderTLS, "1")
	} else {
		h.Del(HeaderTLS)
	}
	if c.Insecure {
		h.Set(HeaderInsecure, "1")
	} else {
		h.Del(HeaderInsecure)
	}
	if c.ReadOnly {
		h.Set(HeaderReadOnly, "1")
	} else {
		h.Del(HeaderReadOnly)
	}
}

// ConfigFromHeaders 从请求头还原连接参数。
func ConfigFromHeaders(h http.Header) Config {
	get := func(k string) string {
		raw := h.Get(k)
		if raw == "" {
			return ""
		}
		if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
			return string(b)
		}
		// 兼容明文头（手工调用 Agent 接口时更直观）
		return raw
	}
	port, _ := strconv.Atoi(get(HeaderPort))
	return Config{
		Host:     get(HeaderHost),
		Port:     port,
		Username: get(HeaderUser),
		Password: get(HeaderPassword),
		Database: get(HeaderDB),
		Charset:  get(HeaderCharset),
		TLS:      h.Get(HeaderTLS) == "1",
		Insecure: h.Get(HeaderInsecure) == "1",
		ReadOnly: h.Get(HeaderReadOnly) == "1",
	}
}

// Open 建立连接池。每次请求开一条、用完即关，避免长期持有一个可能已经失效的连接。
func Open(c Config) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = c.Username
	mc.Passwd = c.Password
	mc.Net = "tcp"
	mc.Addr = c.Addr()
	mc.DBName = strings.TrimSpace(c.Database)
	mc.ParseTime = true
	mc.Loc = time.Local
	mc.Timeout = 6 * time.Second
	mc.ReadTimeout = 60 * time.Second
	mc.WriteTimeout = 60 * time.Second
	mc.Params = map[string]string{"charset": c.effectiveCharset()}
	if c.TLS {
		if c.Insecure {
			mc.TLSConfig = "skip-verify"
		} else {
			mc.TLSConfig = "true"
		}
	}
	db, err := sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return nil, err
	}
	// 面板是单用户操作场景，连接数给很小即可，避免把业务库的连接额度吃掉
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return db, nil
}

// Ping 测试连通性并返回版本号与握手耗时。
func Ping(ctx context.Context, c Config) (int64, string, error) {
	db, err := Open(c)
	if err != nil {
		return 0, "", Friendly(err)
	}
	defer db.Close()

	start := time.Now()
	if err := db.PingContext(ctx); err != nil {
		return 0, "", Friendly(err)
	}
	latency := time.Since(start).Milliseconds()

	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return latency, "", Friendly(err)
	}
	return latency, version, nil
}

// ErrReadOnly 表示连接处于只读模式，拒绝一切写操作。
var ErrReadOnly = errors.New("当前连接处于只读模式，如需写入请在「连接设置」中关闭只读")

// Friendly 把驱动返回的错误翻译成更容易读懂的提示，同时保留原始信息便于排查。
// 注意连接类错误（拒绝连接、DNS 失败）不是 MySQLError，需要单独识别。
func Friendly(err error) error {
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case 1045:
			return fmt.Errorf("账号或密码错误（1045）：%s", me.Message)
		case 1044:
			return fmt.Errorf("该账号无权访问这个数据库（1044）：%s", me.Message)
		case 1049:
			return fmt.Errorf("数据库不存在（1049）：%s", me.Message)
		case 1130:
			return fmt.Errorf("该账号不允许从当前主机连接（1130），请检查账号的 Host 授权：%s", me.Message)
		case 1129:
			return fmt.Errorf("当前主机已被服务端封禁（1129），请先在服务器上执行 FLUSH HOSTS：%s", me.Message)
		case 1226:
			return fmt.Errorf("账号的连接数已超限（1226）：%s", me.Message)
		case 1290:
			return fmt.Errorf("服务端以只读方式运行，拒绝写入（1290）：%s", me.Message)
		case 1064:
			return fmt.Errorf("SQL 语法错误（1064）：%s", me.Message)
		case 1142, 1143:
			return fmt.Errorf("权限不足（%d）：%s", me.Number, me.Message)
		default:
			return fmt.Errorf("MySQL 错误 %d：%s", me.Number, me.Message)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "connect: connection refused") || strings.Contains(msg, "actively refused") {
		return fmt.Errorf("无法连接到 MySQL（%s），请确认地址、端口与服务状态", msg)
	}
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") {
		return fmt.Errorf("连接 MySQL 超时，请检查网络与防火墙：%s", msg)
	}
	if strings.Contains(msg, "no such host") {
		return fmt.Errorf("主机名无法解析：%s", msg)
	}
	return err
}

// quoteIdent 用反引号包裹标识符，内部反引号翻倍转义。
func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// qualified 拼接 `schema`.`table`。
func qualified(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}
