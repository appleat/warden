// Package nacos 是 Nacos OpenAPI 的轻量客户端。
// 只覆盖面板需要的能力：服务端状态、命名空间、服务列表、实例列表、配置读写。
// 不依赖任何第三方 SDK，方便交叉编译与版本兼容（1.x / 2.x 都能用）。
package nacos

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 连接参数 ----------

// Config 是连接一个 Nacos 服务端所需的参数。
type Config struct {
	Addr      string `json:"addr"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Namespace string `json:"namespace"`
	Insecure  bool   `json:"insecure"`
}

// Empty 表示尚未配置连接地址。
func (c Config) Empty() bool { return strings.TrimSpace(c.Addr) == "" }

// 连接参数通过请求头在「控制端 → Agent」之间传递：
// 被管端不落库，因此 handler 在 server / agent 两种模式下完全一致。
// 值统一 base64，避免中文密码或命名空间被 HTTP 头编码破坏。
const (
	HeaderAddr      = "X-Warden-Nacos-Addr"
	HeaderUser      = "X-Warden-Nacos-User"
	HeaderPassword  = "X-Warden-Nacos-Pass"
	HeaderNamespace = "X-Warden-Nacos-Namespace"
	HeaderInsecure  = "X-Warden-Nacos-Insecure"
)

// HeaderKeys 供中间件清理客户端伪造的同名请求头。
var HeaderKeys = []string{HeaderAddr, HeaderUser, HeaderPassword, HeaderNamespace, HeaderInsecure}

// SetHeaders 把连接参数写入请求头。
func SetHeaders(h http.Header, c Config) {
	h.Set(HeaderAddr, b64(c.Addr))
	h.Set(HeaderUser, b64(c.Username))
	h.Set(HeaderPassword, b64(c.Password))
	h.Set(HeaderNamespace, b64(c.Namespace))
	if c.Insecure {
		h.Set(HeaderInsecure, "1")
	} else {
		h.Del(HeaderInsecure)
	}
}

// ConfigFromHeaders 从请求头还原连接参数。
func ConfigFromHeaders(h http.Header) Config {
	return Config{
		Addr:      unb64(h.Get(HeaderAddr)),
		Username:  unb64(h.Get(HeaderUser)),
		Password:  unb64(h.Get(HeaderPassword)),
		Namespace: unb64(h.Get(HeaderNamespace)),
		Insecure:  h.Get(HeaderInsecure) == "1",
	}
}

func b64(s string) string {
	if s == "" {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func unb64(s string) string {
	if s == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// NormalizeAddr 接受 "127.0.0.1:8848"、"http://nacos:8848/nacos" 等写法，
// 统一补全协议、默认端口（8848）与上下文路径（/nacos）。
func NormalizeAddr(raw string) (string, error) {
	a := strings.TrimSpace(raw)
	if a == "" {
		return "", errors.New("请填写 Nacos 地址，例如 127.0.0.1:8848")
	}
	if !strings.Contains(a, "://") {
		a = "http://" + a
	}
	u, err := url.Parse(a)
	if err != nil || u.Host == "" {
		return "", errors.New("Nacos 地址格式不正确，例如 127.0.0.1:8848")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("Nacos 地址仅支持 http / https")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "8848")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/nacos" // 默认上下文路径
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String(), nil
}

// ---------- 客户端 ----------

type Client struct {
	cfg   Config
	base  *url.URL
	http  *http.Client
	token string
	// loginErr 记录登录失败原因，便于把 403 翻译成人话
	loginErr error
}

// New 创建客户端，addr 会被规范化。
func New(cfg Config) (*Client, error) {
	addr, err := NormalizeAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 6 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   6 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: cfg.Insecure},
	}
	return &Client{
		cfg:  cfg,
		base: u,
		http: &http.Client{Timeout: 12 * time.Second, Transport: tr},
	}, nil
}

// ---------- 令牌缓存 ----------
// 控制台每次操作都会新建客户端，登录请求不该重复发，这里按 地址+账号 缓存 accessToken。

type cachedToken struct {
	token string
	exp   time.Time
}

var (
	tokenMu sync.Mutex
	tokens  = map[string]cachedToken{}
)

func (c *Client) cacheKey() string {
	sum := sha256.Sum256([]byte(c.cfg.Password))
	return c.base.String() + "|" + c.cfg.Username + "|" + hex.EncodeToString(sum[:8])
}

func (c *Client) ensureToken(ctx context.Context) {
	if c.token != "" || c.cfg.Username == "" {
		return
	}
	key := c.cacheKey()
	tokenMu.Lock()
	if t, ok := tokens[key]; ok && time.Now().Before(t.exp) {
		tokenMu.Unlock()
		c.token = t.token
		return
	}
	tokenMu.Unlock()

	form := url.Values{}
	form.Set("username", c.cfg.Username)
	form.Set("password", c.cfg.Password)
	body, status, err := c.roundTrip(ctx, http.MethodPost, "/v1/auth/login", nil, form)
	if err != nil {
		c.loginErr = err
		return
	}
	if status >= 400 {
		c.loginErr = fmt.Errorf("用户名或密码错误（HTTP %d）", status)
		return
	}
	var resp struct {
		AccessToken string `json:"accessToken"`
		TokenTTL    int    `json:"tokenTtl"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
		c.loginErr = errors.New("登录响应解析失败，请确认地址指向 Nacos 服务端")
		return
	}
	ttl := time.Duration(resp.TokenTTL) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if ttl > 60*time.Second {
		ttl -= 60 * time.Second
	}
	tokenMu.Lock()
	tokens[key] = cachedToken{token: resp.AccessToken, exp: time.Now().Add(ttl)}
	tokenMu.Unlock()
	c.token = resp.AccessToken
}

