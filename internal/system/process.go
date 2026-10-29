package system

import (
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// ProcDetail 是进程管理页面展示的单条进程信息。
// CPU 为「单核口径」百分比：100 表示占满一个核心，多核进程可超过 100（与 top 一致）。
type ProcDetail struct {
	PID       int32   `json:"pid"`
	PPID      int32   `json:"ppid"`
	Name      string  `json:"name"`
	User      string  `json:"user"`
	Status    string  `json:"status"`
	CPU       float64 `json:"cpu"`
	CPUTime   float64 `json:"cpu_time"` // 累计 CPU 秒
	MemPct    float64 `json:"mem_pct"`
	RSS       uint64  `json:"rss"`
	VMS       uint64  `json:"vms"`
	Threads   int32   `json:"threads"`
	StartAt   int64   `json:"start_at"`   // 启动时间（毫秒时间戳）
	UptimeSec int64   `json:"uptime_sec"` // 已运行秒数
	Cmdline   string  `json:"cmdline"`
	Exe       string  `json:"exe"`
	Self      bool    `json:"self"` // 是否为 Warden 自身进程
}

// ProcSampleInterval 是两次 CPU 时间采样的间隔。
// gopsutil 的 Process.CPUPercent 在 Linux 上返回的是「自进程启动以来的平均值」，
// 对长期运行的进程完全没有参考意义，所以这里自己采两次 /proc/<pid>/stat 求差值。
const ProcSampleInterval = 300 * time.Millisecond

// procSortKeys 是允许的排序字段，非法值回落到 cpu。
var procSortKeys = map[string]bool{
	"cpu": true, "mem": true, "rss": true, "pid": true,
	"name": true, "threads": true, "uptime": true, "cpu_time": true,
}

// ListProcesses 采样当前全部进程，按 sortBy/desc 排列后返回前 limit 条。
// q 非空时按进程名做不区分大小写的包含匹配（在截断前过滤，因此是全量进程范围）。
// 第二个返回值是匹配到的进程总数（截断前），供前端展示。
func ListProcesses(sortBy string, desc bool, q string, limit int) ([]ProcDetail, int, error) {
	if !procSortKeys[sortBy] {
		sortBy = "cpu"
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q = strings.ToLower(strings.TrimSpace(q))

	procs, err := process.Processes()
	if err != nil {
		return nil, 0, err
	}

	// 第一轮：记录 CPU 累计时间与读取时刻，作为区间计算的起点。
	// 每个进程各自记时，这样即使遍历本身有耗时，换算出来的百分比也不会被放大。
	type seed struct {
		p   *process.Process
		cpu float64
		at  time.Time
		ok  bool
	}
	seeds := make([]seed, 0, len(procs))
	for _, p := range procs {
		s := seed{p: p}
		if ts, err := p.Times(); err == nil {
			s.cpu, s.at, s.ok = ts.Total(), time.Now(), true
		}
		seeds = append(seeds, s)
	}

	time.Sleep(ProcSampleInterval)

	self := int32(os.Getpid())

	// 第二轮：只重读 CPU 时间（紧循环），把每个进程的采样窗口压到接近名义值
	type cpuResult struct {
		pct   float64
		total float64
		ok    bool
	}
	cpu := make([]cpuResult, len(seeds))
	for i := range seeds {
		s := &seeds[i]
		if !s.ok {
			continue
		}
		ts, err := s.p.Times()
		if err != nil {
			continue
		}
		el := time.Since(s.at).Seconds()
		used := ts.Total() - s.cpu
		if used < 0 {
			used = 0
		}
		r := cpuResult{total: ts.Total(), ok: true}
		if el > 0 {
			r.pct = used / el * 100
		}
		cpu[i] = r
	}

	var totalMem uint64
	if vm, err := mem.VirtualMemory(); err == nil {
		totalMem = vm.Total
	}
	threads := processThreadCounts() // Windows 一次快照批量取，Linux 返回 nil
	now := time.Now()

	out := make([]ProcDetail, 0, len(seeds))
	for i := range seeds {
		p := seeds[i].p
		d := ProcDetail{PID: p.Pid, Self: p.Pid == self}

		if name, err := p.Name(); err == nil {
			d.Name = name
		}
		if d.Name == "" {
			// 部分内核线程拿不到 name，用命令行兜底，避免表格出现空行
			if cl, err := p.Cmdline(); err == nil {
				d.Name = firstField(cl)
			}
		}
		if d.Name == "" {
			continue // 已退出或不可读
		}
		if q != "" && !strings.Contains(strings.ToLower(d.Name), q) {
			continue
		}
		if ppid, err := p.Ppid(); err == nil {
			d.PPID = ppid
		}
		if st, err := p.Status(); err == nil && len(st) > 0 {
			d.Status = st[0] // 部分平台会返回多个状态，取主状态即可
		}
		if n, ok := threads[p.Pid]; ok {
			d.Threads = n
		} else if n, err := p.NumThreads(); err == nil {
			d.Threads = n
		}
		if ct, err := p.CreateTime(); err == nil && ct > 0 {
			d.StartAt = ct
			d.UptimeSec = int64(now.Sub(time.UnixMilli(ct)).Seconds())
			if d.UptimeSec < 0 {
				d.UptimeSec = 0
			}
		}
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			d.RSS, d.VMS = mi.RSS, mi.VMS
			if totalMem > 0 {
				d.MemPct = roundPct(float64(mi.RSS) / float64(totalMem) * 100)
			}
		}
		if r := cpu[i]; r.ok {
			d.CPUTime = roundPct(r.total)
			d.CPU = roundPct(r.pct)
		}
		out = append(out, d)
	}

	sortProcesses(out, sortBy, desc)
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}

	// 用户名与命令行属于「重字段」（Username 每次都要解析 passwd，Cmdline 要读 cmdline 文件），
	// 只对最终展示的这几百条补取，避免遍历全部进程时做上千次无谓 IO。
	for i := range out {
		p, err := process.NewProcess(out[i].PID)
		if err != nil {
			continue
		}
		if u, err := p.Username(); err == nil {
			out[i].User = u
		}
		if cl, err := p.Cmdline(); err == nil {
			out[i].Cmdline = strings.TrimSpace(truncStr(cl, 400))
		}
		if exe, err := p.Exe(); err == nil {
			out[i].Exe = exe
		}
	}
	return out, total, nil
}

