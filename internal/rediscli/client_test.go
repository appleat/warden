package rediscli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantTLS bool
		wantDB  int
		wantErr bool
	}{
		{"127.0.0.1", "127.0.0.1:6379", false, -1, false},
		{"127.0.0.1:6380", "127.0.0.1:6380", false, -1, false},
		{"redis://10.0.0.5:6379", "10.0.0.5:6379", false, -1, false},
		{"rediss://redis.prod:6380/0", "redis.prod:6380", true, 0, false},
		{"redis://10.0.0.5:6379/3", "10.0.0.5:6379", false, 3, false},
		{"  redis://user:pass@1.2.3.4:6379  ", "1.2.3.4:6379", false, -1, false},
		{"::1", "[::1]:6379", false, -1, false},
		{"", "", false, -1, true},
		{"host:abc", "", false, -1, true},
		{"host:70000", "", false, -1, true},
		{"redis://host:6379/abc", "", false, -1, true},
	}
	for _, c := range cases {
		got, useTLS, db, err := NormalizeAddr(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q 期望报错，实际得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q 解析失败：%v", c.in, err)
		}
		if got != c.want || useTLS != c.wantTLS || db != c.wantDB {
			t.Fatalf("%q => %q tls=%v db=%d，期望 %q tls=%v db=%d",
				c.in, got, useTLS, db, c.want, c.wantTLS, c.wantDB)
		}
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	h := http.Header{}
	// 密码含中文，验证 base64 编码能安全穿越 HTTP 头部
	cfg := Config{Addr: "10.1.2.3:6379", Username: "运维", Password: "口令-123", DB: 3, TLS: true, Insecure: true}
	SetHeaders(h, cfg)

	got := ConfigFromHeaders(h)
	if got.Addr != cfg.Addr || got.Username != cfg.Username || got.Password != cfg.Password ||
		got.DB != cfg.DB || !got.TLS || !got.Insecure {
		t.Fatalf("头部往返不一致：%+v", got)
	}
	// 伪造头必须能被识别，中间件依赖 HeaderKeys 清空
	if len(HeaderKeys) != 6 {
		t.Fatalf("HeaderKeys 数量应为 6，实际 %d", len(HeaderKeys))
	}
}

func TestParseCommandLine(t *testing.T) {
	args, err := ParseCommandLine(`SET "hello world" 'a b' plain\ value ""`)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	want := []string{"SET", "hello world", "a b", "plain value", ""}
	if len(args) != len(want) {
		t.Fatalf("参数个数 %d，期望 %d（%v）", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("第 %d 个参数为 %v，期望 %q", i, args[i], want[i])
		}
	}
	if _, err := ParseCommandLine(`GET "未闭合`); err == nil {
		t.Fatal("未闭合引号应报错")
	}
	if _, err := ParseCommandLine("   "); err == nil {
		t.Fatal("空命令应报错")
	}
}

func TestParseInfo(t *testing.T) {
	raw := "# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\n\r\n" +
		"# Clients\r\nconnected_clients:3\r\n\r\n" +
		"# Keyspace\r\ndb0:keys=12,expires=2,avg_ttl=0\r\ndb2:keys=5,expires=0,avg_ttl=0\r\n"
	info := ParseInfo(raw)
	if info.Get("REDIS_VERSION") != "7.2.4" {
		t.Fatalf("大小写无关查找失败：%q", info.Get("REDIS_VERSION"))
	}
	if info.GetInt("connected_clients") != 3 {
		t.Fatalf("数字字段解析失败：%d", info.GetInt("connected_clients"))
	}
	dbs := info.Keyspace()
	if len(dbs) != 2 || dbs[0].Index != 0 || dbs[1].Keys != 5 {
		t.Fatalf("keyspace 解析异常：%+v", dbs)
	}
	if info.TotalKeys() != 17 {
		t.Fatalf("总键数应为 17，实际 %d", info.TotalKeys())
	}
}

