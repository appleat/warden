package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"warden/internal/auth"
	"warden/internal/config"
	"warden/internal/fleet"
	"warden/internal/middleware"
	"warden/internal/store"
)

// ---------- 假 Redis ----------

// testRedis 只实现面板用到的命令，供接口层测试使用。
type testRedis struct {
	ln   net.Listener
	keys []string // SCAN 返回的键名，默认 user:1 / cfg:app
}

// defaultTestKeys 是绝大多数用例依赖的固定键集。
var defaultTestKeys = []string{"user:1", "cfg:app"}

func startTestRedis(t *testing.T) *testRedis {
	t.Helper()
	return startTestRedisWithKeys(t, defaultTestKeys...)
}

func startTestRedisWithKeys(t *testing.T, keys ...string) *testRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	s := &testRedis{ln: ln, keys: keys}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *testRedis) addr() string { return s.ln.Addr().String() }

func (s *testRedis) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	for {
		args, err := readTestCmd(r)
		if err != nil {
			return
		}
		if _, err := w.WriteString(s.reply(args)); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func readTestCmd(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("bad command line %q", line)
	}
	n, _ := strconv.Atoi(line[1:])
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		h, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		l, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimRight(h, "\r\n"), "$"))
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		out = append(out, string(buf[:l]))
	}
	return out, nil
}

func testBulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func testArr(items ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(items))
	for _, it := range items {
		b.WriteString(testBulk(it))
	}
	return b.String()
}

var testRedisMu sync.Mutex

func (s *testRedis) reply(args []string) string {
	testRedisMu.Lock()
	defer testRedisMu.Unlock()
	switch strings.ToUpper(args[0]) {
	case "PING":
		return "+PONG\r\n"
	case "AUTH", "SELECT":
		return "+OK\r\n"
	case "INFO":
		return testBulk("# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\n" +
			"# Clients\r\nconnected_clients:1\r\n# Memory\r\nused_memory:2048\r\nused_memory_human:2.00K\r\n" +
			"# Stats\r\nkeyspace_hits:8\r\nkeyspace_misses:2\r\n# Replication\r\nrole:master\r\n" +
			"# Keyspace\r\ndb0:keys=2,expires=0,avg_ttl=0\r\n")
	case "DBSIZE":
		return fmt.Sprintf(":%d\r\n", len(s.keys))
	case "SCAN":
		return "*2\r\n" + testBulk("0") + testArr(s.keys...)
	case "TYPE":
		// 只认识 SCAN 返回过的键，其余按「不存在」处理。
		// 这样「用转义展示名去查二进制键」这类错误会被真实地判成 404。
		found := false
		for _, k := range s.keys {
			if k == args[1] {
				found = true
				break
			}
		}
		if !found {
			return "+none\r\n"
		}
		if args[1] == "cfg:app" {
			return "+hash\r\n"
		}
		return "+string\r\n"
	case "TTL":
		return ":-1\r\n"
	case "MEMORY":
		return ":64\r\n"
	case "STRLEN":
		return ":4\r\n"
	case "GETRANGE":
		return testBulk("demo")
	case "OBJECT":
		return testBulk("embstr")
	case "HLEN":
		return ":1\r\n"
	case "HSCAN":
		return "*2\r\n" + testBulk("0") + testArr("env", "prod")
	case "GET":
		return testBulk("demo")
	case "SET":
		return "+OK\r\n"
	case "DEL", "EXPIRE", "PERSIST":
		return ":1\r\n"
	}
	return "-ERR unknown command\r\n"
}

// ---------- 测试夹具 ----------

type testEnv struct {
	deps   *Deps
	router http.Handler
	token  string
	hostID uint
}