func dropToken(key string) {
	tokenMu.Lock()
	delete(tokens, key)
	tokenMu.Unlock()
}

// ---------- 请求 ----------

// callRaw 执行一次请求，返回原始 body、状态码与错误。鉴权失败时会清掉失效令牌重登一次。
func (c *Client) callRaw(ctx context.Context, method, path string, q url.Values, form url.Values) ([]byte, int, error) {
	c.ensureToken(ctx)
	body, status, err := c.roundTrip(ctx, method, path, q, form)
	if err != nil {
		return nil, status, err
	}
	// 缓存的令牌可能已失效：清掉重登一次
	if status == http.StatusForbidden && c.token != "" {
		dropToken(c.cacheKey())
		c.token = ""
		c.ensureToken(ctx)
		body, status, err = c.roundTrip(ctx, method, path, q, form)
		if err != nil {
			return nil, status, err
		}
	}
	return body, status, nil
}

func (c *Client) call(ctx context.Context, method, path string, q url.Values, form url.Values) ([]byte, error) {
	body, status, err := c.callRaw(ctx, method, path, q, form)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, c.statusError(status, body)
	}
	return body, nil
}

func (c *Client) roundTrip(ctx context.Context, method, path string, q url.Values, form url.Values) ([]byte, int, error) {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	vals := url.Values{}
	for k, v := range q {
		vals[k] = v
	}
	if c.token != "" {
		vals.Set("accessToken", c.token)
	}
	u.RawQuery = vals.Encode()

	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "warden-nacos-client")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, friendlyErr(err, c.base.Host)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (c *Client) statusError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if c.loginErr != nil {
			return fmt.Errorf("Nacos 认证失败：%v", c.loginErr)
		}
		if c.cfg.Username == "" {
			return errors.New("Nacos 拒绝访问：该实例已开启鉴权，请填写用户名与密码")
		}
		return errors.New("Nacos 拒绝访问：账号权限不足")
	case http.StatusNotFound:
		return errors.New("接口不存在（HTTP 404），请确认地址是否指向 Nacos 服务端")
	}
	if msg == "" {
		return fmt.Errorf("Nacos 返回 HTTP %d", status)
	}
	return fmt.Errorf("Nacos 返回 HTTP %d：%s", status, msg)
}

