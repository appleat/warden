package monitor

import (
	"testing"
	"time"

	"warden/internal/store"
)

// TestSetEnabledAndView 覆盖「启动 / 停止」的状态语义：
// 停用后 View 固定 paused，但 ViewLive 仍给真实结果（手动检测用）；
// 启用时清掉失败计数并立刻到期。
func TestSetEnabledAndView(t *testing.T) {
	s := &Service{state: map[uint]*state{}}
	m := &store.Monitor{ID: 1, Name: "站点A", Enabled: true}

	if got := s.View(m, 0).Status; got != "unknown" {
		t.Fatalf("初始状态应为 unknown，实际 %q", got)
	}

	// 模拟一次成功检测后的运行时态
	s.state[1] = &state{
		Status: "up", LatencyMs: 42, Fails: 3,
		LastCheck: time.Now(), LastRun: time.Now(),
	}

	// 停止
	m.Enabled = false
	s.SetEnabled(1, false)
	if got := s.View(m, 0).Status; got != "paused" {
		t.Fatalf("停用后 View 应为 paused，实际 %q", got)
	}
	if got := s.ViewLive(m, 0).Status; got != "up" {
		t.Fatalf("停用后 ViewLive 应保留真实状态 up，实际 %q", got)
	}

	// 启动
	m.Enabled = true
	s.SetEnabled(1, true)
	st := s.state[1]
	if st.Status != "unknown" || st.Fails != 0 || !st.LastRun.IsZero() {
		t.Fatalf("启用后应重置状态/失败计数/上次执行时间: %+v", st)
	}

	// 进程重启后内存态为空，停用的任务仍要显示 paused
	delete(s.state, 1)
	m.Enabled = false
	if got := s.View(m, 0).Status; got != "paused" {
		t.Fatalf("无内存态时停用任务仍应显示 paused，实际 %q", got)
	}
}