func sortProcesses(list []ProcDetail, key string, desc bool) {
	sort.SliceStable(list, func(i, j int) bool {
		var less, greater bool
		switch key {
		case "pid":
			less, greater = list[i].PID < list[j].PID, list[i].PID > list[j].PID
		case "name":
			less, greater = list[i].Name < list[j].Name, list[i].Name > list[j].Name
		case "mem":
			less, greater = list[i].MemPct < list[j].MemPct, list[i].MemPct > list[j].MemPct
		case "rss":
			less, greater = list[i].RSS < list[j].RSS, list[i].RSS > list[j].RSS
		case "threads":
			less, greater = list[i].Threads < list[j].Threads, list[i].Threads > list[j].Threads
		case "uptime":
			less, greater = list[i].UptimeSec < list[j].UptimeSec, list[i].UptimeSec > list[j].UptimeSec
		case "cpu_time":
			less, greater = list[i].CPUTime < list[j].CPUTime, list[i].CPUTime > list[j].CPUTime
		default: // cpu
			less, greater = list[i].CPU < list[j].CPU, list[i].CPU > list[j].CPU
		}
		if desc {
			return greater
		}
		return less
	})
}

// KillProcess 结束进程。force=true 用 SIGKILL（无法被捕获），否则 SIGTERM（可优雅退出）。
// Windows 上两者都是 TerminateProcess，无区别。
func KillProcess(pid int32, force bool) error {
	if pid <= 1 {
		return errors.New("不允许结束 PID ≤ 1 的进程")
	}
	if pid == int32(os.Getpid()) {
		return errors.New("不能结束 Warden 自身进程（会立即断开面板）")
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return errors.New("进程不存在或已退出")
	}
	// 二次确认：避免 PID 复用后误杀（拿到对象后再核一次名字是否可读）
	if _, err := p.Name(); err != nil {
		return errors.New("进程不存在、已退出或权限不足")
	}
	if force {
		err = p.Kill()
	} else {
		err = p.Terminate()
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("权限不足：需要 root 或进程属主才能结束该进程")
		}
		return err
	}
	return nil
}

func firstField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " \t"); i > 0 {
		return s[:i]
	}
	return s
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func roundPct(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