// ---------- 假 Redis 服务端 ----------

// fakeRedis 是一个只认少量命令的 RESP2 服务端，用来验证客户端编解码，
// 让测试不依赖本机是否装了 Redis。
type fakeRedis struct {
	ln   net.Listener
	mu   sync.Mutex
	cmds [][]string
	// handle 返回原始 RESP 响应文本
	handle func(args []string) string
}

func startFake(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	f := &fakeRedis{ln: ln}
	f.handle = func(args []string) string { return "+OK\r\n" }
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeRedis) addr() string { return f.ln.Addr().String() }

func (f *fakeRedis) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	for {
		args, err := readCommand(r)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, args)
		h := f.handle
		f.mu.Unlock()
		if _, err := w.WriteString(h(args)); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (f *fakeRedis) seen() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.cmds))
	copy(out, f.cmds)
	return out
}

func readCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("期望数组，实际 %q", line)
	}
	n, _ := strconv.Atoi(line[1:])
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		l, err := strconv.Atoi(strings.TrimPrefix(hdr, "$"))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, l+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:l]))
	}
	return args, nil
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }
func array(items ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(items))
	for _, it := range items {
		b.WriteString(bulk(it))
	}
	return b.String()
}

func TestListKeysAndDetail(t *testing.T) {
	f := startFake(t)
	f.handle = func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "AUTH", "SELECT":
			return "+OK\r\n"
		case "PING":
			return "+PONG\r\n"
		case "DBSIZE":
			return ":2\r\n"
		case "SCAN":
			// 第一轮就返回两个键并结束扫描
			return scanReply("0", "user:1", "user:2")
		case "TYPE":
			if args[1] == "user:2" {
				return "+hash\r\n"
			}
			return "+string\r\n"
		case "TTL":
			return ":-1\r\n"
		case "MEMORY":
			return ":42\r\n"
		case "STRLEN":
			return ":11\r\n"
		case "GETRANGE":
			return bulk("hello world")
		case "OBJECT":
			return bulk("embstr")
		case "HLEN":
			return ":2\r\n"
		case "HSCAN":
			return scanReply("0", "name", "warden", "level", "admin")
		}
		return "-ERR unknown command\r\n"
	}

	cl, err := New(Config{Addr: f.addr(), Password: "secret", DB: 2})
	if err != nil {
		t.Fatalf("创建客户端失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := cl.Open(ctx)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	// AUTH + SELECT 必须在业务命令之前完成
	seen := f.seen()
	if len(seen) < 2 || strings.ToUpper(seen[0][0]) != "AUTH" || strings.ToUpper(seen[1][0]) != "SELECT" {
		t.Fatalf("握手顺序异常：%v", seen)
	}
	if seen[1][1] != "2" {
		t.Fatalf("SELECT 的库号应为 2，实际 %q", seen[1][1])
	}

	page, err := ListKeys(ctx, conn, "user:*", "", 50)
	if err != nil {
		t.Fatalf("扫描键失败：%v", err)
	}
	if len(page.Items) != 2 || page.Total != 2 || page.Cursor != "0" {
		t.Fatalf("扫描结果异常：%+v", page)
	}
	if page.Items[0].Type != "string" || page.Items[0].Size != 42 || page.Items[1].Type != "hash" {
		t.Fatalf("键元信息补取失败：%+v", page.Items)
	}

	// 类型过滤在客户端完成
	filtered, err := ListKeys(ctx, conn, "user:*", "hash", 50)
	if err != nil {
		t.Fatalf("类型过滤失败：%v", err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Key != "user:2" {
		t.Fatalf("类型过滤结果异常：%+v", filtered.Items)
	}

	str, err := GetKey(ctx, conn, "user:1")
	if err != nil {
		t.Fatalf("读取字符串键失败：%v", err)
	}
	if str.Preview != "hello world" || str.Length != 11 || str.Encoding != "embstr" || str.Truncated {
		t.Fatalf("字符串键详情异常：%+v", str)
	}

	h, err := GetKey(ctx, conn, "user:2")
	if err != nil {
		t.Fatalf("读取 hash 失败：%v", err)
	}
	pairs, ok := h.Preview.([]Pair)
	if !ok || len(pairs) != 2 || pairs[0].Field != "name" {
		t.Fatalf("hash 预览异常：%+v", h.Preview)
	}
}

// TestListKeysScatteredMatches 复现「有的能查到有的查不到」的 bug：
// 匹配键在键空间里是散落的，第一轮 SCAN 只命中其中一个，若扫描在首轮即停止，
// 后面的匹配键就会漏掉。这里用多轮游标、把匹配键分散在各批次，验证全部被返回。
func TestListKeysScatteredMatches(t *testing.T) {
	f := startFake(t)
	// 模拟一个「非匹配键很多、匹配键稀少且分散」的键空间：
	// 第 1 轮返回 filler:1（不匹配）与 user:a（匹配）；游标继续
	// 第 2 轮只返回 filler:2；第 3 轮返回 user:b（匹配）并结束
	round := 0
	f.handle = func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "AUTH", "SELECT":
			return "+OK\r\n"
		case "PING":
			return "+PONG\r\n"
		case "DBSIZE":
			return ":4\r\n"
		case "SCAN":
			round++
			var batch []string
			switch round {
			case 1:
				batch = []string{"filler:1", "user:a"}
			case 2:
				batch = []string{"filler:2"} // 这一轮没有任何匹配键
			default:
				batch = []string{"user:b"}
			}
			// 模拟 Redis 的 MATCH：只回传匹配 pattern 的键，游标照常推进
			pat := ""
			for i := 0; i+1 < len(args); i++ {
				if strings.EqualFold(args[i], "MATCH") {
					pat = args[i+1]
					break
				}
			}
			matched := make([]string, 0, len(batch))
			for _, k := range batch {
				if globMatch(pat, k) {
					matched = append(matched, k)
				}
			}
			cursor := "0"
			if round < 3 {
				cursor = fmt.Sprintf("%d", round+10)
			}
			return scanReply(cursor, matched...)
		case "TYPE":
			return "+string\r\n"
		case "TTL":
			return ":-1\r\n"
		case "MEMORY":
			return ":10\r\n"
		}
		return "-ERR unknown command\r\n"
	}

	cl, err := New(Config{Addr: f.addr()})
	if err != nil {
		t.Fatalf("创建客户端失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cl.Open(ctx)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	page, err := ListKeys(ctx, conn, "user:*", "", 5000)
	if err != nil {
		t.Fatalf("扫描失败：%v", err)
	}
	got := map[string]bool{}
	for _, it := range page.Items {
		got[it.Key] = true
	}
	// 两个匹配键 user:a / user:b 必须都在，不能只返回第一轮撞见的 user:a
	if !got["user:a"] || !got["user:b"] {
		t.Fatalf("散落的匹配键被漏掉：%+v", page.Items)
	}
	if len(page.Items) != 2 {
		t.Fatalf("匹配数量应为 2，实际 %d：%+v", len(page.Items), page.Items)
	}

	// 子串模糊：不含 glob 元字符时应前后补 *，user:b / user:a 都被 *ser* 命中
	round = 0
	fuzzy, err := ListKeys(ctx, conn, "ser", "", 5000)
	if err != nil {
		t.Fatalf("模糊扫描失败：%v", err)
	}
	if len(fuzzy.Items) != 2 {
		t.Fatalf("子串模糊应命中 2 个，实际 %d：%+v", len(fuzzy.Items), fuzzy.Items)
	}
}

func TestGetKeyMissing(t *testing.T) {	f := startFake(t)
	f.handle = func(args []string) string {
		if strings.EqualFold(args[0], "TYPE") {
			return "+none\r\n"
		}
		return "+OK\r\n"
	}
	cl, err := New(Config{Addr: f.addr()})
	if err != nil {
		t.Fatalf("创建客户端失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cl.Open(ctx)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	if _, err := GetKey(ctx, conn, "ghost"); err != ErrNotExist {
		t.Fatalf("应返回 ErrNotExist，实际 %v", err)
	}
}

// 集合类键的预览结构各不相同：set 是扁平成员，list 是元素列表，zset 是成员+分值。
func TestGetKeyCollectionTypes(t *testing.T) {
	f := startFake(t)
	f.handle = func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "AUTH", "SELECT":
			return "+OK\r\n"
		case "TYPE":
			return "+" + strings.TrimPrefix(args[1], "t:") + "\r\n"
		case "TTL":
			return ":-1\r\n"
		case "MEMORY":
			return ":8\r\n"
		case "OBJECT":
			return bulk("listpack")
		case "SCARD":
			return ":3\r\n"
		case "SSCAN":
			// 扁平成员列表，不能被当成键值对解析
			return scanReply("0", "ops", "db", "cache")
		case "LLEN":
			return ":2\r\n"
		case "LRANGE":
			return array("a", "b")
		case "ZCARD":
			return ":2\r\n"
		case "ZRANGE":
			return array("alice", "100", "bob", "88")
		}
		return "-ERR unknown command\r\n"
	}

	cl, err := New(Config{Addr: f.addr()})
	if err != nil {
		t.Fatalf("创建客户端失败：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cl.Open(ctx)
	if err != nil {
		t.Fatalf("连接失败：%v", err)
	}
	defer conn.Close()

	setKey, err := GetKey(ctx, conn, "t:set")
	if err != nil {
		t.Fatalf("读取 set 失败：%v", err)
	}
	members, ok := setKey.Preview.([]string)
	if !ok || len(members) != 3 || setKey.Length != 3 {
		t.Fatalf("set 预览异常：%+v", setKey)
	}

	listKey, err := GetKey(ctx, conn, "t:list")
	if err != nil {
		t.Fatalf("读取 list 失败：%v", err)
	}
	if items, ok := listKey.Preview.([]string); !ok || len(items) != 2 {
		t.Fatalf("list 预览异常：%+v", listKey)
	}

	zKey, err := GetKey(ctx, conn, "t:zset")
	if err != nil {
		t.Fatalf("读取 zset 失败：%v", err)
	}
	if pairs, ok := zKey.Preview.([]Pair); !ok || len(pairs) != 2 || pairs[0].Value != "100" {
		t.Fatalf("zset 预览异常：%+v", zKey)
	}
}

func TestFormat(t *testing.T) {
	if got := Format([]any{"a", int64(2)}); got != "1) a\n2) (integer) 2" {
		t.Fatalf("数组格式化异常：%q", got)
	}
	if got := Format(nil); got != "(nil)" {
		t.Fatalf("nil 格式化异常：%q", got)
	}
	if got := Format(&ReplyError{Message: "ERR bad"}); got != "(error) ERR bad" {
		t.Fatalf("错误格式化异常：%q", got)
	}
}

// scanReply 拼一个 SCAN/HSCAN 的响应：[游标, [元素...]]
func scanReply(cursor string, items ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*2\r\n")
	b.WriteString(bulk(cursor))
	fmt.Fprintf(&b, "*%d\r\n", len(items))
	for _, it := range items {
		b.WriteString(bulk(it))
	}
	return b.String()
}

// globMatch 是测试用的极简 glob 匹配，仅支持 *（任意序列）与 ?（单字符），
// 足以驱动假 Redis 的 MATCH 行为，复现真实服务端的过滤语义。
func globMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			for len(pattern) > 1 && pattern[1] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 1 {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if globMatch(pattern[1:], s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}
