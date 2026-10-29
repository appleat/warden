// Package rediscli 是 Warden 内置的 Redis 客户端。
//
// 刻意只依赖标准库：面板要保持单二进制、可纯 Go 交叉编译（禁 CGO），
// 不必为了一条 RESP 协议引入第三方驱动；自己实现还能精确控制超时与错误语义，
// 并且天然支持「连接参数由控制端通过请求头下发，Agent 不落库」的多主机模式。
//
// 只使用 RESP2：本客户端不会发送 HELLO，因此 Redis 6+ 也不会切到 RESP3，
// 解析逻辑只需覆盖 `+ - : $ *` 五种前缀。
package rediscli

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout 单次调用（拨号 + 命令）的兜底超时。
const DefaultTimeout = 8 * time.Second

// 连接参数请求头。控制端按 ?host= 取出该主机已保存的连接信息后注入，
// 请求经反向代理送到 Agent，因此 Agent 端无需落库即可连到目标 Redis。
const (
	HeaderAddr     = "X-Warden-Redis-Addr"
	HeaderUser     = "X-Warden-Redis-User"
	HeaderPass     = "X-Warden-Redis-Password"
	HeaderDB       = "X-Warden-Redis-DB"
	HeaderTLS      = "X-Warden-Redis-TLS"
	HeaderInsecure = "X-Warden-Redis-Insecure"
)

// HeaderKeys 需要在入口统一清空的请求头，防止客户端伪造后让 Agent 连到任意地址。
var HeaderKeys = []string{HeaderAddr, HeaderUser, HeaderPass, HeaderDB, HeaderTLS, HeaderInsecure}

// Config 描述一次 Redis 连接。Addr 形如 127.0.0.1:6379。
type Config struct {
	Addr     string
	Username string // Redis 6+ ACL 用户名，留空表示默认用户
	Password string
	DB       int
	TLS      bool
	Insecure bool // 跳过证书校验（自签证书场景）
}

func (c Config) Empty() bool { return strings.TrimSpace(c.Addr) == "" }

// SetHeaders 把连接参数写进请求头。用户名与密码可能含非 ASCII 字符，
// HTTP 头部只允许 ASCII，所以统一 base64 编码。
func SetHeaders(h http.Header, c Config) {
	h.Set(HeaderAddr, c.Addr)
	if c.Username != "" {
		h.Set(HeaderUser, base64.StdEncoding.EncodeToString([]byte(c.Username)))
	}
	if c.Password != "" {
		h.Set(HeaderPass, base64.StdEncoding.EncodeToString([]byte(c.Password)))
	}
	h.Set(HeaderDB, strconv.Itoa(c.DB))
	if c.TLS {
		h.Set(HeaderTLS, "1")
	}
	if c.Insecure {
		h.Set(HeaderInsecure, "1")
	}
}

// ConfigFromHeaders 还原中间件注入的连接参数。
func ConfigFromHeaders(h http.Header) Config {
	if h == nil {
		return Config{}
	}
	return Config{
		Addr:     strings.TrimSpace(h.Get(HeaderAddr)),
		Username: decodeHeader(h.Get(HeaderUser)),
		Password: decodeHeader(h.Get(HeaderPass)),
		DB:       atoi(h.Get(HeaderDB)),
		TLS:      h.Get(HeaderTLS) == "1",
		Insecure: h.Get(HeaderInsecure) == "1",
	}
}

// decodeHeader 兼容明文与 base64 两种取法，避免手工构造请求时踩坑。
func decodeHeader(v string) string {
	if v == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(v); err == nil {
		return string(b)
	}
	return v
}