func friendlyErr(err error, host string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return fmt.Errorf("无法连接 %s：连接被拒绝，请确认 Nacos 已启动且端口可访问", host)
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("无法连接 %s：域名解析失败", host)
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "Client.Timeout"):
		return fmt.Errorf("连接 %s 超时，请检查网络与防火墙", host)
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"):
		return fmt.Errorf("连接 %s 的 TLS 证书校验失败，自签证书请勾选「跳过证书校验」", host)
	}
	return err
}

// ---------- 数据类型 ----------

type Namespace struct {
	Namespace         string `json:"namespace"`
	NamespaceShowName string `json:"namespace_show_name"`
	Quota             int    `json:"quota"`
	ConfigCount       int    `json:"config_count"`
	Type              int    `json:"type"`
}

type Service struct {
	Name         string `json:"name"`
	GroupName    string `json:"group_name"`
	ClusterCount int    `json:"cluster_count"`
	IPCount      int    `json:"ip_count"`
	HealthyCount int    `json:"healthy_count"`
	TriggerFlag  bool   `json:"trigger_flag"`
}

type Instance struct {
	IP          string            `json:"ip"`
	Port        int               `json:"port"`
	Healthy     bool              `json:"healthy"`
	Enabled     bool              `json:"enabled"`
	Ephemeral   bool              `json:"ephemeral"`
	Weight      float64           `json:"weight"`
	ClusterName string            `json:"cluster_name"`
	Metadata    map[string]string `json:"metadata"`
}

type ConfigItem struct {
	ID      string `json:"id"`
	DataID  string `json:"data_id"`
	Group   string `json:"group"`
	Type    string `json:"type"`
	AppName string `json:"app_name"`
	MD5     string `json:"md5"`
	Tenant  string `json:"namespace"`
}

type ServerState struct {
	Version     string `json:"version"`
	ServerEnv   string `json:"server_env"`
	Datasource  string `json:"datasource"`
	AuthEnabled bool   `json:"auth_enabled"`
	StartupMode string `json:"startup_mode"`
	ConsoleUI   bool   `json:"console_ui"`
}

type Metrics struct {
	ServiceCount  int     `json:"service_count"`
	InstanceCount int     `json:"instance_count"`
	CPU           float64 `json:"cpu"`
	Load          float64 `json:"load"`
	Mem           float64 `json:"mem"`
}

type InstanceList struct {
	Name         string     `json:"name"`
	Clusters     string     `json:"clusters"`
	ProtectThres bool       `json:"reach_protection_threshold"`
	TriggerFlag  bool       `json:"trigger_flag"`
	Items        []Instance `json:"items"`
}

// ---------- 接口 ----------

// Ping 探测服务端可用性，兼容 1.x（没有 readiness 接口）。
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.call(ctx, http.MethodGet, "/v1/console/health/readiness", nil, nil); err == nil {
		return nil
	}
	_, err := c.call(ctx, http.MethodGet, "/v1/ns/operator/metrics", nil, nil)
	return err
}

