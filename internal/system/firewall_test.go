package system

import (
	"context"
	"strings"
	"testing"
)

func TestValidateRule(t *testing.T) {
	ok := []Rule{
		{Action: "allow", Port: "80", Proto: "tcp"},
		{Action: "deny", Port: "8000:9000", Proto: "any", From: "10.0.0.0/8"},
		{Action: "allow", Port: "", Proto: "tcp", Comment: "全部端口 放行"},
	}
	for _, r := range ok {
		if err := validateRule(r); err != nil {
			t.Fatalf("规则应通过校验 %+v: %v", r, err)
		}
	}

	bad := []Rule{
		{Action: "drop", Port: "80"},            // 非法动作
		{Action: "allow", Port: "80; rm -rf /"}, // 命令注入
		{Action: "allow", Port: "999999"},       // 端口越界
		{Action: "allow", Port: "80", Proto: "icmp"},
		{Action: "allow", From: "1.2.3.4 && id"}, // 来源注入
		{Action: "allow", Comment: "`rm -rf /`"}, // 备注注入
	}
	for _, r := range bad {
		if err := validateRule(r); err == nil {
			t.Fatalf("规则应被拒绝: %+v", r)
		}
	}
}

func TestRevertGuard(t *testing.T) {
	g := newRevertGuard()
	called := false
	token := g.Schedule(60, []Rule{{Action: "allow", Port: "22"}}, func(r []Rule) error {
		called = true
		return nil
	})
	if token == "" || !g.Pending() {
		t.Fatal("回滚任务未挂起")
	}
	if !g.Confirm(token) {
		t.Fatal("确认应当成功")
	}
	if g.Pending() {
		t.Fatal("确认后不应仍处于挂起状态")
	}
	if g.Confirm("wrong") {
		t.Fatal("错误令牌不应通过")
	}
	if called {
		t.Fatal("已确认的任务不应触发回滚")
	}
}

// ufw 把备注放在行尾 "# xxx"，解析时必须摘掉，
// 否则会被正则最后一个捕获组吞进「来源」，备注列也永远是空的。
func TestUfwListParsesComment(t *testing.T) {
	out := strings.Join([]string{
		"Status: active",
		"",
		"     To                         Action      From",
		"     --                         ------      ----",
		"[ 1] 22/tcp                     ALLOW IN    Anywhere",
		"[ 2] 8080/tcp                   ALLOW IN    Anywhere                   # warden-server",
		"[ 3] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)              # warden-server",
		"[ 4] 3306                       ALLOW       Anywhere                   #  数据库  内网",
	}, "\n")
	fakeRun(t, func(name string, args []string) (string, error) { return out, nil })

	fw := NewFirewall()
	rules, err := fw.ufwListLocked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("应解析出 4 条规则，实际 %d：%+v", len(rules), rules)
	}
	if rules[1].Comment != "warden-server" {
		t.Fatalf("备注没解析出来：%q", rules[1].Comment)
	}
	if rules[1].From != "Anywhere" {
		t.Fatalf("备注被吞进了来源：%q", rules[1].From)
	}
	if rules[1].Port != "8080" || rules[1].Proto != "tcp" || rules[1].Action != "allow" {
		t.Fatalf("端口/协议/动作解析不对：%+v", rules[1])
	}
	if rules[3].Comment != "数据库  内网" {
		t.Fatalf("中文备注解析不对：%q", rules[3].Comment)
	}
	if rules[0].Comment != "" {
		t.Fatalf("没有备注的规则应为空：%q", rules[0].Comment)
	}
}

func TestSplitRuleArgs(t *testing.T) {
	got := splitRuleArgs(`-p tcp --dport 8080 -m comment --comment "my note" -j ACCEPT`)
	want := []string{"-p", "tcp", "--dport", "8080", "-m", "comment", "--comment", "my note", "-j", "ACCEPT"}
	if len(got) != len(want) {
		t.Fatalf("切分结果长度不对：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个参数是 %q，期望 %q（整体 %v）", i, got[i], want[i], got)
		}
	}
	if got := splitRuleArgs("  a  b  "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("多余空白应被忽略：%v", got)
	}
}

// 备注要真的写进 iptables 规则里（-m comment），否则列表里永远拿不到。
func TestIptablesAddCarriesComment(t *testing.T) {
	calls := fakeRun(t, func(name string, args []string) (string, error) { return "", nil })
	fw := NewFirewall()
	if err := fw.iptablesAddLocked(context.Background(), Rule{Action: "allow", Port: "8080", Proto: "tcp", Comment: "web 入口"}); err != nil {
		t.Fatal(err)
	}
	if !hasCall(*calls, `iptables -I INPUT -p tcp --dport 8080 -m comment --comment "web 入口" -j ACCEPT`) {
		t.Fatalf("备注没带上：%v", *calls)
	}

	// 不带备注时不应该出现 -m comment
	calls = fakeRun(t, func(name string, args []string) (string, error) { return "", nil })
	if err := fw.iptablesAddLocked(context.Background(), Rule{Action: "allow", Port: "22", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	if hasCall(*calls, "iptables -I INPUT -p tcp --dport 22 -m comment") {
		t.Fatalf("没填备注却加了 comment 匹配：%v", *calls)
	}
}

// 带空格备注的规则必须能删掉：删之前要把带引号的原始规则正确切开。
func TestIptablesDeleteWithSpacedComment(t *testing.T) {
	list := "-P INPUT ACCEPT\n-A INPUT -p tcp --dport 8080 -m comment --comment \"my note\" -j ACCEPT\n"
	var calls *[]string
	calls = fakeRun(t, func(name string, args []string) (string, error) {
		if len(args) > 0 && args[0] == "-S" {
			return list, nil
		}
		return "", nil
	})
	fw := NewFirewall()
	if err := fw.iptablesDeleteLocked(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(*calls, `iptables -D INPUT -p tcp --dport 8080 -m comment --comment "my note" -j ACCEPT`) {
		t.Fatalf("删除参数不对（备注被切碎？）：%v", *calls)
	}
}

// iptables -S 的备注能被解析回列表。
func TestIptablesListParsesComment(t *testing.T) {
	list := "-A INPUT -p tcp --dport 8080 -m comment --comment \"my note\" -j ACCEPT\n"
	fakeRun(t, func(name string, args []string) (string, error) { return list, nil })
	fw := NewFirewall()
	rules, err := fw.iptablesListLocked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Comment != "my note" {
		t.Fatalf("备注解析失败：%+v", rules)
	}
	if rules[0].Port != "8080" || rules[0].Proto != "tcp" || rules[0].Action != "allow" {
		t.Fatalf("其它字段解析不对：%+v", rules[0])
	}
}
