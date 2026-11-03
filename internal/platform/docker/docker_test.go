package docker

import (
	"context"
	"encoding/json"
	"io/fs"
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

// ---------- 跨平台端点与提示 ----------

// withGOOS 临时切换平台判断，让 Windows / Linux 两条分支都能被覆盖。
func withGOOS(t *testing.T, v string) {
	t.Helper()
	old := goos
	goos = v
	t.Cleanup(func() { goos = old })
}

// 没配 docker_host 时，端点必须跟着平台走：Windows 是 npipe，Linux/WSL 是 unix socket。
func TestDefaultEndpointFollowsPlatform(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	withGOOS(t, "windows")
	if got := defaultEndpoint(); got != "npipe:////./pipe/docker_engine" {
		t.Fatalf("Windows 默认端点应为 npipe，实际 %q", got)
	}
	withGOOS(t, "linux")
	if got := defaultEndpoint(); got != "unix:///var/run/docker.sock" {
		t.Fatalf("Linux 默认端点应为 unix socket，实际 %q", got)
	}
}

// DOCKER_HOST 环境变量的优先级高于平台默认值（Docker Desktop 与远程 daemon 都靠它）。
func TestDefaultEndpointRespectsEnv(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.9:2375")
	withGOOS(t, "windows")
	if got := defaultEndpoint(); got != "tcp://10.0.0.9:2375" {
		t.Fatalf("应优先使用 DOCKER_HOST，实际 %q", got)
	}
}

func TestEndpointPrefersConfiguredHost(t *testing.T) {
	s := New("  tcp://10.0.0.5:2375  ")
	if got := s.Endpoint(); got != "tcp://10.0.0.5:2375" {
		t.Fatalf("应使用配置的 docker_host 且去掉首尾空格，实际 %q", got)
	}
}

// 提示语不能写死 Linux：Windows 上提 systemctl 只会误导人。
func TestUnreachableHintMentionsPlatformTool(t *testing.T) {
	withGOOS(t, "windows")
	h := unreachableHint()
	if !strings.Contains(h, "Docker Desktop") || strings.Contains(h, "systemctl") {
		t.Fatalf("Windows 提示应提 Docker Desktop 且不提 systemctl，实际 %q", h)
	}
	withGOOS(t, "linux")
	h = unreachableHint()
	if !strings.Contains(h, "systemctl") || strings.Contains(h, "Docker Desktop") {
		t.Fatalf("Linux 提示应提 systemctl，实际 %q", h)
	}
}

// Windows 的 Go 运行时没有 unix socket，配置成 /var/run/docker.sock 一定连不上，
// 必须提前拦下并指向 npipe，而不是等 dial 报一句 no such file。
func TestClientRejectsUnixSocketOnWindows(t *testing.T) {
	withGOOS(t, "windows")
	s := New("unix:///var/run/docker.sock")
	_, err := s.Client()
	if err == nil {
		t.Fatal("Windows 上配置 unix socket 应当直接报错")
	}
	if !strings.Contains(err.Error(), "unix socket") {
		t.Fatalf("错误应说清是 unix socket 不可用，实际 %q", err.Error())
	}
	// 替代方案放在 hint 里，不和错误文案重复
	if h := s.lastHint(); !strings.Contains(h, "npipe") {
		t.Fatalf("提示里应给出 npipe 的替代方案，实际 %q", h)
	}
}

// Error 说现象、Hint 说怎么做，两者不能是同一句话，否则界面上会出现两段重复文字。
func TestStatusSeparatesErrorAndHint(t *testing.T) {
	withGOOS(t, "windows")
	st := New("unix:///var/run/docker.sock").Status(context.Background())
	if st.Error == "" || st.Hint == "" {
		t.Fatalf("错误与提示都应有内容，实际 error=%q hint=%q", st.Error, st.Hint)
	}
	if st.Error == st.Hint {
		t.Fatalf("错误与提示不应重复：%q", st.Error)
	}
	if st.Platform != "windows" {
		t.Fatalf("应带回平台标识，实际 %q", st.Platform)
	}
	if st.ServerEnd != "unix:///var/run/docker.sock" {
		t.Fatalf("连不上时也应显示尝试过的端点，实际 %q", st.ServerEnd)
	}
}

// compose 文件在 WSL 虚拟机内时，Windows 侧读不到，提示必须说清原因而不是裸 os 错误。
func TestComposePathErrExplainsWSLPath(t *testing.T) {
	withGOOS(t, "windows")
	err := composePathErr("读取 compose 文件", "/home/deploy/stack/docker-compose.yml", fs.ErrNotExist)
	msg := err.Error()
	if !strings.Contains(msg, "WSL") || !strings.Contains(msg, "/home/deploy/stack/docker-compose.yml") {
		t.Fatalf("Windows 下应说明是 WSL 内部路径并带上原路径，实际 %q", msg)
	}
	withGOOS(t, "linux")
	err = composePathErr("读取 compose 文件", "/home/deploy/stack/docker-compose.yml", fs.ErrNotExist)
	msg = err.Error()
	if strings.Contains(msg, "WSL") {
		t.Fatalf("Linux 下不该提 WSL，实际 %q", msg)
	}
	if !strings.Contains(msg, "不存在") {
		t.Fatalf("Linux 下应说明文件不存在，实际 %q", msg)
	}
}

// 非「文件不存在」的错误（权限、IO）不能被改写成「路径读不到」，否则会掩盖真实原因。
func TestComposePathErrKeepsOtherErrors(t *testing.T) {
	withGOOS(t, "windows")
	err := composePathErr("写入 compose 文件", "/home/x/docker-compose.yml", fs.ErrPermission)
	msg := err.Error()
	if !strings.Contains(msg, "permission denied") {
		t.Fatalf("应保留原始错误，实际 %q", msg)
	}
	if strings.Contains(msg, "WSL") {
		t.Fatalf("权限错误不该被说成路径不可达，实际 %q", msg)
	}
}
