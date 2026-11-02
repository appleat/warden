package system

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestListenPort(t *testing.T) {
	cases := []struct {
		addr string
		want int
		ok   bool
	}{
		{":8080", 8080, true},
		{":3001", 3001, true},
		{"0.0.0.0:3001", 3001, true},
		{"127.0.0.1:8080", 8080, true},
		{"[::]:8090", 8090, true},
		{"[::1]:8090", 8090, true},
		{"  10.0.0.1:80  ", 80, true},
		{"", 0, false},
		{"8080", 0, false},     // 缺主机冒号
		{":0", 0, false},       // 端口越界
		{":70000", 0, false},   // 端口越界
		{"host:", 0, false},    // 空端口
	}
	for _, c := range cases {
		got, err := ListenPort(c.addr)
		if c.ok {
			if err != nil {
				t.Fatalf("ListenPort(%q) 期望成功，实际 %v", c.addr, err)
			}
			if got != c.want {
				t.Fatalf("ListenPort(%q) = %d，期望 %d", c.addr, got, c.want)
			}
			continue
		}
		if err == nil {
			t.Fatalf("ListenPort(%q) 期望失败，实际返回 %d", c.addr, got)
		}
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", false}, // 空 host = 监听所有网卡
		{"0.0.0.0:8080", false},
		{"[::]:8080", false},
		{"10.0.0.1:8080", false},
		{"127.0.0.1:8080", true},
		{"127.0.0.5:8080", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{"LocalHost:8080", true},
		{"garbage", false},
	}
	for _, c := range cases {
		if got := IsLoopbackAddr(c.addr); got != c.want {
			t.Fatalf("IsLoopbackAddr(%q) = %v，期望 %v", c.addr, got, c.want)
		}
	}
}

func TestUfwPortAllowed(t *testing.T) {
	rules := []Rule{
		{Action: "allow", Port: "22", Proto: "tcp", From: "Anywhere"},
		{Action: "allow", Port: "8080", Proto: "tcp", From: "10.0.0.0/8"}, // 限来源不算
		{Action: "deny", Port: "8090", Proto: "tcp", From: "Anywhere"},   // deny 不算
		{Action: "allow", Port: "3306", Proto: "any", From: "Anywhere"},
	}
	if ufwPortAllowed(rules, 8080) {
		t.Fatal("限来源的规则不应被当成已放行")
	}
	if ufwPortAllowed(rules, 8090) {
		t.Fatal("deny 规则不应被当成已放行")
	}
	if !ufwPortAllowed(rules, 3306) {
		t.Fatal("proto=any 的全网放行应被识别")
	}
	if !ufwPortAllowed(rules, 22) {
		t.Fatal("已有 tcp 放行应被识别")
	}
	if ufwPortAllowed(rules, 9000) {
		t.Fatal("没有该端口的规则不应被识别为已放行")
	}
	// IPv6 展示形态
	if !ufwPortAllowed([]Rule{{Action: "allow", Port: "8443", Proto: "tcp", From: "Anywhere (v6)"}}, 8443) {
		t.Fatal("IPv6 的 Anywhere 规则应被识别")
	}
}

// 只监听回环时不该动防火墙，且结果要能通过 Status 回到界面。
func TestEnsurePortOpenLoopbackSkipped(t *testing.T) {
	fw := NewFirewall()
	res := fw.EnsurePortOpen(context.Background(), "127.0.0.1:3001", "warden-server")
	if res.Status != "skipped" {
		t.Fatalf("期望 skipped，实际 %s（%s）", res.Status, res.Message)
	}
	if res.Port != 3001 {
		t.Fatalf("端口应为 3001，实际 %d", res.Port)
	}
	if !strings.Contains(res.Message, "回环") {
		t.Fatalf("提示应说明只监听回环，实际 %q", res.Message)
	}
	if fw.autoOpen == nil {
		t.Fatal("结果应记录在 Firewall 上")
	}

	st := fw.Status(context.Background())
	if st.AutoOpen == nil || st.AutoOpen.Port != 3001 || st.AutoOpen.Status != "skipped" {
		t.Fatalf("Status 未带出自动放行结果：%+v", st.AutoOpen)
	}
}

func TestEnsurePortOpenBadAddr(t *testing.T) {
	fw := NewFirewall()
	res := fw.EnsurePortOpen(context.Background(), "8080", "")
	if res.Status != "skipped" {
		t.Fatalf("期望 skipped，实际 %s", res.Status)
	}
	if !strings.Contains(res.Message, "解析失败") {
		t.Fatalf("提示应说明地址解析失败，实际 %q", res.Message)
	}
}

// ---------- 各防火墙驱动的「已存在就跳过 / 不存在才加」 ----------

// fakeRun 替换包内的命令出口，返回记录下来的调用列表。
func fakeRun(t *testing.T, handle func(name string, args []string) (string, error)) *[]string {
	t.Helper()
	calls := &[]string{}
	orig := runCmd
	runCmd = func(ctx context.Context, name string, args ...string) (string, error) {
		*calls = append(*calls, name+" "+joinArgs(args))
		return handle(name, args)
	}
	t.Cleanup(func() { runCmd = orig })
	return calls
}

// joinArgs 给含空格的参数补上引号，这样断言里能看出「一个参数」还是「两个参数」。
func joinArgs(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			parts[i] = `"` + a + `"`
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

func hasCall(calls []string, prefix string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func TestUfwEnsurePortAddsAndSkips(t *testing.T) {
	fw := NewFirewall()
	ctx := context.Background()

	// 1) 没有规则 -> 添加
	calls := fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "status" && args[1] == "numbered" {
			return "Status: active\n", nil
		}
		if len(args) == 1 && args[0] == "status" {
			return "Status: active\n", nil
		}
		return "", nil
	})
	res := &AutoOpen{Proto: "tcp", Port: 3001}
	fw.ufwEnsurePortLocked(ctx, res, 3001, "warden-server")
	if res.Status != "opened" {
		t.Fatalf("期望 opened，实际 %s（%s）", res.Status, res.Message)
	}
	if !hasCall(*calls, "ufw allow 3001/tcp comment warden-server") {
		t.Fatalf("应调用 ufw allow，实际调用：%v", *calls)
	}

	// 2) 已有放行规则 -> 跳过，不再重复添加
	calls = fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "status" && args[1] == "numbered" {
			return "Status: active\n\n[ 1] 3001/tcp                   ALLOW       Anywhere\n[ 2] 3001/tcp (v6)              ALLOW       Anywhere (v6)\n", nil
		}
		return "Status: active\n", nil
	})
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.ufwEnsurePortLocked(ctx, res, 3001, "warden-server")
	if res.Status != "exists" {
		t.Fatalf("已有规则时应为 exists，实际 %s", res.Status)
	}
	if hasCall(*calls, "ufw allow") {
		t.Fatalf("已有规则时不应再添加：%v", *calls)
	}

	// 3) ufw 未启用时仍写规则，但提示要说明生效时机
	calls = fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) >= 2 && args[0] == "status" && args[1] == "numbered" {
			return "Status: inactive\n", nil
		}
		if len(args) == 1 && args[0] == "status" {
			return "Status: inactive\n", nil
		}
		return "", nil
	})
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.ufwEnsurePortLocked(ctx, res, 3001, "")
	if res.Status != "opened" || !strings.Contains(res.Message, "未启用") {
		t.Fatalf("未启用时提示不对：%s / %s", res.Status, res.Message)
	}
	_ = calls
}

