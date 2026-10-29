package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"warden/internal/store"
)

type state struct {
	Status      string
	LatencyMs   int64
	LastCode    int
	LastMessage string
	LastCheck   time.Time
	CertDays    int
	Fails       int
	LastRun     time.Time
}

type Service struct {
	store  *store.Store
	notify *Notifier

	mu    sync.RWMutex
	state map[uint]*state

	sem chan struct{}
}

func New(st *store.Store, notify *Notifier) *Service {
	return &Service{
		store:  st,
		notify: notify,
		state:  map[uint]*state{},
		sem:    make(chan struct{}, 12),
	}
}

// Start 启动后台巡检循环（每 3 秒扫一次到期任务）。
func (s *Service) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		cleanTicker := time.NewTicker(6 * time.Hour)
		defer cleanTicker.Stop()
		s.loop(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.loop(ctx)
			case <-cleanTicker.C:
				cutoff := time.Now().AddDate(0, 0, -30)
				s.store.DB.Where("created_at < ?", cutoff).Delete(&store.MonitorResult{})
			}
		}
	}()
}

func (s *Service) loop(ctx context.Context) {
	var list []store.Monitor
	if err := s.store.DB.Where("enabled = ?", true).Find(&list).Error; err != nil {
		return
	}
	now := time.Now()
	for i := range list {
		m := list[i]
		interval := m.Interval
		if interval < 10 {
			interval = 60
		}
		s.mu.RLock()
		st := s.state[m.ID]
		due := st == nil || now.Sub(st.LastRun) >= time.Duration(interval)*time.Second
		s.mu.RUnlock()
		if !due {
			continue
		}
		s.mu.Lock()
		if s.state[m.ID] == nil {
			s.state[m.ID] = &state{Status: "unknown"}
		}
		s.state[m.ID].LastRun = now
		s.mu.Unlock()

		mm := m
		go func() {
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-ctx.Done():
				return
			}
			s.check(context.Background(), &mm)
		}()
	}
}

// SetEnabled 在任务被启动时重置运行时态：清掉失败计数、状态回到 unknown，
// 并把 LastRun 归零让它立刻跑一次，不用干等一个完整间隔。
//
// 停止时不需要动内存态——巡检循环只扫 enabled=true 的任务，自然就不再执行；
// 界面上的「已停止」由 View 依据 enabled 推导（这样手动「检测」仍能拿到真实结果）。
func (s *Service) SetEnabled(id uint, enabled bool) {
	if !enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[id]
	if st == nil {
		st = &state{}
		s.state[id] = st
	}
	st.Status = "unknown"
	st.Fails = 0
	st.LastRun = time.Time{}
}

// CheckNow 立即检测一次。
func (s *Service) CheckNow(id uint) error {
	var m store.Monitor
	if err := s.store.DB.First(&m, id).Error; err != nil {
		return err
	}
	s.check(context.Background(), &m)
	return nil
}

func (s *Service) check(ctx context.Context, m *store.Monitor) {
	timeout := time.Duration(m.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	start := time.Now()
	ok, code, msg, certDays := probe(ctx, m, timeout)
	latency := time.Since(start).Milliseconds()

	s.mu.Lock()
	st := s.state[m.ID]
	if st == nil {
		st = &state{Status: "unknown"}
		s.state[m.ID] = st
	}
	prev := st.Status
	st.LastCheck = time.Now()
	st.LatencyMs = latency
	st.LastCode = code
	st.LastMessage = msg
	st.CertDays = certDays
	if ok {
		st.Fails = 0
		st.Status = "up"
	} else {
		st.Fails++
		retries := m.FailRetries
		if retries <= 0 {
			retries = 1
		}
		if st.Fails >= retries {
			st.Status = "down"
		} else {
			st.Status = "pending"
		}
	}
	cur := st.Status
	fails := st.Fails
	s.mu.Unlock()

	// 落库
	res := &store.MonitorResult{
		MonitorID: m.ID, OK: ok, StatusCode: code, LatencyMs: latency,
		Message: truncate(msg, 240), CreatedAt: time.Now(),
	}
	go func() {
		if err := s.store.DB.Create(res).Error; err != nil {
			slog.Error("写入监控结果失败", "err", err)
		}
	}()

	// 状态跳变告警：连续失败达到阈值才从 up/pending 转为 down
	if s.notify != nil && prev != cur {
		switch {
		case cur == "down":
			s.notify.Send(fmt.Sprintf("🔴 站点异常：%s", m.Name),
				fmt.Sprintf("目标：%s\n状态码：%d\n耗时：%dms\n原因：%s\n连续失败：%d 次\n时间：%s",
					m.Target, code, latency, msg, fails, time.Now().Format("2006-01-02 15:04:05")))
		case cur == "up" && (prev == "down" || prev == "pending"):
			s.notify.Send(fmt.Sprintf("🟢 站点恢复：%s", m.Name),
				fmt.Sprintf("目标：%s\n耗时：%dms\n时间：%s", m.Target, latency, time.Now().Format("2006-01-02 15:04:05")))
		}
	}
}

func probe(ctx context.Context, m *store.Monitor, timeout time.Duration) (bool, int, string, int) {
	switch m.Type {
	case "tcp":
		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, "tcp", m.Target)
		if err != nil {
			return false, 0, err.Error(), 0
		}
		conn.Close()
		return true, 0, "TCP 连接成功", 0
	case "http", "keyword", "":
		return probeHTTP(ctx, m, timeout)
	default:
		return false, 0, "不支持的检测类型: " + m.Type, 0
	}
}