func newTestEnv(t *testing.T, redisAddr string) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	// Windows 下不关连接会导致 t.TempDir 清理时删不掉 db 文件
	t.Cleanup(func() {
		if sqlDB, err := st.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	cfg := &config.Config{Mode: "server", DataDir: dir, FileRoot: dir, JWTSecret: "test-secret-key", TokenHours: 1}
	d := &Deps{Cfg: cfg, Store: st, Auth: auth.New(st, cfg.JWTSecret, cfg.TokenTTL()), Version: "test"}

	reg, err := fleet.New(st, cfg.SecretKey())
	if err != nil {
		t.Fatalf("初始化主机注册表失败：%v", err)
	}
	d.Fleet = reg
	local := reg.Local()
	if local == nil {
		t.Fatal("本机记录未创建")
	}

	u, err := d.Auth.Bootstrap("admin", "admin12345")
	if err != nil {
		t.Fatalf("创建账号失败：%v", err)
	}
	token, err := d.Auth.Issue(u)
	if err != nil {
		t.Fatalf("签发令牌失败：%v", err)
	}

	if redisAddr != "" {
		if err := st.DB.Create(&store.RedisConf{HostID: local.ID, Addr: redisAddr, DB: 0}).Error; err != nil {
			t.Fatalf("写入 Redis 连接失败：%v", err)
		}
	}

	r := gin.New()
	g := r.Group("/api")
	// 不挂 HostProxy：这里只验证「按主机注入连接参数」这一段，
	// 反向代理在真实路由里由 fleet 提供，httptest 的 ResponseRecorder 也不支持
	// ReverseProxy 需要的 CloseNotifier。
	g.Use(middleware.Auth(d.Auth), middleware.RedisConn(reg, st, cfg.SecretKey()))
	{
		g.GET("/redis/status", d.redisStatus)
		g.GET("/redis/keys", d.redisKeys)
		g.GET("/redis/key", d.redisKeyDetail)
		g.DELETE("/redis/key", d.redisDeleteKey)
		g.POST("/redis/command", d.redisCommand)
	}

	return &testEnv{deps: d, router: r, token: token, hostID: local.ID}
}

func (e *testEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)

	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// ---------- 用例 ----------

func TestRedisStatusAndKeys(t *testing.T) {
	fake := startTestRedis(t)
	env := newTestEnv(t, fake.addr())

	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/status?host=%d", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("status 返回 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data == nil || data["online"] != true {
		t.Fatalf("连接状态异常：%v", body)
	}
	if data["version"] != "7.2.4" || data["role"] != "master" {
		t.Fatalf("服务端信息未解析：%v", data)
	}
	if keys, _ := data["keys"].(float64); keys != 2 {
		t.Fatalf("当前库键数应为 2，实际 %v", data["keys"])
	}

	code, body = env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/keys?host=%d&pattern=*&count=50", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("keys 返回 %d：%v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("键列表应为 2 条，实际 %v", data)
	}
	first, _ := items[0].(map[string]any)
	if first["key"] != "user:1" || first["type"] != "string" || first["size"] != float64(64) {
		t.Fatalf("键元信息异常：%v", first)
	}

	// 类型过滤
	code, body = env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/keys?host=%d&type=hash", env.hostID), "")
	data, _ = body["data"].(map[string]any)
	if items, _ := data["items"].([]any); len(items) != 1 {
		t.Fatalf("类型过滤结果异常：%v", data)
	}
}