// NormalizeAddr 归一化用户输入的地址：
// 支持 127.0.0.1、127.0.0.1:6379、redis://host:port/2、rediss://host:port。
// 返回值依次是归一化地址、是否应启用 TLS（由 rediss:// 前缀推断）、
// URL 里带的库号（未指定时为 -1）。
func NormalizeAddr(raw string) (string, bool, int, error) {
	addr := strings.TrimSpace(raw)
	tls := false
	switch {
	case strings.HasPrefix(addr, "rediss://"):
		tls = true
		addr = strings.TrimPrefix(addr, "rediss://")
	case strings.HasPrefix(addr, "redis://"):
		addr = strings.TrimPrefix(addr, "redis://")
	}
	// 用户偶尔会把用户名密码一起粘进来，这里只保留 host:port 部分
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		addr = addr[i+1:]
	}

	// 斜杠之后是库号，例如 redis://10.0.0.5:6379/3
	db := -1
	if i := strings.Index(addr, "/"); i >= 0 {
		rest := strings.TrimSpace(addr[i+1:])
		addr = addr[:i]
		if rest != "" {
			n, err := strconv.Atoi(rest)
			if err != nil || n < 0 {
				return "", tls, -1, errors.New("地址中的库号必须是非负整数")
			}
			db = n
		}
	}

	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", tls, db, errors.New("请填写 Redis 地址")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		// 没带端口时补默认 6379；JoinHostPort 会自动给 IPv6 字面量加方括号
		addr = net.JoinHostPort(addr, "6379")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", tls, db, errors.New("地址格式应为 host:port，例如 127.0.0.1:6379")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return "", tls, db, errors.New("端口必须是 1-65535 之间的数字")
	}
	return addr, tls, db, nil
}

// Client 保存连接参数，实际连接由 Open 建立。
// 每个 API 请求开一条连接、用完即关：面板的并发量很低，
// 这样可以避免连接池里残留 AUTH/SELECT 状态不一致的问题。
type Client struct {
	cfg     Config
	timeout time.Duration
}

func New(cfg Config) (*Client, error) {
	if cfg.Empty() {
		return nil, errors.New("未配置 Redis 地址")
	}
	addr, tlsFromScheme, dbFromURL, err := NormalizeAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	cfg.Addr = addr
	if tlsFromScheme {
		cfg.TLS = true
	}
	// URL 里显式写了库号就以它为准
	if dbFromURL >= 0 {
		cfg.DB = dbFromURL
	}
	if cfg.DB < 0 {
		cfg.DB = 0
	}
	return &Client{cfg: cfg, timeout: DefaultTimeout}, nil
}

func (cl *Client) Config() Config { return cl.cfg }

// Conn 是一条已完成 AUTH/SELECT 的连接。
type Conn struct {
	raw net.Conn
	r   *bufio.Reader
	w   *bufio.Writer
}

func (cl *Client) Open(ctx context.Context) (*Conn, error) {
	if cl.cfg.Empty() {
		return nil, errors.New("未配置 Redis 地址")
	}
	host, _, _ := net.SplitHostPort(cl.cfg.Addr)
	d := &net.Dialer{Timeout: cl.timeout}

	var (
		raw net.Conn
		err error
	)
	if cl.cfg.TLS {
		td := &tls.Dialer{
			NetDialer: d,
			Config:    &tls.Config{ServerName: host, InsecureSkipVerify: cl.cfg.Insecure},
		}
		raw, err = td.DialContext(ctx, "tcp", cl.cfg.Addr)
	} else {
		raw, err = d.DialContext(ctx, "tcp", cl.cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("连接 Redis 失败：%w", err)
	}

	c := &Conn{
		raw: raw,
		r:   bufio.NewReaderSize(raw, 32<<10),
		w:   bufio.NewWriterSize(raw, 32<<10),
	}
	if err := c.handshake(ctx, cl.cfg); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return c, nil
}

func (c *Conn) handshake(ctx context.Context, cfg Config) error {
	if cfg.Password != "" {
		args := []any{"AUTH"}
		// 有用户名才走 ACL 形式：AUTH username password
		if cfg.Username != "" {
			args = append(args, cfg.Username)
		}
		args = append(args, cfg.Password)
		if _, err := c.Do(ctx, args...); err != nil {
			return fmt.Errorf("Redis 认证失败：%w", err)
		}
	}
	if cfg.DB > 0 {
		if _, err := c.Do(ctx, "SELECT", cfg.DB); err != nil {
			return fmt.Errorf("选择数据库 %d 失败：%w", cfg.DB, err)
		}
	}
	return nil
}

func (c *Conn) Close() error { return c.raw.Close() }

// Do 执行一条命令并返回解析后的响应。
// 响应类型：string / int64 / []any / nil；服务端错误以 *ReplyError 返回（连接仍可用）。
func (c *Conn) Do(ctx context.Context, args ...any) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("命令不能为空")
	}
	c.touch(ctx)
	if err := writeCommand(c.w, args); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	return readReply(c.r)
}

