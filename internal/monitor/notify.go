package monitor

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"warden/internal/store"
)

// Notifier 负责把告警推送到 Webhook（企业微信 / 钉钉 / 飞书 / 通用 JSON）。
type Notifier struct {
	store *store.Store

	mu       sync.RWMutex
	url      string
	enabled  bool
	lastSent map[string]time.Time
}

func NewNotifier(st *store.Store) *Notifier {
	n := &Notifier{store: st, lastSent: map[string]time.Time{}}
	n.Reload()
	return n
}

func (n *Notifier) Reload() {
	url := n.store.GetSetting("alert_webhook", "")
	enabled := n.store.GetSetting("alert_enabled", "false") == "true"
	n.mu.Lock()
	n.url, n.enabled = url, enabled
	n.mu.Unlock()
}

func (n *Notifier) Configure(url string, enabled bool) {
	n.mu.Lock()
	n.url, n.enabled = url, enabled
	n.mu.Unlock()
	n.store.SetSetting("alert_webhook", url)
	n.store.SetSetting("alert_enabled", map[bool]string{true: "true", false: "false"}[enabled])
}

func (n *Notifier) Config() (string, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.url, n.enabled
}

// Send 异步推送，同类消息 2 分钟内去重，避免告警风暴。
func (n *Notifier) Send(title, content string) {
	n.mu.Lock()
	url, enabled := n.url, n.enabled
	if last, ok := n.lastSent[title]; ok && time.Since(last) < 2*time.Minute {
		n.mu.Unlock()
		return
	}
	if enabled && url != "" {
		n.lastSent[title] = time.Now()
	}
	n.mu.Unlock()

	if !enabled || url == "" {
		slog.Info("监控告警（未配置通知渠道）", "title", title, "content", content)
		return
	}

	go func() {
		payload := buildPayload(url, title, content)
		b, _ := json.Marshal(payload)
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(b))
		if err != nil {
			slog.Error("推送告警失败", "err", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			slog.Error("推送告警返回异常", "status", resp.StatusCode)
		}
	}()
}

func buildPayload(url, title, content string) any {
	text := title + "\n" + content
	switch {
	case strings.Contains(url, "qyapi.weixin.qq.com"):
		return map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	case strings.Contains(url, "oapi.dingtalk.com"):
		return map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	case strings.Contains(url, "feishu.cn"), strings.Contains(url, "larksuite.com"):
		return map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	default:
		return map[string]any{
			"title": title, "content": content,
			"timestamp": time.Now().Format(time.RFC3339),
		}
	}
}