func TestRedisKeyDetailAndCommand(t *testing.T) {
	fake := startTestRedis(t)
	env := newTestEnv(t, fake.addr())

	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/key?host=%d&key=user:1", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("key 详情返回 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	if data["type"] != "string" || data["preview"] != "demo" {
		t.Fatalf("键详情异常：%v", data)
	}

	code, body = env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/key?host=%d&key=cfg:app", env.hostID), "")
	data, _ = body["data"].(map[string]any)
	preview, _ := data["preview"].([]any)
	if len(preview) != 1 {
		t.Fatalf("hash 预览异常：%v", data["preview"])
	}

	// 命令台：正常命令
	code, body = env.do(t, http.MethodPost, fmt.Sprintf("/api/redis/command?host=%d", env.hostID),
		`{"command":"GET user:1"}`)
	if code != http.StatusOK {
		t.Fatalf("命令执行返回 %d：%v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	if data["ok"] != true || data["output"] != "demo" {
		t.Fatalf("命令输出异常：%v", data)
	}

	// 命令台：危险命令必须被拦下
	code, body = env.do(t, http.MethodPost, fmt.Sprintf("/api/redis/command?host=%d", env.hostID),
		`{"command":"FLUSHALL"}`)
	if code != http.StatusForbidden {
		t.Fatalf("FLUSHALL 应被拒绝，实际 %d：%v", code, body)
	}
	code, body = env.do(t, http.MethodPost, fmt.Sprintf("/api/redis/command?host=%d", env.hostID),
		`{"command":"config set maxmemory 0"}`)
	if code != http.StatusForbidden {
		t.Fatalf("CONFIG SET 应被拒绝，实际 %d：%v", code, body)
	}
}

func TestRedisWithoutConnConfig(t *testing.T) {
	env := newTestEnv(t, "")

	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/status?host=%d", env.hostID), "")
	if code != http.StatusPreconditionFailed {
		t.Fatalf("未配置时应返回 412，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "尚未配置") {
		t.Fatalf("提示信息不友好：%v", body)
	}

	// 伪造的请求头必须被中间件清掉，不能连到客户端指定的地址
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/redis/status?host=%d", env.hostID), nil)
	req.Header.Set("Authorization", "Bearer "+env.token)
	req.Header.Set("X-Warden-Redis-Addr", "10.255.255.1:6379")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("伪造连接头应被清除，实际 %d：%s", w.Code, w.Body.String())
	}
}

func TestRedisConnectionIsHostScoped(t *testing.T) {
	fake := startTestRedis(t)
	env := newTestEnv(t, fake.addr())

	// 另一台主机没有配置 Redis，绝不能复用本机那份连接参数
	other := store.Host{Name: "远端", Kind: "agent", Address: "127.0.0.1:1", Enabled: true}
	if err := env.deps.Store.DB.Create(&other).Error; err != nil {
		t.Fatalf("创建主机失败：%v", err)
	}
	code, body := env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/status?host=%d", other.ID), "")
	if code != http.StatusPreconditionFailed {
		t.Fatalf("未配置的主机应返回 412，实际 %d：%v", code, body)
	}
}

// Java 序列化写出来的键名形如 \xac\xed\x00\x05t\x00\x04name，是二进制字节。
// 这类键名经 JSON 传到前端时，非法 UTF-8 字节会被 encoding/json 替换成 U+FFFD，
// 页面上就是乱码；再拿这个乱码字符串回查，自然报「键不存在」。
// 所以列表/详情必须带上 key_raw（原始字节的 base64），前端回传时用它。
func TestRedisBinaryKeyRoundTrip(t *testing.T) {
	binKey := "\xac\xed\x00\x05t\x00\x04name"
	fake := startTestRedisWithKeys(t, "cfg:app", binKey)
	env := newTestEnv(t, fake.addr())

	code, body := env.do(t, http.MethodGet,
		fmt.Sprintf("/api/redis/keys?host=%d&pattern=*&count=50", env.hostID), "")
	if code != http.StatusOK {
		t.Fatalf("keys 返回 %d：%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("键列表应为 2 条，实际 %v", data)
	}
	var bin map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if raw, _ := m["key_raw"].(string); raw != "" {
			bin = m
			break
		}
	}
	if bin == nil {
		t.Fatalf("二进制键应带回 key_raw：%v", items)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte(binKey))
	if bin["key_raw"] != wantB64 {
		t.Fatalf("key_raw 应为原始字节的 base64，实际 %v", bin["key_raw"])
	}
	disp, _ := bin["key"].(string)
	if !strings.HasPrefix(disp, `\xac\xed\x00\x05t\x00\x04`) {
		t.Fatalf("展示名应转义二进制字节，实际 %q", disp)
	}
	if strings.ContainsRune(disp, '\uFFFD') {
		t.Fatalf("展示名不应含替换字符：%q", disp)
	}

	// 用 key_raw 查详情：必须真的命中那个键
	code, body = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/redis/key?host=%d&key_raw=%s", env.hostID, url.QueryEscape(wantB64)), "")
	if code != http.StatusOK {
		t.Fatalf("按 key_raw 查详情返回 %d：%v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	if data["key_raw"] != wantB64 || data["type"] != "string" {
		t.Fatalf("详情应回带同样的 key_raw：%v", data)
	}

	// 反面用例：只拿转义后的展示名去查（旧前端的做法）必须查不到，
	// 这就是用户看到的「点开显示键不存在」。
	code, body = env.do(t, http.MethodGet,
		fmt.Sprintf("/api/redis/key?host=%d&key=%s", env.hostID, url.QueryEscape(disp)), "")
	if code != http.StatusNotFound {
		t.Fatalf("用转义展示名查键应 404，实际 %d：%v", code, body)
	}

	// 删除同样要按原始字节走
	code, body = env.do(t, http.MethodDelete,
		fmt.Sprintf("/api/redis/key?host=%d&key_raw=%s", env.hostID, url.QueryEscape(wantB64)), "")
	if code != http.StatusOK {
		t.Fatalf("按 key_raw 删除返回 %d：%v", code, body)
	}

	// 非法的 base64 应当明确拒绝，而不是当成键名去查
	code, body = env.do(t, http.MethodGet, fmt.Sprintf("/api/redis/key?host=%d&key_raw=!!!", env.hostID), "")
	if code != http.StatusBadRequest {
		t.Fatalf("非法 key_raw 应 400，实际 %d：%v", code, body)
	}
	if msg, _ := body["msg"].(string); !strings.Contains(msg, "编码") {
		t.Fatalf("提示应说明编码问题：%v", body)
	}
}