// State 读取服务端状态（版本、鉴权开关等）。
func (c *Client) State(ctx context.Context) (*ServerState, error) {
	body, err := c.call(ctx, http.MethodGet, "/v1/console/server/state", nil, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Version     string `json:"version"`
		ServerEnv   string `json:"server_env"`
		Datasource  string `json:"datasource_platform"`
		AuthEnabled any    `json:"auth_enabled"`
		StartupMode string `json:"startup_mode"`
		ConsoleUI   any    `json:"console_ui_enabled"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("Nacos 服务端状态解析失败")
	}
	return &ServerState{
		Version:     raw.Version,
		ServerEnv:   raw.ServerEnv,
		Datasource:  raw.Datasource,
		AuthEnabled: truthy(raw.AuthEnabled),
		StartupMode: raw.StartupMode,
		ConsoleUI:   truthy(raw.ConsoleUI),
	}, nil
}

// Metrics 读取服务数 / 实例数等汇总指标。
func (c *Client) Metrics(ctx context.Context) (*Metrics, error) {
	body, err := c.call(ctx, http.MethodGet, "/v1/ns/operator/metrics", nil, nil)
	if err != nil {
		return nil, err
	}
	var raw struct {
		ServiceCount  int     `json:"serviceCount"`
		InstanceCount int     `json:"instanceCount"`
		CPU           float64 `json:"cpu"`
		Load          float64 `json:"load"`
		Mem           float64 `json:"mem"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("Nacos 指标解析失败")
	}
	return &Metrics{
		ServiceCount:  raw.ServiceCount,
		InstanceCount: raw.InstanceCount,
		CPU:           raw.CPU,
		Load:          raw.Load,
		Mem:           raw.Mem,
	}, nil
}

