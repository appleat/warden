package system

import (
	"os"
	"testing"
)

func sampleProcs() []ProcDetail {
	return []ProcDetail{
		{PID: 3, Name: "nginx", CPU: 1.5, MemPct: 9.1, RSS: 300, Threads: 2, UptimeSec: 500, CPUTime: 12},
		{PID: 1, Name: "systemd", CPU: 0.2, MemPct: 0.5, RSS: 100, Threads: 1, UptimeSec: 9000, CPUTime: 30},
		{PID: 2, Name: "java", CPU: 88.0, MemPct: 42.0, RSS: 900, Threads: 64, UptimeSec: 60, CPUTime: 600},
	}
}

func TestSortProcesses(t *testing.T) {
	cases := []struct {
		key  string
		desc bool
		want int32 // 期望的第一个 PID
	}{
		{"cpu", true, 2},
		{"cpu", false, 1},
		{"mem", true, 2},
		{"rss", false, 1},
		{"pid", true, 3},
		{"pid", false, 1},
		{"name", false, 2}, // java < nginx < systemd
		{"threads", true, 2},
		{"uptime", true, 1},
		{"cpu_time", false, 3},
		{"不存在的字段", true, 2}, // 非法字段回落到 cpu 降序
	}
	for _, c := range cases {
		list := sampleProcs()
		sortProcesses(list, c.key, c.desc)
		if list[0].PID != c.want {
			t.Fatalf("key=%s desc=%v 首位应是 PID %d，实际 %d（顺序 %v）",
				c.key, c.desc, c.want, list[0].PID, pids(list))
		}
	}
}

func pids(list []ProcDetail) []int32 {
	out := make([]int32, len(list))
	for i, p := range list {
		out[i] = p.PID
	}
	return out
}

func TestListProcessesSelf(t *testing.T) {
	items, total, err := ListProcesses("cpu", true, "", 100)
	if err != nil {
		t.Fatalf("采样失败：%v", err)
	}
	if total == 0 || len(items) == 0 {
		t.Fatal("没有采集到任何进程")
	}
	if len(items) > 100 {
		t.Fatalf("limit 未生效，返回了 %d 条", len(items))
	}

	self := int32(os.Getpid())
	var found *ProcDetail
	for i := range items {
		if items[i].PID == self {
			found = &items[i]
			break
		}
	}
	// 测试进程按 CPU 排序未必进前 100，进不去就用 q 精确再查一次
	if found == nil {
		items, _, _ = ListProcesses("pid", false, "test", 100)
		for i := range items {
			if items[i].PID == self {
				found = &items[i]
				break
			}
		}
	}
	if found == nil {
		t.Skip("当前进程未出现在列表中，跳过字段校验")
	}
	if !found.Self {
		t.Fatal("自身进程的 self 标记应为 true")
	}
	if found.Name == "" {
		t.Fatal("进程名不应为空")
	}
	if found.Threads <= 0 {
		t.Fatalf("线程数异常：%d", found.Threads)
	}
	if found.RSS == 0 {
		t.Fatal("常驻内存不应为 0")
	}
	if found.CPUTime <= 0 {
		t.Fatalf("累计 CPU 时间异常：%v", found.CPUTime)
	}
}

func TestListProcessesFilter(t *testing.T) {
	// 用一个不可能匹配的关键字，验证过滤在截断前生效且总数随之变化
	items, total, err := ListProcesses("cpu", true, "___no_such_process_name___", 100)
	if err != nil {
		t.Fatalf("采样失败：%v", err)
	}
	if len(items) != 0 || total != 0 {
		t.Fatalf("过滤应无结果，实际 items=%d total=%d", len(items), total)
	}
}

func TestKillProcessGuards(t *testing.T) {
	if err := KillProcess(0, true); err == nil {
		t.Fatal("PID 0 应被拒绝")
	}
	if err := KillProcess(1, true); err == nil {
		t.Fatal("PID 1 应被拒绝")
	}
	if err := KillProcess(int32(os.Getpid()), true); err == nil {
		t.Fatal("自身进程应被拒绝")
	}
	// 一个几乎不可能存在的 PID，应返回「不存在」而不是 panic
	if err := KillProcess(1<<30, false); err == nil {
		t.Fatal("不存在的进程应报错")
	}
}