// Pipeline 一次发出多条命令再统一读取，键列表里给每个 key 查 TYPE/TTL/内存
// 时可以把几百次往返压成一次。
func (c *Conn) Pipeline(ctx context.Context, cmds [][]any) ([]any, error) {
	if len(cmds) == 0 {
		return nil, nil
	}
	c.touch(ctx)
	for _, args := range cmds {
		if err := writeCommand(c.w, args); err != nil {
			return nil, err
		}
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(cmds))
	for range cmds {
		v, err := readReply(c.r)
		if err != nil {
			// 服务端错误只占位，继续读后续响应，否则整条连接会错位
			out = append(out, err)
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// touch 按 ctx 的 deadline 设置读写超时，没有 deadline 时用默认值兜底。
func (c *Conn) touch(ctx context.Context) {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.raw.SetDeadline(dl)
		return
	}
	_ = c.raw.SetDeadline(time.Now().Add(DefaultTimeout))
}

// ---------- RESP2 编解码 ----------

// ReplyError 是 Redis 返回的 -ERR 类响应，作为普通 error 使用。
type ReplyError struct{ Message string }

func (e *ReplyError) Error() string { return e.Message }

func writeCommand(w *bufio.Writer, args []any) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, a := range args {
		b := argBytes(a)
		if _, err := fmt.Fprintf(w, "$%d\r\n", len(b)); err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if _, err := w.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return nil
}

func argBytes(a any) []byte {
	switch v := a.(type) {
	case string:
		return []byte(v)
	case []byte:
		return v
	case nil:
		return []byte("")
	case int:
		return []byte(strconv.Itoa(v))
	case int64:
		return []byte(strconv.FormatInt(v, 10))
	case float64:
		return []byte(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		if v {
			return []byte("1")
		}
		return []byte("0")
	default:
		return []byte(fmt.Sprint(v))
	}
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readReply(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if line == "" {
		return nil, errors.New("Redis 返回了空响应")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, &ReplyError{Message: line[1:]}
	case ':':
		n, err := strconv.ParseInt(line[1:], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("无法解析整数响应 %q", line)
		}
		return n, nil
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("无法解析批量字符串长度 %q", line)
		}
		if n < 0 {
			return nil, nil // $-1 表示 nil
		}
		buf := make([]byte, n+2) // 末尾还有 \r\n
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("无法解析数组长度 %q", line)
		}
		if n < 0 {
			return nil, nil
		}
		items := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := readReply(r)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	}
	return nil, fmt.Errorf("无法解析的响应类型 %q", line)
}

// ---------- 响应取值助手 ----------

// ReplyString 取标量响应的文本形式。
func ReplyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}

// ReplyInt 取整数响应。
func ReplyInt(v any, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	switch t := v.(type) {
	case int64:
		return t, nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("期望整数响应，实际为 %T", v)
	}
}

// ReplyStrings 把数组响应转成字符串切片，跳过非标量元素。
func ReplyStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		if it == nil {
			continue
		}
		out = append(out, ReplyString(it))
	}
	return out
}

// Format 把响应渲染成接近 redis-cli 的文本，供命令台直接展示。
func Format(v any) string {
	switch t := v.(type) {
	case nil:
		return "(nil)"
	case string:
		return t
	case int64:
		return "(integer) " + strconv.FormatInt(t, 10)
	case *ReplyError:
		return "(error) " + t.Message
	case error:
		return "(error) " + t.Error()
	case []any:
		if len(t) == 0 {
			return "(empty array)"
		}
		var b strings.Builder
		for i, it := range t {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(fmt.Sprintf("%d) %s", i+1, Format(it)))
		}
		return b.String()
	default:
		return fmt.Sprint(t)
	}
}

// ---------- 便捷读取 ----------

// String 执行命令并把结果当字符串返回。
func (c *Conn) String(ctx context.Context, args ...any) (string, error) {
	v, err := c.Do(ctx, args...)
	if err != nil {
		return "", err
	}
	return ReplyString(v), nil
}

// Int 执行命令并把结果当整数返回。
func (c *Conn) Int(ctx context.Context, args ...any) (int64, error) {
	v, err := c.Do(ctx, args...)
	return ReplyInt(v, err)
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