func TestFirewalldEnsurePortAddsAndSkips(t *testing.T) {
	fw := NewFirewall()
	ctx := context.Background()

	// 1) 运行时与永久都没有 -> 两处都加，且不 reload
	calls := fakeRun(t, func(name string, args []string) (string, error) {
		for _, a := range args {
			if strings.HasPrefix(a, "--query-port=") {
				return "", errors.New("not enabled")
			}
		}
		return "", nil
	})
	res := &AutoOpen{Proto: "tcp", Port: 3001}
	fw.firewalldEnsurePortLocked(ctx, res, 3001)
	if res.Status != "opened" {
		t.Fatalf("期望 opened，实际 %s（%s）", res.Status, res.Message)
	}
	if !hasCall(*calls, "firewall-cmd --add-port=3001/tcp") {
		t.Fatalf("应添加运行时规则：%v", *calls)
	}
	if !hasCall(*calls, "firewall-cmd --permanent --add-port=3001/tcp") {
		t.Fatalf("应添加永久规则：%v", *calls)
	}
	if hasCall(*calls, "firewall-cmd --reload") {
		t.Fatalf("不应 reload（会冲掉别人的运行时规则）：%v", *calls)
	}

	// 2) 两处都已存在 -> 跳过
	calls = fakeRun(t, func(name string, args []string) (string, error) { return "yes", nil })
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.firewalldEnsurePortLocked(ctx, res, 3001)
	if res.Status != "exists" {
		t.Fatalf("期望 exists，实际 %s", res.Status)
	}
	if hasCall(*calls, "firewall-cmd --add-port") || hasCall(*calls, "firewall-cmd --permanent --add-port") {
		t.Fatalf("已存在时不应添加：%v", *calls)
	}

	// 3) 只有永久配置写入成功 -> 提示 firewalld 启动后生效
	fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "--permanent" {
			for _, a := range args {
				if strings.HasPrefix(a, "--query-port=") {
					return "", errors.New("absent")
				}
			}
			return "", nil
		}
		for _, a := range args {
			if strings.HasPrefix(a, "--query-port=") {
				return "yes", nil
			}
		}
		return "", errors.New("firewalld is not running")
	})
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.firewalldEnsurePortLocked(ctx, res, 3001)
	if res.Status != "opened" || !strings.Contains(res.Message, "永久") {
		t.Fatalf("永久配置场景提示不对：%s / %s", res.Status, res.Message)
	}
}

