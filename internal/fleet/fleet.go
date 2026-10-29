// Package fleet 管理被纳管的主机：本机直连，远程主机通过 Agent 代理。
package fleet

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"warden/internal/secure"
	"warden/internal/store"
)

// Status 是主机的运行时探测结果，不落库。
type Status struct {
	Online    bool      `json:"online"`
	LatencyMs int64     `json:"latency_ms"`
	Version   string    `json:"version"`
	Hostname  string    `json:"hostname"`
	Platform  string    `json:"platform"`
	Kernel    string    `json:"kernel"`
	Arch      string    `json:"arch"`
	CPUCores  int       `json:"cpu_cores"`
	TotalMem  uint64    `json:"total_mem"`
	Uptime    uint64    `json:"uptime"`
	DockerOK  bool      `json:"docker_ok"`
	Firewall  string    `json:"firewall"`
	FileRoot  string    `json:"file_root"`
	Error     string    `json:"error"`
	CheckedAt time.Time `json:"checked_at"`
}

// HostView 是「配置 + 状态」的合并视图。
type HostView struct {
	store.Host
	Status
}

// AgentInfo 是 Agent 端 /agent/info 的返回结构。
type AgentInfo struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	Platform string `json:"platform"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	CPUCores int    `json:"cpu_cores"`
	TotalMem uint64 `json:"total_mem"`
	Uptime   uint64 `json:"uptime"`
	DockerOK bool   `json:"docker_ok"`
	Firewall string `json:"firewall"`
	FileRoot string `json:"file_root"`
}

type Registry struct {
	st    *store.Store
	key   []byte
	local *store.Host

	mu      sync.RWMutex
	status  map[uint]Status
	proxies map[uint]*httputil.ReverseProxy
}

func New(st *store.Store, key []byte) (*Registry, error) {
	local, err := st.EnsureLocalHost()
	if err != nil {
		return nil, err
	}
	return &Registry{
		st:      st,
		key:     key,
		local:   local,
		status:  map[uint]Status{},
		proxies: map[uint]*httputil.ReverseProxy{},
	}, nil
}

func (r *Registry) Store() *store.Store { return r.st }

// Local 返回本机记录（控制端自身）。
func (r *Registry) Local() *store.Host { return r.local }

func (r *Registry) IsLocal(h *store.Host) bool {
	return h == nil || h.Kind == "local" || (r.local != nil && h.ID == r.local.ID)
}

func (r *Registry) Get(id uint) (*store.Host, error) {
	if r.local != nil && id == r.local.ID {
		return r.local, nil
	}
	var h store.Host
	if err := r.st.DB.First(&h, id).Error; err != nil {
		return nil, errors.New("主机不存在")
	}
	return &h, nil
}

// Reload 刷新本机记录缓存（改名后调用）。
func (r *Registry) Reload() error {
	h, err := r.st.EnsureLocalHost()
	if err != nil {
		return err
	}
	r.local = h
	return nil
}

func (r *Registry) Invalidate(id uint) {
	r.mu.Lock()
	delete(r.proxies, id)
	delete(r.status, id)
	r.mu.Unlock()
}

// ---------- 凭据 ----------

// NewToken 生成 Agent 接入令牌。
func NewToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("warden_%d", time.Now().UnixNano())
	}
	return "warden_" + hex.EncodeToString(buf)
}

func (r *Registry) EncryptToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	return secure.Encrypt(r.key, token)
}

func (r *Registry) decryptToken(h *store.Host) string {
	if h.Token == "" {
		return ""
	}
	t, err := secure.Decrypt(r.key, h.Token)
	if err != nil {
		return ""
	}
	return t
}

// ---------- 目标地址 ----------

func (r *Registry) BaseURL(h *store.Host) (*url.URL, error) {
	addr := strings.TrimSpace(h.Address)
	if addr == "" {
		return nil, errors.New("主机地址为空")
	}
	if !strings.Contains(addr, "://") {
		scheme := "http"
		if h.TLS {
			scheme = "https"
		}
		addr = scheme + "://" + addr
	}
	return url.Parse(addr)
}

// AgentAddr 是展示用的地址（去掉 scheme）。
func AgentAddr(h *store.Host) string {
	addr := strings.TrimSpace(h.Address)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	return strings.TrimRight(addr, "/")
}

func (r *Registry) transport(h *store.Host) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     false, // WS 升级走 HTTP/1.1 更稳
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: h.Insecure},
	}
}

// Proxy 返回指向该主机的反向代理。ReverseProxy 原生支持 WebSocket 升级转发。
func (r *Registry) Proxy(h *store.Host) (*httputil.ReverseProxy, error) {
	r.mu.RLock()
	p := r.proxies[h.ID]
	r.mu.RUnlock()
	if p != nil {
		return p, nil
	}

	base, err := r.BaseURL(h)
	if err != nil {
		return nil, err
	}
	token := r.decryptToken(h)
	hostName := h.Name

	p = &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = base.Scheme
			req.URL.Host = base.Host
			req.Host = base.Host
			req.URL.Path = joinPath(base.Path, "/agent", req.URL.Path)

			q := req.URL.Query()
			q.Del("host")
			q.Del("token") // 不把控制端 JWT 泄漏给 Agent
			req.URL.RawQuery = q.Encode()

			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			// Agent 的 WS 握手会校验同源；代理后 Host 已变为 Agent 地址，
			// 这里把 Origin 一并改写，避免被 403 拒绝。
			req.Header.Set("Origin", base.Scheme+"://"+base.Host)
			if token != "" {
				req.Header.Set("X-Agent-Token", token)
			}
			if ip, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				req.Header.Set("X-Forwarded-For", ip)
			}
		},
		Transport:     r.transport(h),
		FlushInterval: -1, // 日志流、下载需要即时刷出
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r.setStatus(h.ID, Status{Error: err.Error(), CheckedAt: time.Now()})
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 502,
				"msg":  "主机【" + hostName + "】不可达：" + friendlyErr(err),
				"data": nil,
			})
		},
	}

	r.mu.Lock()
	r.proxies[h.ID] = p
	r.mu.Unlock()
	return p, nil
}

// ---------- 探测 ----------

func (r *Registry) setStatus(id uint, st Status) {
	r.mu.Lock()
	r.status[id] = st
	r.mu.Unlock()
}

func (r *Registry) StatusOf(id uint) (Status, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	st, ok := r.status[id]
	return st, ok
}

// Probe 主动探测一次远程主机。
func (r *Registry) Probe(ctx context.Context, h *store.Host) Status {
	start := time.Now()
	st := Status{CheckedAt: start}

	base, err := r.BaseURL(h)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	target := strings.TrimRight(base.String(), "/") + "/agent/info"

	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if token := r.decryptToken(h); token != "" {
		req.Header.Set("X-Agent-Token", token)
	}

	client := &http.Client{Timeout: 8 * time.Second, Transport: r.transport(h)}
	resp, err := client.Do(req)
	if err != nil {
		st.Error = friendlyErr(err)
		r.setStatus(h.ID, st)
		return st
	}
	defer resp.Body.Close()
	st.LatencyMs = time.Since(start).Milliseconds()

	if resp.StatusCode == http.StatusUnauthorized {
		st.Error = "Agent 令牌不匹配，请核对主机令牌"
		r.setStatus(h.ID, st)
		return st
	}
	if resp.StatusCode != http.StatusOK {
		st.Error = fmt.Sprintf("Agent 返回 HTTP %d", resp.StatusCode)
		r.setStatus(h.ID, st)
		return st
	}

	var payload struct {
		Code int       `json:"code"`
		Msg  string    `json:"msg"`
		Data AgentInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		st.Error = "Agent 响应解析失败"
		r.setStatus(h.ID, st)
		return st
	}

	d := payload.Data
	st.Online = true
	st.Version = d.Version
	st.Hostname = d.Hostname
	st.Platform = d.Platform
	st.Kernel = d.Kernel
	st.Arch = d.Arch
	st.CPUCores = d.CPUCores
	st.TotalMem = d.TotalMem
	st.Uptime = d.Uptime
	st.DockerOK = d.DockerOK
	st.Firewall = d.Firewall
	st.FileRoot = d.FileRoot

	r.setStatus(h.ID, st)
	return st
}

// StartPolling 周期性探测所有启用的远程主机。
func (r *Registry) StartPolling(ctx context.Context, interval time.Duration) {
	go func() {
		// 启动后先快速跑一轮，避免界面空白
		timer := time.NewTimer(800 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}

			var hosts []store.Host
			r.st.DB.Where("kind = ? AND enabled = ?", "agent", true).Find(&hosts)
			for i := range hosts {
				h := hosts[i]
				r.Probe(ctx, &h)
			}
			timer.Reset(interval)
		}
	}()
}

// ---------- 组装视图 ----------

// List 返回全部主机（含运行时状态）。
func (r *Registry) List() ([]HostView, error) {
	var hosts []store.Host
	if err := r.st.DB.Order("id asc").Find(&hosts).Error; err != nil {
		return nil, err
	}
	// 本机永远在线，且排在第一位
	if r.local != nil {
		found := false
		for i := range hosts {
			if hosts[i].ID == r.local.ID {
				hosts[i].Name = r.local.Name
				hosts[i].Kind = "local"
				found = true
			}
		}
		if !found {
			hosts = append([]store.Host{*r.local}, hosts...)
		}
	}

	out := make([]HostView, 0, len(hosts))
	for i := range hosts {
		h := hosts[i]
		v := HostView{Host: h}
		if h.Kind == "local" {
			v.Status = Status{Online: true, CheckedAt: time.Now()}
		} else if st, ok := r.StatusOf(h.ID); ok {
			v.Status = st
		} else {
			v.Status = Status{Error: "尚未探测"}
		}
		out = append(out, v)
	}
	// 本机置顶
	for i := 1; i < len(out); i++ {
		if out[i].Kind == "local" {
			out[0], out[i] = out[i], out[0]
			break
		}
	}
	return out, nil
}

// ---------- 安装命令 ----------

// InstallCommand 生成在目标机执行的一行命令。
func InstallCommand(serverAddr, token, name string) string {
	return fmt.Sprintf(
		"sudo ./warden -mode agent -agent-listen :8090 -agent-token %s -agent-name %s",
		token, shellQuote(name))
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t'\"$&|;<>()[]{}*?`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------- 工具 ----------

func joinPath(base string, parts ...string) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(base, "/"))
	for _, p := range parts {
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		b.WriteString(p)
	}
	out := b.String()
	if out == "" {
		return "/"
	}
	return out
}

func friendlyErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "actively refused"):
		return "连接被拒绝，Agent 可能未启动"
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "context deadline exceeded"):
		return "连接超时，请检查网络或防火墙"
	case strings.Contains(msg, "certificate"), strings.Contains(msg, "x509"):
		return "TLS 证书校验失败，自签证书请勾选「跳过证书校验」"
	case strings.Contains(msg, "network is unreachable"), strings.Contains(msg, "No connection could be made"):
		return "网络不可达"
	}
	return msg
}