func probeHTTP(ctx context.Context, m *store.Monitor, timeout time.Duration) (bool, int, string, int) {
	target := m.Target
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "http://" + target
	}
	method := strings.ToUpper(m.Method)
	if method == "" {
		method = http.MethodGet
	}
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, target, nil)
	if err != nil {
		return false, 0, err.Error(), 0
	}
	req.Header.Set("User-Agent", "Warden-Monitor/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, simplifyErr(err), 0
	}
	defer resp.Body.Close()

	certDays := 0
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		certDays = int(time.Until(resp.TLS.PeerCertificates[0].NotAfter).Hours() / 24)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	expect := m.ExpectCode
	if expect == 0 {
		expect = 200
	}
	if expect == 200 {
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			return false, resp.StatusCode, fmt.Sprintf("期望状态码 2xx/3xx，实际 %d", resp.StatusCode), certDays
		}
	} else if resp.StatusCode != expect {
		return false, resp.StatusCode, fmt.Sprintf("期望状态码 %d，实际 %d", expect, resp.StatusCode), certDays
	}

	if m.Keyword != "" && !strings.Contains(string(body), m.Keyword) {
		return false, resp.StatusCode, "响应内容未包含关键字: " + m.Keyword, certDays
	}
	if certDays > 0 && m.CertWarnDays > 0 && certDays <= m.CertWarnDays {
		return true, resp.StatusCode, fmt.Sprintf("证书仅剩 %d 天", certDays), certDays
	}
	return true, resp.StatusCode, "正常", certDays
}

func simplifyErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host"):
		return "域名解析失败"
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "请求超时"
	case strings.Contains(msg, "certificate"):
		return "证书校验失败: " + msg
	}
	if len(msg) > 180 {
		msg = msg[:180]
	}
	return msg
}

// View 返回带运行时态的监控项。已停用的任务固定显示 paused。
func (s *Service) View(m *store.Monitor, uptime float64) store.Monitor {
	return s.view(m, uptime, true)
}

// ViewLive 同 View，但即使任务已停用也返回真实检测状态（手动「检测」时用，
// 否则停用状态下点检测，结果会被 paused 覆盖掉）。
func (s *Service) ViewLive(m *store.Monitor, uptime float64) store.Monitor {
	return s.view(m, uptime, false)
}

func (s *Service) view(m *store.Monitor, uptime float64, respectEnabled bool) store.Monitor {
	out := *m
	s.mu.RLock()
	st := s.state[m.ID]
	s.mu.RUnlock()
	if st == nil {
		out.Status = "unknown"
	} else {
		out.Status = st.Status
		out.LatencyMs = st.LatencyMs
		out.LastCode = st.LastCode
		out.LastMessage = st.LastMessage
		out.CertDays = st.CertDays
		if !st.LastCheck.IsZero() {
			t := st.LastCheck
			out.LastCheckAt = &t
		}
	}
	// 以库里的 enabled 为准：进程重启后内存态是空的，但停用的任务仍应显示「已停止」
	if respectEnabled && !m.Enabled {
		out.Status = "paused"
	}
	out.Uptime = uptime
	return out
}

type HistoryPoint struct {
	Time      int64 `json:"time"`
	OK        bool  `json:"ok"`
	LatencyMs int64 `json:"latency"`
	Code      int   `json:"code"`
}

// History 返回最近 hours 小时的检测点（自动降采样到 ~300 点）。
func (s *Service) History(id uint, hours int) ([]HistoryPoint, float64) {
	if hours <= 0 {
		hours = 24
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	var rows []store.MonitorResult
	s.store.DB.Where("monitor_id = ? AND created_at >= ?", id, since).
		Order("created_at asc").Limit(3000).Find(&rows)

	points := make([]HistoryPoint, 0, len(rows))
	var okCount int
	for _, r := range rows {
		points = append(points, HistoryPoint{Time: r.CreatedAt.Unix(), OK: r.OK, LatencyMs: r.LatencyMs, Code: r.StatusCode})
		if r.OK {
			okCount++
		}
	}
	uptime := 100.0
	if len(rows) > 0 {
		uptime = float64(okCount) / float64(len(rows)) * 100
	}
	if len(points) > 400 {
		step := len(points) / 400
		shrunk := make([]HistoryPoint, 0, 400)
		for i := 0; i < len(points); i += step {
			shrunk = append(shrunk, points[i])
		}
		points = shrunk
	}
	return points, uptime
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