func TestIptablesEnsurePortAddsAndSkips(t *testing.T) {
	fw := NewFirewall()
	ctx := context.Background()

	// 1) -C 检查失败 -> 插入规则
	calls := fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "-C" {
			return "", errors.New("Bad rule (does a matching rule exist in that chain?)")
		}
		return "", nil
	})
	res := &AutoOpen{Proto: "tcp", Port: 3001}
	fw.iptablesEnsurePortLocked(ctx, res, 3001)
	if res.Status != "opened" {
		t.Fatalf("期望 opened，实际 %s（%s）", res.Status, res.Message)
	}
	if !hasCall(*calls, "iptables -I INPUT -p tcp --dport 3001 -j ACCEPT") {
		t.Fatalf("应插入放行规则：%v", *calls)
	}
	if !strings.Contains(res.Message, "iptables-persistent") {
		t.Fatalf("应提示规则不持久：%s", res.Message)
	}

	// 2) -C 命中 -> 跳过
	calls = fakeRun(t, func(name string, args []string) (string, error) { return "", nil })
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.iptablesEnsurePortLocked(ctx, res, 3001)
	if res.Status != "exists" {
		t.Fatalf("期望 exists，实际 %s", res.Status)
	}
	if hasCall(*calls, "iptables -I") {
		t.Fatalf("已存在时不应插入：%v", *calls)
	}

	// 3) 插入失败 -> failed
	fakeRun(t, func(name string, args []string) (string, error) {
		return "", errors.New("Permission denied (you must be root)")
	})
	res = &AutoOpen{Proto: "tcp", Port: 3001}
	fw.iptablesEnsurePortLocked(ctx, res, 3001)
	if res.Status != "failed" || !strings.Contains(res.Message, "Permission denied") {
		t.Fatalf("失败场景不对：%s / %s", res.Status, res.Message)
	}
}
