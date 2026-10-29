package fleet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"warden/internal/store"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	// Windows 上必须先关连接，否则临时目录清理会失败
	t.Cleanup(func() {
		if sqlDB, err := st.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	reg, err := New(st, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("初始化注册表失败: %v", err)
	}
	return reg
}

// TestProxyRewrites 验证代理把路径加 /agent 前缀、注入令牌、且不把控制端参数泄漏给 Agent。
func TestProxyRewrites(t *testing.T) {
	var gotPath, gotToken, gotQuery, gotOrigin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Agent-Token")
		gotQuery = r.URL.RawQuery
		gotOrigin = r.Header.Get("Origin")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"items":[]}}`)
	}))
	defer srv.Close()

	reg := newTestRegistry(t)
	token := "warden_unit_test_token"
	enc, err := reg.EncryptToken(token)
	if err != nil {
		t.Fatal(err)
	}
	h := &store.Host{
		Name:    "node-1",
		Kind:    "agent",
		Address: strings.TrimPrefix(srv.URL, "http://"),
		Token:   enc,
		Enabled: true,
	}
	if err := reg.st.DB.Create(h).Error; err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/files?path=%2Fetc&host=9&token=jwt-should-be-stripped", nil)
	rec := httptest.NewRecorder()
	p, err := reg.Proxy(h)
	if err != nil {
		t.Fatal(err)
	}
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	if gotPath != "/agent/api/files" {
		t.Fatalf("路径未加 /agent 前缀: %s", gotPath)
	}
	if gotToken != token {
		t.Fatalf("Agent 令牌未注入: %q", gotToken)
	}
	if strings.Contains(gotQuery, "host=") || strings.Contains(gotQuery, "token=") {
		t.Fatalf("控制端参数泄漏给了 Agent: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "path=") {
		t.Fatalf("业务参数丢失: %s", gotQuery)
	}
	if gotOrigin == "" || !strings.Contains(gotOrigin, "127.0.0.1") {
		t.Fatalf("Origin 未改写为 Agent 地址: %q", gotOrigin)
	}
}

// TestProxyOffline 验证目标不可达时返回统一结构的 502。
func TestProxyOffline(t *testing.T) {
	reg := newTestRegistry(t)
	enc, _ := reg.EncryptToken("t")
	h := &store.Host{Name: "dead", Kind: "agent", Address: "127.0.0.1:1", Token: enc, Enabled: true}
	_ = reg.st.DB.Create(h).Error

	req := httptest.NewRequest(http.MethodGet, "/api/files?host=2", nil)
	rec := httptest.NewRecorder()
	p, err := reg.Proxy(h)
	if err != nil {
		t.Fatal(err)
	}
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("期望 502，实际 %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), "不可达") {
		t.Fatalf("错误信息不友好: %s", body)
	}
}

// TestProbe 验证在线探测能解析 Agent 信息。
func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/info" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Agent-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{
			"version":"1.1.0","hostname":"node-a","platform":"Ubuntu 24.04",
			"kernel":"6.8.0","arch":"x86_64","cpu_cores":8,"total_mem":16000000000,
			"uptime":3600,"docker_ok":true,"firewall":"ufw","file_root":"/"}}`)
	}))
	defer srv.Close()

	reg := newTestRegistry(t)
	enc, _ := reg.EncryptToken("tok")
	h := &store.Host{
		Name:    "probe",
		Kind:    "agent",
		Address: strings.TrimPrefix(srv.URL, "http://"),
		Token:   enc,
		Enabled: true,
	}
	_ = reg.st.DB.Create(h).Error

	st := reg.Probe(t.Context(), h)
	if !st.Online {
		t.Fatalf("应探测为在线: %+v", st)
	}
	if st.Hostname != "node-a" || st.Firewall != "ufw" || !st.DockerOK || st.CPUCores != 8 {
		t.Fatalf("探测字段不正确: %+v", st)
	}

	// 令牌错误应判为离线并给出提示
	h.Token, _ = reg.EncryptToken("wrong")
	bad := reg.Probe(t.Context(), h)
	if bad.Online || !strings.Contains(bad.Error, "令牌") {
		t.Fatalf("错误令牌应失败: %+v", bad)
	}
}

func TestJoinPath(t *testing.T) {
	cases := [][3]string{
		{"", "/agent", "/api/files"},
		{"/", "/agent", "/api/files"},
		{"/prefix", "/agent", "/api/files"},
		{"", "", ""},
	}
	want := []string{"/agent/api/files", "/agent/api/files", "/prefix/agent/api/files", "/"}
	for i, c := range cases {
		got := joinPath(c[0], c[1], c[2])
		if got != want[i] {
			t.Fatalf("joinPath(%q,%q,%q)=%q，期望 %q", c[0], c[1], c[2], got, want[i])
		}
	}
}

func TestInstallCommand(t *testing.T) {
	cmd := InstallCommand("10.0.0.1:8090", "warden_abc", "生产-01")
	for _, want := range []string{"-mode agent", "-agent-listen :8090", "-agent-token warden_abc", "生产-01"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("安装命令缺少 %q: %s", want, cmd)
		}
	}
}

func TestAgentAddr(t *testing.T) {
	for _, in := range []string{"http://1.2.3.4:8090/", "https://1.2.3.4:8090", "1.2.3.4:8090"} {
		if got := AgentAddr(&store.Host{Address: in}); got != "1.2.3.4:8090" {
			t.Fatalf("AgentAddr(%q)=%q", in, got)
		}
	}
}