// parseNamespaces 解析命名空间列表，兼容 1.x/2.x（namespace/namespaceShowName）
// 与 3.x 控制台（namespaceId/namespaceName）两种字段名。
func parseNamespaces(body []byte) ([]Namespace, bool) {
	var raw struct {
		Data []struct {
			Namespace         string `json:"namespace"`
			NamespaceID       string `json:"namespaceId"`
			NamespaceShowName string `json:"namespaceShowName"`
			NamespaceName     string `json:"namespaceName"`
			Quota             int    `json:"quota"`
			ConfigCount       int    `json:"configCount"`
			Type              int    `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data == nil {
		return nil, false
	}
	out := make([]Namespace, 0, len(raw.Data))
	for _, n := range raw.Data {
		id := n.Namespace
		if id == "" {
			id = n.NamespaceID
		}
		name := n.NamespaceShowName
		if name == "" {
			name = n.NamespaceName
		}
		out = append(out, Namespace{
			Namespace:         id,
			NamespaceShowName: name,
			Quota:             n.Quota,
			ConfigCount:       n.ConfigCount,
			Type:              n.Type,
		})
	}
	return out, true
}

// Namespaces 列出全部命名空间（public 的 namespace 为空串）。
func (c *Client) Namespaces(ctx context.Context) ([]Namespace, error) {
	var lastErr error
	for _, path := range []string{"/v1/console/namespaces", "/v3/admin/core/namespace/list"} {
		body, status, err := c.callRaw(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			// 传输层错误（连不上 / 超时）：两个端点表现一致，没必要再试
			lastErr = err
			break
		}
		if status == http.StatusNotFound {
			// 该版本确实没有这个端点，改用下一个版本的接口
			lastErr = errors.New("命名空间接口不存在（HTTP 404）")
			continue
		}
		if status >= 400 {
			// 鉴权 / 权限问题：换端点也没用，直接返回，避免被下一个 404 覆盖成误导信息
			lastErr = c.statusError(status, body)
			break
		}
		if out, ok := parseNamespaces(body); ok {
			return out, nil
		}
		lastErr = errors.New("命名空间列表解析失败")
	}
	if lastErr == nil {
		lastErr = errors.New("命名空间列表解析失败")
	}
	return nil, lastErr
}

// ServiceList 是服务列表的返回结果。Source 记录实际命中的接口，便于排查版本差异；
// Hint 只在一条都没查到时填充，带上两个接口的原始返回片段，方便直接定位形状问题。
type ServiceList struct {
	Items  []Service `json:"items"`
	Total  int       `json:"total"`
	Source string    `json:"source"`
	Hint   string    `json:"hint,omitempty"`
}

// serviceDetail 覆盖 catalog / service/list 返回体里的服务结构。
// 字段全部用 any：不同版本类型不一致（triggerFlag 有 bool 也有 "false" 字符串，
// 计数也有数字和字符串两种），写成具体类型会让 json.Unmarshal 直接报错，
// 整个数组解析失败 —— 表现就是接口有数据、页面却一条都不显示。
type serviceDetail struct {
	Name                 string `json:"name"`
	GroupName            string `json:"groupName"`
	ClusterCount         any    `json:"clusterCount"`
	IPCount              any    `json:"ipCount"`
	HealthyCount         any    `json:"healthyCount"`
	HealthyInstanceCount any    `json:"healthyInstanceCount"` // 部分版本用这个字段名
	TriggerFlag          any    `json:"triggerFlag"`
}

// toInt 把 any 形式的 JSON 值转成 int，兼容数字、数字字符串与布尔。
func toInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	case bool:
		if t {
			return 1
		}
	}
	return 0
}

// unwrapData 剥掉 {code,message,data} 这类信封，返回真正的数据体。
// 部分版本（含 3.x 的部分接口）会把结果包在 data 里，不剥就一个字段都匹配不到。
func unwrapData(body []byte) []byte {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return body
	}
	d, ok := probe["data"]
	if !ok || len(d) == 0 || d[0] == '"' {
		return body
	}
	return d
}

func namedServices(names []string, group string) []Service {
	out := make([]Service, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		out = append(out, Service{Name: n, GroupName: group, IPCount: -1, HealthyCount: -1})
	}
	return out
}

// parseServiceList 宽容解析服务列表返回体，catalog 与 service/list 共用。
// 覆盖的形状：{serviceList|pageItems|services|doms} 四种键名、对象数组与字符串数组两种元素、
// 以及 {code,message,data} 信封；实在匹配不到键名就扫描第一个数组字段。
// Source 返回命中的键名，便于排查版本差异。
func parseServiceList(body []byte) (*ServiceList, bool) {
	raw := unwrapData(body)

	// 裸数组：["a","b"]
	var bare []string
	if err := json.Unmarshal(raw, &bare); err == nil {
		items := namedServices(bare, "")
		return &ServiceList{Items: items, Total: len(items), Source: "array"}, true
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, false
	}
	var rawItems json.RawMessage
	source := ""
	for _, k := range []string{"serviceList", "pageItems", "services", "doms"} {
		if v, ok := probe[k]; ok {
			rawItems, source = v, k
			break
		}
	}
	if source == "" {
		// 键名未知：退而求其次，取第一个数组字段
		for k, v := range probe {
			if len(v) > 0 && v[0] == '[' {
				rawItems, source = v, k
				break
			}
		}
	}
	if source == "" {
		return nil, false
	}

	// 元素优先按对象解析，失败再按字符串解析
	var details []serviceDetail
	if err := json.Unmarshal(rawItems, &details); err == nil {
		out := make([]Service, 0, len(details))
		for _, s := range details {
			healthy := toInt(s.HealthyCount)
			if healthy == 0 {
				healthy = toInt(s.HealthyInstanceCount)
			}
			out = append(out, Service{
				Name:         s.Name,
				GroupName:    s.GroupName,
				ClusterCount: toInt(s.ClusterCount),
				IPCount:      toInt(s.IPCount),
				HealthyCount: healthy,
				TriggerFlag:  truthy(s.TriggerFlag),
			})
		}
		total := 0
		for _, k := range []string{"count", "totalCount"} {
			if v, exists := probe[k]; exists {
				var n int
				if json.Unmarshal(v, &n) == nil && n > 0 {
					total = n
					break
				}
			}
		}
		if total <= 0 {
			total = len(out)
		}
		return &ServiceList{Items: out, Total: total, Source: source}, true
	}

	var names []string
	if err := json.Unmarshal(rawItems, &names); err == nil {
		items := namedServices(names, "")
		total := len(items)
		for _, k := range []string{"count", "totalCount"} {
			if v, exists := probe[k]; exists {
				var n int
				if json.Unmarshal(v, &n) == nil && n > 0 {
					total = n
					break
				}
			}
		}
		return &ServiceList{Items: items, Total: total, Source: source}, true
	}
	return nil, false
}

// Services 分页列出服务。
// 优先用 catalog 接口（能带实例数），形状不认识或接口不存在时回落到 service/list。
func (c *Client) Services(ctx context.Context, ns, group, keyword string, page, size int) (*ServiceList, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 500 {
		size = 100
	}
	// 参数照抄 Nacos 控制台自己的请求：hasIpCount=true 才会带出实例数
	q := url.Values{}
	q.Set("hasIpCount", "true")
	q.Set("pageNo", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(size))
	q.Set("namespaceId", ns)
	// 分组过滤的键名是 groupNameParam（控制台就是这么发的），老版本有的认 groupName，
	// 两个都发、值相同，互不冲突；空值一律不发，否则部分版本会当字面量过滤条件查不到东西
	if group != "" {
		q.Set("groupName", group)
		q.Set("groupNameParam", group)
	}
	if keyword != "" {
		q.Set("serviceNameParam", keyword)
	}
	q2 := url.Values{}
	for k, vs := range q {
		for _, v := range vs {
			q2.Add(k, v)
		}
	}

	// 一条都没查到时，把两个接口的原始返回片段带出去，省得再来回猜形状
	var catalogBody, listBody []byte
	withHint := func(l *ServiceList) *ServiceList {
		if l != nil && len(l.Items) == 0 {
			l.Hint = "catalog: " + snippet(catalogBody) + " ｜ service/list: " + snippet(listBody)
		}
		return l
	}

	// 1) 优先 catalog（能带实例数）
	var catalog *ServiceList
	if body, err := c.call(ctx, http.MethodGet, "/v1/ns/catalog/services", q, nil); err == nil {
		catalogBody = body
		if list, ok := parseServiceList(body); ok {
			if list.Source == "serviceList" {
				list.Source = "catalog"
			} else {
				list.Source = "catalog-1x"
			}
			catalog = list
			if len(list.Items) > 0 {
				return list, nil
			}
		}
	}

	// 2) 兜底 service/list：catalog 不可用，或者可用但一条都没返回（版本差异很常见）
	body, err := c.call(ctx, http.MethodGet, "/v1/ns/service/list", q2, nil)
	if err != nil {
		if catalog != nil {
			// catalog 至少通了，只是这个命名空间确实没服务
			return withHint(catalog), nil
		}
		return nil, err
	}
	listBody = body
	sl, ok := parseServiceList(body)
	if !ok {
		if catalog != nil {
			return withHint(catalog), nil
		}
		return nil, errors.New("服务列表解析失败，请确认地址指向 Nacos 服务端")
	}
	sl.Source = "service-list"
	if sl.Total <= 0 {
		sl.Total = len(sl.Items)
	}
	if catalog != nil && len(catalog.Items) >= len(sl.Items) {
		return withHint(catalog), nil
	}
	return withHint(sl), nil
}

// Instances 列出某个服务的实例。
func (c *Client) Instances(ctx context.Context, ns, group, name string, healthyOnly bool) (*InstanceList, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("请选择服务")
	}
	q := url.Values{}
	q.Set("serviceName", name)
	q.Set("groupName", group)
	q.Set("namespaceId", ns)
	if healthyOnly {
		q.Set("healthyOnly", "true")
	}
	body, err := c.call(ctx, http.MethodGet, "/v1/ns/instance/list", q, nil)
	if err != nil {
		return nil, err
	}
	// 同样用 any 收：这台服务端把 triggerFlag 返回成字符串，实例字段很可能也是
	var raw struct {
		Name     string `json:"name"`
		Clusters string `json:"clusters"`
		Protect  any    `json:"reachProtectionThreshold"`
		Trigger  any    `json:"triggerFlag"`
		Hosts    []struct {
			IP          string         `json:"ip"`
			Port        any            `json:"port"`
			Healthy     any            `json:"healthy"`
			Enabled     any            `json:"enabled"`
			Ephemeral   any            `json:"ephemeral"`
			Weight      any            `json:"weight"`
			ClusterName string         `json:"clusterName"`
			Metadata    map[string]any `json:"metadata"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("实例列表解析失败")
	}
	out := &InstanceList{
		Name:         raw.Name,
		Clusters:     raw.Clusters,
		ProtectThres: truthy(raw.Protect),
		TriggerFlag:  truthy(raw.Trigger),
	}
	for _, h := range raw.Hosts {
		enabled := true
		if h.Enabled != nil {
			enabled = truthy(h.Enabled)
		}
		meta := make(map[string]string, len(h.Metadata))
		for k, v := range h.Metadata {
			meta[k] = toStr(v)
		}
		out.Items = append(out.Items, Instance{
			IP: h.IP, Port: toInt(h.Port), Healthy: truthy(h.Healthy), Enabled: enabled,
			Ephemeral: truthy(h.Ephemeral), Weight: toFloat(h.Weight), ClusterName: h.ClusterName,
			Metadata: meta,
		})
	}
	return out, nil
}

// toFloat 把 any 形式的 JSON 值转成 float64，兼容数字与数字字符串。
func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

// toStr 把 any 形式的 JSON 值转成字符串，用于 metadata 里的非字符串值。
func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

// Configs 分页列出配置。dataID 为空时按全量模糊匹配。
func (c *Client) Configs(ctx context.Context, ns, group, dataID string, page, size int) ([]ConfigItem, int, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 500 {
		size = 50
	}
	if strings.TrimSpace(dataID) == "" {
		dataID = "*"
	} else {
		dataID = "*" + dataID + "*"
	}
	if strings.TrimSpace(group) == "" {
		group = "*"
	}
	q := url.Values{}
	q.Set("search", "blur")
	q.Set("dataId", dataID)
	q.Set("group", group)
	q.Set("pageNo", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(size))
	setConfigScope(q, ns)
	body, err := c.call(ctx, http.MethodGet, "/v1/cs/configs", q, nil)
	if err != nil {
		return nil, 0, err
	}
	var raw struct {
		TotalCount int `json:"totalCount"`
		PageItems  []struct {
			ID      string `json:"id"`
			DataID  string `json:"dataId"`
			Group   string `json:"group"`
			Type    string `json:"type"`
			AppName string `json:"appName"`
			MD5     string `json:"md5"`
			Tenant  string `json:"tenant"`
		} `json:"pageItems"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, 0, errors.New("配置列表解析失败")
	}
	out := make([]ConfigItem, 0, len(raw.PageItems))
	for _, it := range raw.PageItems {
		out = append(out, ConfigItem{
			ID: it.ID, DataID: it.DataID, Group: it.Group,
			Type: it.Type, AppName: it.AppName, MD5: it.MD5, Tenant: it.Tenant,
		})
	}
	return out, raw.TotalCount, nil
}

// ConfigContent 读取配置正文（Nacos 直接返回纯文本）。
func (c *Client) ConfigContent(ctx context.Context, ns, group, dataID string) (string, error) {
	if strings.TrimSpace(dataID) == "" {
		return "", errors.New("请选择配置")
	}
	q := url.Values{}
	q.Set("dataId", dataID)
	q.Set("group", group)
	setConfigScope(q, ns)
	body, err := c.call(ctx, http.MethodGet, "/v1/cs/configs", q, nil)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return "", errors.New("配置不存在或已被删除")
		}
		return "", err
	}
	return string(body), nil
}

// PublishConfig 新增或更新配置。
func (c *Client) PublishConfig(ctx context.Context, ns, group, dataID, content, typ string) error {
	if strings.TrimSpace(dataID) == "" {
		return errors.New("请填写 Data ID")
	}
	if strings.TrimSpace(group) == "" {
		return errors.New("请填写分组")
	}
	form := url.Values{}
	form.Set("dataId", dataID)
	form.Set("group", group)
	form.Set("content", content)
	setConfigScope(form, ns)
	if typ != "" {
		form.Set("type", typ)
	}
	body, err := c.call(ctx, http.MethodPost, "/v1/cs/configs", nil, form)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != "true" {
		return fmt.Errorf("发布失败：%s", strings.TrimSpace(string(body)))
	}
	return nil
}

// DeleteConfig 删除配置。
func (c *Client) DeleteConfig(ctx context.Context, ns, group, dataID string) error {
	if strings.TrimSpace(dataID) == "" {
		return errors.New("请选择配置")
	}
	q := url.Values{}
	q.Set("dataId", dataID)
	q.Set("group", group)
	setConfigScope(q, ns)
	body, err := c.call(ctx, http.MethodDelete, "/v1/cs/configs", q, nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != "true" {
		return fmt.Errorf("删除失败：%s", strings.TrimSpace(string(body)))
	}
	return nil
}

// setConfigScope 给配置接口设置命名空间。
// 关键点：Nacos 的配置接口（/v1/cs/configs）认的是 tenant，命名接口认的是 namespaceId。
// 1.x 只认 tenant，只传 namespaceId 会永远落在 public —— 表现就是「切命名空间配置不变」，
// 所以两个都带上（新版本两者等价，同时传不冲突）。
func setConfigScope(v url.Values, ns string) {
	v.Set("tenant", ns)
	v.Set("namespaceId", ns)
}

// ---------- 诊断 ----------

// Probe 是一次接口探测的结果，用于排查不同 Nacos 版本的差异。
type Probe struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
	OK      bool   `json:"ok"`
	Note    string `json:"note"`
	Snippet string `json:"snippet"`
}

// Diagnose 逐个探测面板用到的接口，返回状态码与返回片段。
func (c *Client) Diagnose(ctx context.Context, ns string) []Probe {
	c.ensureToken(ctx)
	out := make([]Probe, 0, 8)
	add := func(name, path string, q url.Values) ([]byte, int) {
		body, status, err := c.roundTrip(ctx, http.MethodGet, path, q, nil)
		p := Probe{Name: name, Path: path, Status: status, OK: err == nil && status < 400}
		if err != nil {
			p.Note = err.Error()
		}
		p.Snippet = snippet(body)
		out = append(out, p)
		return body, status
	}

	add("服务端状态", "/v1/console/server/state", nil)
	add("就绪检查", "/v1/console/health/readiness", nil)
	add("命名空间列表", "/v1/console/namespaces", nil)

	// 参数必须和 Services() 真实请求保持一致（空值不传），否则诊断结果没有参考价值
	sq := url.Values{}
	sq.Set("hasIpCount", "true")
	sq.Set("pageNo", "1")
	sq.Set("pageSize", "20")
	sq.Set("namespaceId", ns)
	body, status := add("服务列表 catalog", "/v1/ns/catalog/services", sq)
	if status < 400 {
		if list, ok := parseServiceList(body); ok {
			out[len(out)-1].Note = "已解析，命中键 " + list.Source + "，本页 " +
				strconv.Itoa(len(list.Items)) + " 条 / 共 " + strconv.Itoa(list.Total) + " 个"
		} else {
			out[len(out)-1].Note = "返回体形状不认识，已回落到 service/list"
		}
	}
	i := len(out)
	body, status = add("服务列表 service/list", "/v1/ns/service/list", sq)
	if status < 400 {
		if list, ok := parseServiceList(body); ok {
			out[i].Note = "已解析，命中键 " + list.Source + "，本页 " +
				strconv.Itoa(len(list.Items)) + " 条 / 共 " + strconv.Itoa(list.Total) + " 个"
		} else {
			out[i].Note = "返回体形状不认识"
		}
	}

	cq := url.Values{}
	cq.Set("search", "blur")
	cq.Set("dataId", "*")
	cq.Set("group", "*")
	cq.Set("pageNo", "1")
	cq.Set("pageSize", "1")
	setConfigScope(cq, ns)
	add("配置列表", "/v1/cs/configs", cq)
	return out
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return s
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	}
	return false
}
