package system

import "testing"

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
