package docker

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// 第一帧：precpu 为空（容器刚被采样），只能靠第二帧自己算增量
const statsFrameA = `{
  "cpu_stats": {"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 100000, "online_cpus": 4},
  "precpu_stats": {"cpu_usage": {"total_usage": 0}, "system_cpu_usage": 0},
  "memory_stats": {"usage": 104857600, "limit": 1073741824, "stats": {"inactive_file": 10485760}},
  "networks": {"eth0": {"rx_bytes": 1024, "tx_bytes": 2048}},
  "blkio_stats": {"io_service_bytes_recursive": [{"op": "read", "value": 4096}, {"op": "Write", "value": 8192}]},
  "pids_stats": {"current": 12}
}`

const statsFrameB = `{
  "cpu_stats": {"cpu_usage": {"total_usage": 3000}, "system_cpu_usage": 200000, "online_cpus": 4},
  "precpu_stats": {"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 100000},
  "memory_stats": {"usage": 104857600, "limit": 1073741824, "stats": {"inactive_file": 10485760}},
  "networks": {"eth0": {"rx_bytes": 2048, "tx_bytes": 4096}},
  "blkio_stats": {"io_service_bytes_recursive": [{"op": "read", "value": 4096}, {"op": "Write", "value": 8192}]},
  "pids_stats": {"current": 12}
}`

func TestReadStatsUsesPreCPU(t *testing.T) {
	st, err := readStats(strings.NewReader(statsFrameB))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if st.CPUPercent != 8 {
		t.Fatalf("CPU 占用应为 8%%，实际 %v", st.CPUPercent)
	}
	if st.PIDs != 12 {
		t.Fatalf("PIDs 解析错误: %v", st.PIDs)
	}
}

// 只有第一帧可用时（precpu 为 0）必须继续读第二帧，否则 CPU 永远是 0
func TestReadStatsFallsBackToSecondSample(t *testing.T) {
	st, err := readStats(strings.NewReader(statsFrameA + "\n" + statsFrameB))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if st.CPUPercent != 8 {
		t.Fatalf("CPU 占用应为 8%%，实际 %v", st.CPUPercent)
	}
	// 内存、网络取最后一帧
	if st.NetRx != 2048 || st.NetTx != 4096 {
		t.Fatalf("网络统计应取最后一帧，实际 rx=%d tx=%d", st.NetRx, st.NetTx)
	}
}

func TestStatFromSampleMemoryAndIO(t *testing.T) {
	var raw statsSample
	if err := json.Unmarshal([]byte(statsFrameA), &raw); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	st := statFromSample(raw)
	// cgroup 内存要扣掉 inactive_file，否则显示偏高
	if want := uint64(104857600 - 10485760); st.MemUsage != want {
		t.Fatalf("内存占用应为 %d，实际 %d", want, st.MemUsage)
	}
	if st.MemLimit != 1073741824 {
		t.Fatalf("内存上限解析错误: %d", st.MemLimit)
	}
	if st.MemPercent != 8.79 {
		t.Fatalf("内存占比应为 8.79%%，实际 %v", st.MemPercent)
	}
	if st.BlkRead != 4096 || st.BlkWrite != 8192 {
		t.Fatalf("块设备读写解析错误: read=%d write=%d", st.BlkRead, st.BlkWrite)
	}
}

func TestCalcCPUPercent(t *testing.T) {
	cases := []struct {
		name                               string
		curTotal, curSys, preTotal, preSys uint64
		cpus                               uint32
		want                               float64
	}{
		{"单核跑满", 1000, 1000, 0, 0, 1, 100},
		{"四核跑满两核", 2000, 4000, 0, 0, 4, 200},
		{"空闲", 100, 100000, 100, 100000, 4, 0},
		{"系统时间未推进", 100, 1000, 50, 1000, 2, 0},
		{"online_cpus 缺失时回退", 1000, 1000, 0, 0, 0, 100 * float64(runtime.NumCPU())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := calcCPUPercent(c.curTotal, c.curSys, c.preTotal, c.preSys, c.cpus); got != c.want {
				t.Fatalf("期望 %v，实际 %v", c.want, got)
			}
		})
	}
}

// 容器未运行时 Docker 只返回 name/id 的空帧，必须报错而不是当成 0 值成功，
// 否则前端会一直显示 0.0%（之前的假象就来自这里）。
func TestReadStatsRejectsEmptyFrames(t *testing.T) {
	const empty = `{"name":"/demo","id":"abc","read":"2026-10-26T01:00:00Z","memory_stats":{},"cpu_stats":{"cpu_usage":{"total_usage":0},"system_cpu_usage":0}}`
	if _, err := readStats(strings.NewReader(empty + "\n" + empty)); err == nil {
		t.Fatal("空帧应当返回错误")
	}
}

// 只拿到一帧且没有 precpu 时，内存等瞬时值仍然可用，CPU 增量标记为无效。
func TestReadStatsSingleFrameKeepsMemory(t *testing.T) {
	st, err := readStats(strings.NewReader(statsFrameA))
	if err != nil {
		t.Fatalf("单帧不应报错: %v", err)
	}
	if st.MemUsage == 0 {
		t.Fatal("单帧也应带回内存占用")
	}
	if st.CPUAvailable() {
		t.Fatal("只有一帧时 CPU 增量不可用，不应标记为有效")
	}
}

// 系统 CPU 时间没有推进（同一时刻的两帧）说明增量无意义，不能当作有效样本。
func TestReadStatsIgnoresStalledSystemClock(t *testing.T) {
	const stalled = `{
	  "cpu_stats": {"cpu_usage": {"total_usage": 5000}, "system_cpu_usage": 200000, "online_cpus": 4},
	  "precpu_stats": {"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 200000},
	  "memory_stats": {"usage": 20971520, "limit": 1073741824, "stats": {}}
	}`
	st, err := readStats(strings.NewReader(stalled))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if st.CPUAvailable() || st.CPUPercent != 0 {
		t.Fatalf("系统时间未推进时不应给出 CPU 值，实际 %v", st.CPUPercent)
	}
	if st.MemUsage != 20971520 {
		t.Fatalf("内存占用应保留，实际 %d", st.MemUsage)
	}
}

// 同一端口在 IPv4 / IPv6 上会各报一条，列表里必须合并成一条，
// 否则端口列会重复显示（叠成十几行）。
func TestDedupePorts(t *testing.T) {
	src := []container.Port{
		{IP: "0.0.0.0", PrivatePort: 9200, PublicPort: 9200, Type: "tcp"},
		{IP: "::", PrivatePort: 9200, PublicPort: 9200, Type: "tcp"},
		{IP: "0.0.0.0", PrivatePort: 9300, PublicPort: 9300, Type: "tcp"},
		{IP: "::", PrivatePort: 9300, PublicPort: 9300, Type: "tcp"},
		{PrivatePort: 9600, Type: "udp"}, // 未发布的端口
	}
	got := dedupePorts(src)
	if len(got) != 3 {
		t.Fatalf("应合并为 3 条端口，实际 %d 条: %+v", len(got), got)
	}
	if got[0].PublicPort != 9200 || got[1].PublicPort != 9300 {
		t.Fatalf("端口顺序或数值不对: %+v", got)
	}
	if got[2].PublicPort != 0 || got[2].PrivatePort != 9600 {
		t.Fatalf("未发布端口解析错误: %+v", got[2])
	}
}
